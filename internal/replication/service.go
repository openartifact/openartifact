// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
	"go.uber.org/zap"
)

// Config is what New needs.
type Config struct {
	// Primary is the store clients read and write: the tiered store.
	Primary storage.BlobStore
	// Replica is the secondary backend.
	Replica storage.BlobStore
	// Queue is the durable queue and state (the PostgresDB).
	Queue database.BlobReplicationStore
	// SQLDB is the pool the verify lock's pinned connection comes from; nil
	// disables the lock (a single replica).
	SQLDB *sql.DB
	// Lock is the per-blob lock (storage.WithBlobLock); nil runs without one.
	Lock LockFunc
	// Info describes the replica for the status endpoint.
	Info ReplicaInfo
	// Options tune the workers.
	Options Options
	// Metrics may be nil.
	Metrics *metrics.ReplicationMetrics
	Log     *logger.Logger
}

// ReplicaInfo is the credential-free description of the replica.
type ReplicaInfo struct {
	Type     string `json:"type"`
	Location string `json:"location"`
}

// Service ties the replicating store, the queue worker, the verifier and the
// read repair together.
type Service struct {
	store      *Store
	replicator *Replicator
	verifier   *Verifier
	repairs    *repairer
	info       ReplicaInfo
	core       *core

	verifyPoll time.Duration

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	runCtx    context.Context //nolint:containedctx // lifecycle context of the background loops
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// New assembles the replication service. Nothing runs until Start.
func New(cfg Config) (*Service, error) {
	if cfg.Primary == nil || cfg.Replica == nil {
		return nil, errors.New("replication needs a primary and a replica store") //nolint:err113 // programming error
	}

	if cfg.Queue == nil {
		return nil, errNoQueue
	}

	if cfg.Options.Batch < 1 || cfg.Options.Concurrency < 1 || cfg.Options.Interval <= 0 || cfg.Options.MaxBackoff <= 0 {
		return nil, errors.New("replication options are incomplete") //nolint:err113 // programming error
	}

	m := cfg.Metrics
	if m == nil {
		m = metrics.NewReplicationMetrics()
	}

	log := cfg.Log
	if log == nil {
		log = &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
	}

	kickCh := make(chan struct{}, 1)
	c := &core{
		inner: cfg.Primary, replica: cfg.Replica, queue: cfg.Queue, lock: cfg.Lock,
		opts: cfg.Options, log: log, m: m,
		now: time.Now, jitter: defaultJitter,
		kick: func() {
			select {
			case kickCh <- struct{}{}:
			default:
			}
		},
	}

	runCtx, cancel := context.WithCancel(context.Background())
	repairs := newRepairer(c)

	return &Service{
		store:      &Store{core: c, repairs: repairs},
		replicator: newReplicator(c, kickCh),
		verifier:   &Verifier{core: c, sqlDB: cfg.SQLDB, budget: verifyBudget, window: verifyWindow},
		repairs:    repairs,
		info:       cfg.Info,
		core:       c,
		verifyPoll: max(cfg.Options.Interval, time.Second),
		stopCh:     make(chan struct{}),
		runCtx:     runCtx, cancel: cancel,
	}, nil
}

// Store returns the blob store to give the server: the primary store plus
// replication.
func (s *Service) Store() *Store { return s.store }

// Replicator returns the queue worker.
func (s *Service) Replicator() *Replicator { return s.replicator }

// Verifier returns the verifier.
func (s *Service) Verifier() *Verifier { return s.verifier }

// Start launches the queue worker, the verifier and the read repair.
func (s *Service) Start() {
	s.startOnce.Do(func() {
		s.initVerifyGauge()
		s.replicator.Start()
		s.repairs.run(s.runCtx, &s.wg)

		s.wg.Add(1)

		go s.verifyLoop()
	})
}

// Stop ends the loops: the queue worker finishes the copies in flight (up to a
// grace period) and leaves the rest queued. It is idempotent.
func (s *Service) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.replicator.Stop()
	s.cancel()
	s.wg.Wait()
}

func (s *Service) initVerifyGauge() {
	ctx, cancel := context.WithTimeout(s.runCtx, dbTimeout)
	defer cancel()

	st, err := s.core.queue.GetBlobReplicationState(ctx)
	if err != nil {
		return
	}

	if last, ok := decodeResult(st.LastVerify); ok && last.FinishedAt != nil {
		s.core.m.LastVerify.Set(float64(last.FinishedAt.Unix()))
	}
}

func (s *Service) verifyLoop() {
	defer s.wg.Done()

	t := time.NewTicker(s.verifyPoll)
	defer t.Stop()

	for {
		s.verifyTick()

		select {
		case <-s.stopCh:
			return
		case <-t.C:
		}
	}
}

func (s *Service) verifyTick() {
	defer func() {
		if p := recover(); p != nil {
			s.core.log.Errorw("replication: verify panicked", "panic", p)
		}
	}()

	if err := s.verifier.Tick(s.runCtx); err != nil && s.runCtx.Err() == nil {
		s.core.log.Warnw("replication: verify tick failed", "error", err)
	}
}

// Status is the replication state the admin API reports.
type Status struct {
	Enabled      bool        `json:"enabled"`
	Replica      ReplicaInfo `json:"replica"`
	ReadFallback string      `json:"read_fallback"`

	BacklogCopy          int64   `json:"backlog_copy"`
	BacklogDelete        int64   `json:"backlog_delete"`
	OldestPendingSeconds float64 `json:"oldest_pending_age_seconds"`
	// Failing is the number of queued operations that failed repeatedly.
	Failing int64 `json:"failing"`

	ReplicatedTotal int64      `json:"replicated_total"`
	ReplicatedBytes int64      `json:"replicated_bytes_total"`
	LastError       string     `json:"last_error,omitempty"`
	LastErrorAt     *time.Time `json:"last_error_at,omitempty"`

	VerifyRequested bool          `json:"verify_requested"`
	VerifyRunning   *VerifyResult `json:"verify_running,omitempty"`
	LastVerify      *VerifyResult `json:"last_verify,omitempty"`
}

// Status reads the queue and the shared state.
func (s *Service) Status(ctx context.Context) (Status, error) {
	b, err := s.core.queue.BlobReplicationBacklog(ctx, failingAttempts)
	if err != nil {
		return Status{}, fmt.Errorf("replication backlog: %w", err)
	}

	st, err := s.core.queue.GetBlobReplicationState(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("replication state: %w", err)
	}

	out := Status{
		Enabled: true, Replica: s.info, ReadFallback: string(s.core.opts.ReadFallback),
		BacklogCopy: b.Copy, BacklogDelete: b.Delete, OldestPendingSeconds: b.OldestAgeSeconds, Failing: b.Failing,
		ReplicatedTotal: st.ReplicatedTotal, ReplicatedBytes: st.ReplicatedBytes,
		LastError: st.LastError, LastErrorAt: st.LastErrorAt,
		VerifyRequested: st.VerifyRequested,
	}

	if out.LastError == "" {
		out.LastErrorAt = nil
	}

	if r, ok := decodeResult(st.VerifyRun); ok {
		out.VerifyRunning = &r
	}

	if r, ok := decodeResult(st.LastVerify); ok {
		out.LastVerify = &r
	}

	return out, nil
}

// RequestVerify asks for a verify pass; false when one is already running or
// requested.
func (s *Service) RequestVerify(ctx context.Context) (bool, error) {
	return s.verifier.RequestRun(ctx)
}
