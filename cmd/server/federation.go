// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// federationRuntime is what federation adds to the server: the store the API
// reads, and, when federation.enabled, the pull worker and the peer endpoints.
type federationRuntime struct {
	store *federation.Store
	// server answers /federation/v1; nil when federation is off.
	server *federation.Server
	// kick wakes the pull scheduler; nil when federation is off.
	kick func()
	// stop ends the pull worker; nil when federation is off.
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
// but nothing pulls or serves until it is turned on).
func setupFederation(ctx context.Context, cfg *config.Config, db *database.PostgresDB, blobs storage.BlobStore,
	log *logger.Logger, publisher webhook.Publisher, maven *worker.MavenMetadataWorker, npm *worker.NPMMetadataWorker) (*federationRuntime, error) {
	store := federation.NewStore(db.GetDB())
	rt := &federationRuntime{store: store}

	if !cfg.Federation.Enabled {
		return rt, nil
	}

	metrics, err := federation.NewMetrics(prometheus.DefaultRegisterer, store)
	if err != nil {
		return nil, fmt.Errorf("register federation metrics: %w", err)
	}

	engine, err := federation.NewEngine(ctx, federation.Options{
		Store: store, SQL: db.GetDB(), DB: db, Blobs: blobs, Log: log,
		Webhooks: publisher, Notify: regenNotifier{maven: maven, npm: npm}, Metrics: metrics,
		PollInterval: cfg.Federation.PollInterval, BatchSize: cfg.Federation.BatchSize,
		MaxBackoff: cfg.Federation.MaxBackoff, RequestTimeout: cfg.Federation.RequestTimeout,
		TombstoneRetention: cfg.Federation.TombstoneRetention,
		SpoolDir:           cfg.Federation.SpoolDir, MaxPartialBytes: cfg.Federation.MaxPartialBytes, MaxPartialAge: cfg.Federation.MaxPartialAge,
		TrashDefaultDays: cfg.Trash.RetentionDays,
	})
	if err != nil {
		return nil, fmt.Errorf("start federation: %w", err)
	}

	w := engine.StartWorker(ctx)

	rt.server = federation.NewServer(store, db, blobs, log)
	rt.kick = engine.Kick
	rt.stop = w.Stop

	log.Infow("Federation enabled", "poll_interval", cfg.Federation.PollInterval.String(), "batch_size", cfg.Federation.BatchSize)

	return rt, nil
}
