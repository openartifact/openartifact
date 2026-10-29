// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/internal/workq"
	"github.com/openartifact/openartifact/pkg/logger"
)

// federationKickKind is the kind of the worker request that wakes the pull
// scheduler, from a process that does not run it (server.role=web).
const federationKickKind = "federation:kick"

// federationKickTimeout bounds writing one kick to the database.
const federationKickTimeout = 5 * time.Second

// federationRuntime is what federation adds to the server: the store the API
// reads, and, when federation.enabled, the peer endpoints and, where the
// workers run, the pull worker.
type federationRuntime struct {
	store *federation.Store
	// server answers /federation/v1; nil when federation is off.
	server *federation.Server
	// kick wakes the pull scheduler; nil when federation is off. A web process
	// writes a worker request for it.
	kick func()
	// stop ends the pull worker; nil when federation is off or the process
	// does not run workers.
	stop func()
}

// regenNotifier queues the metadata regeneration of replicated changes on the
// format workers (the RegenQueue).
type regenNotifier struct {
	maven *worker.MavenMetadataWorker
	npm   *worker.NPMMetadataWorker
}

func (n regenNotifier) NotifyMaven(repoID, groupID, artifactID string) {
	n.maven.NotifyStale(repoID, groupID, artifactID)
}

func (n regenNotifier) NotifyNPM(packageID string) { n.npm.NotifyStale(packageID) }

// setupFederation builds the federation runtime. With federation.enabled off
// only the store exists (repositories created federated earlier keep their log,
// but nothing pulls or serves until it is turned on). A process that does not
// run the workers (w.requests is nil) serves the peer endpoints and leaves the
// pull to a worker process.
func setupFederation(
	ctx context.Context,
	cfg *config.Config,
	db *database.PostgresDB,
	blobs storage.BlobStore,
	log *logger.Logger,
	publisher webhook.Publisher,
	w *workerSet,
) (*federationRuntime, error) {
	store := federation.NewStore(db.GetDB())
	rt := &federationRuntime{store: store}

	if !cfg.Federation.Enabled {
		return rt, nil
	}

	rt.server = federation.NewServer(store, db, blobs, log)

	if w.requests == nil {
		rt.kick = func() { kickPull(w.queue, log) }

		log.Infow("Federation enabled; the pull worker runs in a worker process")

		return rt, nil
	}

	metrics, err := federation.NewMetrics(prometheus.DefaultRegisterer, store)
	if err != nil {
		return nil, fmt.Errorf("register federation metrics: %w", err)
	}

	engine, err := federation.NewEngine(ctx, federation.Options{
		Store:              store,
		SQL:                db.GetDB(),
		DB:                 db,
		Blobs:              blobs,
		Log:                log,
		Webhooks:           publisher,
		Notify:             regenNotifier{maven: w.maven, npm: w.npm},
		Metrics:            metrics,
		PollInterval:       cfg.Federation.PollInterval,
		BatchSize:          cfg.Federation.BatchSize,
		MaxBackoff:         cfg.Federation.MaxBackoff,
		RequestTimeout:     cfg.Federation.RequestTimeout,
		TombstoneRetention: cfg.Federation.TombstoneRetention,
		SpoolDir:           cfg.Federation.SpoolDir,
		MaxPartialBytes:    cfg.Federation.MaxPartialBytes,
		MaxPartialAge:      cfg.Federation.MaxPartialAge,
		TrashDefaultDays:   cfg.Trash.RetentionDays,
	})
	if err != nil {
		return nil, fmt.Errorf("start federation: %w", err)
	}

	pull := engine.StartWorker(ctx)

	rt.kick = engine.Kick
	rt.stop = pull.Stop

	w.requests.Handle(federationKickKind, func(context.Context, workq.Request) workq.Result {
		engine.Kick()
		return workq.Done
	})

	log.Infow("Federation enabled", "poll_interval", cfg.Federation.PollInterval.String(), "batch_size", cfg.Federation.BatchSize)

	return rt, nil
}

// kickPull asks a worker process to start a pull round now. A lost kick costs
// at most one poll interval.
func kickPull(q *workq.Store, log *logger.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), federationKickTimeout)
	defer cancel()

	if err := q.Put(ctx, federationKickKind, "pull", nil); err != nil {
		log.Warnw("federation: queueing a pull for the worker failed", "error", err)
	}
}
