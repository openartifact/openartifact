// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"

	"github.com/openartifact/openartifact/internal/api"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/replication"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/internal/worker"
)

// storageStack is the blob storage as layered at startup: the configured
// backend, storage tiers on top of it, optional replication, and the metrics
// wrapper every writer uses. served adds presigned download redirects for the
// read paths.
type storageStack struct {
	raw        storage.BlobStore
	tiers      *tiering.Registry
	tierAccess *tiering.AccessTracker
	tiered     *tiering.Store

	// replSvc is nil unless storage.replica is configured.
	replSvc   *replication.Service
	replAdmin api.ReplicationService
	replStop  func()

	// sweep is what the blob deletion sweeper removes bytes through.
	sweep worker.BlobByteStore

	blobs  *storage.MetricsBlobStore
	served *presign.Store
}

// setupStorage opens the configured backend, layers tiers and replication on
// it, and wraps the result with metrics.
func (a *app) setupStorage() error {
	raw, err := storage.NewBlobStore(a.cfg.Storage)
	if err != nil {
		return a.fail("failed to initialize storage", err)
	}

	a.store.raw = raw

	if err := a.setupTiers(); err != nil {
		return err
	}

	top, err := a.setupReplication()
	if err != nil {
		return err
	}

	a.store.blobs = storage.NewMetricsBlobStore(top, a.reg)

	a.log.Infow("Storage initialized", "type", a.cfg.Storage.Type, "path", a.cfg.Storage.Path)

	return nil
}

// setupTiers builds the storage tiers (docs/operations/storage-tiering.md):
// reads find a blob on whichever tier holds it, new blobs land on the primary
// store. With no tier besides the primary one the wrapper forwards straight to
// it.
func (a *app) setupTiers() error {
	cfg := a.cfg

	a.store.tiers = tiering.NewRegistry(a.db, a.store.raw, cfg.Storage, a.log)
	if cfg.StorageReplica.Enabled() {
		// A tier backend must never be the blob replica's location.
		a.store.tiers.Reserve(cfg.StorageReplica.StorageConfig)
	}

	if err := a.store.tiers.Start(context.Background(), cfg.Tiering.RefreshInterval); err != nil {
		return a.fail("failed to load storage tiers", err)
	}

	a.store.tierAccess = tiering.NewAccessTracker(a.db, cfg.Tiering.AccessFlushInterval, a.log)
	a.store.tierAccess.Start()

	a.store.tiered = tiering.NewStore(a.store.tiers, a.db, a.store.tierAccess, a.reg, a.log)

	return nil
}

// setupReplication builds blob replication (docs/operations/replication.md):
// with storage.replica set, every write and delete is queued for a secondary
// backend and reads the primary cannot answer fall back to it. Unset, the
// stack is unchanged. It returns the top of the stack.
func (a *app) setupReplication() (storage.BlobStore, error) {
	s := &a.store
	s.replStop = func() {}
	s.sweep = s.tiered

	replSvc, err := newReplication(a.cfg, a.db, s.tiered, a.reg, a.log)
	if err != nil {
		return nil, a.fail("failed to initialize blob replication", err)
	}

	if replSvc == nil {
		return s.tiered, nil
	}

	s.replSvc, s.sweep, s.replAdmin, s.replStop = replSvc, replSvc.Store(), replSvc, replSvc.Stop

	a.log.Infow("Blob replication enabled", "replica", a.cfg.StorageReplica.Type, "location", a.cfg.StorageReplica.Summary(), "read_fallback", a.cfg.StorageReplica.ReadFallback)

	return replSvc.Store(), nil
}

// startGomodRepair repairs Go module zips left behind by the <=0.4.1
// shared-blob bug. A no-op (one cheap query) when nothing is affected; one
// replica at a time. It ends with the server.
func (a *app) startGomodRepair() {
	a.tasks.Go(func(ctx context.Context) {
		if _, err := formatgomod.RepairZipBlobs(ctx, a.db, a.store.blobs, a.log); err != nil && ctx.Err() == nil {
			a.log.Errorw("gomod repair failed; run `openartifact-server -repair-gomod` to retry", "error", err)
		}
	})
}

// setupServedStore builds what the download paths (format handlers, proxy
// cache) read from: the same store, plus 307 redirects to presigned URLs for
// repositories that have them enabled (storage.presignDownloads or the
// per-repository override).
func (a *app) setupServedStore() {
	a.store.served = presign.Wrap(a.store.blobs, a.db, presign.SettingsFromConfig(a.cfg.Storage), a.reg, a.log)
	if a.cfg.Storage.PresignDownloads && !a.store.served.Supported() {
		a.log.Warnw("storage.presignDownloads is on but the storage backend cannot presign URLs; downloads are streamed", "type", a.cfg.Storage.Type)
	}
}
