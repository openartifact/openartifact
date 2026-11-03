// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// repoStatsLockKey names the cluster-wide advisory lock (hashed by
	// pglock.Try) that the replica running a pass holds.
	repoStatsLockKey = "openartifact:repo-stats"

	// repoStatsBatch is the number of repositories one transaction recomputes.
	repoStatsBatch = 50

	// repoStatsMaxAge is the age after which a repository's figures are
	// recomputed even if nothing marked it: the bound on drift that no write
	// announced (rows written with the triggers off, a restored backup).
	repoStatsMaxAge = time.Hour

	// repoStatsRunTimeout bounds one scheduled pass (and the pinned lock
	// connection); the next pass continues where it stopped.
	repoStatsRunTimeout = 10 * time.Minute
)

// RepoStatsPass is the outcome of one pass of the repo stats worker.
type RepoStatsPass struct {
	// Repositories counts the repositories whose figures were recomputed.
	Repositories int
	// Busy is set when another replica holds the lock; nothing was done.
	Busy bool
}

// RepoStatsStore is the part of the database RepoStatsWorker needs.
type RepoStatsStore interface {
	GetDB() *sql.DB
}

// RepoStatsWorker keeps repo_stats, the per-repository package count, newest
// publication and security counts of GET /api/v1/repositories, up to date: it
// recomputes the figures of the repositories a write marked in repo_stats_dirty,
// and of those whose figures are missing or older than an hour. The repository
// list reads the stored figures, so it lags the data by about the interval.
// Replicas are excluded from each other by a Postgres advisory lock held on a
// pinned connection for the pass; a pass interrupted at any point loses no mark
// (each batch claims its marks in the transaction that stores its figures).
type RepoStatsWorker struct {
	*lifecycle

	db       RepoStatsStore
	store    database.RepoStatsReconciler
	log      *logger.Logger
	interval time.Duration
	batch    int
	maxAge   time.Duration

	// lock takes the cluster-wide lock; a field so tests can simulate another
	// replica.
	lock func(ctx context.Context) (release func(), acquired bool, err error)
}

// NewRepoStatsWorker creates the worker, which runs a pass at Start and then
// one per interval. Without a database that implements
// database.RepoStatsReconciler, or with a non-positive interval, it does
// nothing.
func NewRepoStatsWorker(db RepoStatsStore, log *logger.Logger, interval time.Duration) *RepoStatsWorker {
	store, _ := db.(database.RepoStatsReconciler)

	w := &RepoStatsWorker{
		lifecycle: newLifecycle(),
		db:        db, store: store, log: log,
		interval: interval, batch: repoStatsBatch, maxAge: repoStatsMaxAge,
	}
	w.lock = func(ctx context.Context) (func(), bool, error) {
		return tryClusterLock(ctx, w.db.GetDB(), repoStatsLockKey)
	}

	return w
}

// Start runs a first pass right away, so a freshly migrated database is filled
// in without waiting, and then one per interval.
func (w *RepoStatsWorker) Start() {
	w.startOnce.Do(func() {
		if w.store == nil || w.interval <= 0 {
			close(w.doneCh)

			return
		}

		go w.loop()
	})
}

func (w *RepoStatsWorker) loop() {
	defer close(w.doneCh)

	w.runScheduled()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.runScheduled()
		case <-w.stopCh:
			return
		}
	}
}

// runScheduled runs one pass of the schedule. A panic ends the pass, not the
// schedule.
func (w *RepoStatsWorker) runScheduled() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in repo stats pass", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.runCtx, repoStatsRunTimeout)
	defer cancel()

	pass, err := w.RunOnce(ctx)
	if err != nil && w.runCtx.Err() == nil {
		w.log.Errorw("repo stats pass failed", "error", err, "repositories", pass.Repositories)
	}
}

// RunOnce runs one pass: batch after batch, it recomputes the marked and
// drifted repositories until a batch comes back short or ctx ends. A pass that
// finds the lock held by another replica does nothing and reports Busy. The
// error is non-nil only when the pass could not run to the end; the batches
// that committed stay stored.
func (w *RepoStatsWorker) RunOnce(ctx context.Context) (RepoStatsPass, error) {
	var pass RepoStatsPass

	if w.store == nil {
		return pass, nil
	}

	release, acquired, err := w.lock(ctx)
	if err != nil {
		return pass, fmt.Errorf("lock repo stats: %w", err)
	}

	if !acquired {
		pass.Busy = true

		return pass, nil
	}
	defer release()

	for {
		if err := ctx.Err(); err != nil {
			return pass, fmt.Errorf("repo stats interrupted: %w", err)
		}

		n, err := w.store.ReconcileRepoStats(ctx, w.batch, w.maxAge)
		if err != nil {
			return pass, fmt.Errorf("reconcile repo stats: %w", err)
		}

		pass.Repositories += n

		if n < w.batch {
			return pass, nil
		}
	}
}
