// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"time"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// workerSet is the background workers and the services built with them.
type workerSet struct {
	gpgKeys  *gpgkeys.Service
	proxySvc *proxy.Service

	webhook *webhook.Dispatcher

	rpm       *worker.RPMetadataWorker
	deb       *worker.DebMetadataWorker
	groupMeta *worker.GroupMetadataWorker
	maven     *worker.MavenMetadataWorker
	npm       *worker.NPMMetadataWorker

	proxyCache          *worker.ProxyCacheWorker
	authPurge           *worker.AuthPurgeWorker
	cleanup             *worker.CleanupWorker
	presignedUpload     *worker.PresignedUploadWorker
	dockerGC            *worker.DockerGCWorker
	dockerUploadSweeper *worker.DockerUploadSweeper
	trashPurge          *worker.TrashPurgeWorker
	auditRetention      *worker.AuditRetentionWorker

	tierEngine  *tiering.Engine
	tierWorker  *worker.TierMoverWorker
	blobSweeper *worker.BlobDeletionSweeper

	// journalWorker is nil unless the recovery journal is enabled.
	journal       *recovery.Journal
	journalWorker *recovery.Worker
}

// setupWorkers builds and starts every background worker, and registers their
// stop for the end of serve.
func (a *app) setupWorkers() {
	a.setupSigning()
	a.startMetadataWorkers()
	a.startHousekeepingWorkers()
	a.startStorageWorkers()
	a.startJournal()

	// stopWorkers runs in the shutdown sequence; this covers early returns.
	a.onClose(a.stopWorkers)
}

// setupSigning builds the signer of every repository: its own database key,
// else the global active one, else the configured file/env key.
func (a *app) setupSigning() {
	// File/env GPG key (optional): the read-only fallback that signs wherever no
	// database-managed key (admin API) applies.
	signer := fileSigner(a.cfg, a.log)

	// Resolves the signer of each repository: its own database key, else the
	// global active one, else the configured file/env key.
	a.workers.gpgKeys = gpgkeys.New(a.db, a.log, signer)
}

// startMetadataWorkers builds the per-format metadata workers and the group
// worker, and queues the regeneration of whatever was left stale.
func (a *app) startMetadataWorkers() {
	w := &a.workers
	db, blobStore, log, reg := a.db, a.store.blobs, a.log, a.reg

	// One limiter bounds regenerations across every format: each pins up to two
	// database connections while it runs.
	regenLimiter := worker.NewRegenLimiter(a.cfg.Workers.MetadataRegenConcurrency)

	w.rpm = worker.NewRPMetadataWorker(db, blobStore, w.gpgKeys, log, reg).WithRegenLimiter(regenLimiter)
	a.tasks.Go(w.rpm.RegenerateStale)

	w.deb = worker.NewDebMetadataWorker(db, blobStore, w.gpgKeys, log).WithMetrics(reg).WithRegenLimiter(regenLimiter)

	if a.cfg.GPG.AutoGenerate {
		a.tasks.Go(func(ctx context.Context) { autoGenerateGPGKey(ctx, w.gpgKeys, log) })
	}

	// Deb and rpm group repositories: their merged metadata is regenerated when
	// a member changes (the hosted workers' stale hook) and read from the
	// proxy members' caches.
	w.proxySvc = proxy.NewService(log, db, a.store.served).WithDockerDigestTTL(a.cfg.Workers.ProxyDockerDigestTTL)
	w.groupMeta = worker.NewGroupMetadataWorker(db, blobStore, w.proxySvc, w.rpm, w.gpgKeys, log).
		WithMetrics(reg).WithRegenLimiter(regenLimiter)
	w.rpm.WithStaleHook(w.groupMeta.NotifyMemberChanged)
	w.deb.WithStaleHook(w.groupMeta.NotifyMemberChanged)

	// A key change re-signs a group through the group worker, not the hosted ones.
	w.gpgKeys.SetRefreshers(groupRefresher{db: db, hosted: w.rpm, group: w.groupMeta}, groupRefresher{db: db, hosted: w.deb, group: w.groupMeta})

	a.tasks.Go(w.groupMeta.RegenerateStale)

	w.maven = worker.NewMavenMetadataWorker(db, blobStore, db.GetDB(), log, reg).WithRegenLimiter(regenLimiter)
	w.npm = worker.NewNPMMetadataWorker(db, log, db.GetDB(), reg).WithRegenLimiter(regenLimiter)
}

// startHousekeepingWorkers starts the cache, purge, cleanup and webhook
// workers.
func (a *app) startHousekeepingWorkers() {
	w := &a.workers
	cfg, db, blobStore, log, reg := a.cfg, a.db, a.store.blobs, a.log, a.reg

	w.proxyCache = worker.NewProxyCacheWorker(db, blobStore, log)
	w.proxyCache.Start()

	w.authPurge = worker.NewAuthPurgeWorker(db, log)
	w.authPurge.SetWebhookDeliveryRetention(cfg.Workers.WebhookDeliveryRetentionDays)
	w.authPurge.Start()

	// Initialize cleanup worker
	w.cleanup = worker.NewCleanupWorker(db, blobStore, log)
	w.cleanup.SetMetadataNotifiers(w.rpm, w.deb)

	w.presignedUpload = worker.NewPresignedUploadWorker(db, blobStore, cfg.Storage.PresignUploadTTL, log)
	w.presignedUpload.Start()

	// Docker registry GC: untagged manifests past the grace period and the
	// blobs only they referenced. Also runs on demand from the admin API.
	w.dockerGC = worker.NewDockerGCWorker(db, blobStore, log, cfg.Workers.DockerGCInterval, cfg.Workers.DockerGCGracePeriod)

	// A cleanup run refreshes the maven metadata and npm packuments of what it
	// removed and collects the docker manifests it left untagged.
	w.cleanup.SetRegenNotifiers(w.maven, w.npm, w.dockerGC)
	w.cleanup.Start()

	w.dockerGC.Start()

	// Abandoned docker blob uploads: idle sessions and their staged files.
	w.dockerUploadSweeper = worker.NewDockerUploadSweeper(db, dockerUploadDir(cfg), cfg.Workers.DockerUploadTTL, log)
	w.dockerUploadSweeper.Start()

	// Initialize webhook dispatcher
	w.webhook = webhook.NewDispatcher(db, log)
	w.webhook.SetBaseURL(cfg.BaseURL)

	w.webhook.Start()

	// Retention purge: hard-deletes trash items past their window (see
	// docs/operations/trash.md).
	w.trashPurge = worker.NewTrashPurgeWorker(db, blobStore, log, cfg.Trash.PurgeInterval).WithMetrics(reg)
	w.trashPurge.SetPublisher(w.webhook)
	w.trashPurge.Start()

	// Audit log retention: deletes entries older than audit.retentionDays in
	// paced batches, one replica at a time (advisory lock). Off by default (see
	// docs/operations/audit-retention.md).
	w.auditRetention = worker.NewAuditRetentionWorker(db, log, cfg.Audit.RetentionDays, cfg.Audit.PurgeInterval, cfg.Audit.PurgeBatchSize).WithMetrics(reg)
	w.auditRetention.Start()
}

// startStorageWorkers starts the tier mover, the blob deletion sweeper and
// the replication service.
func (a *app) startStorageWorkers() {
	w, s := &a.workers, &a.store
	cfg := a.cfg

	// Storage tier mover: relocates blobs between tiers per policy, crash-safe
	// and throttled; one replica at a time (advisory lock).
	tierMover := tiering.NewMover(a.db, s.tiers, s.tiered, tiering.NewThrottle(cfg.Tiering.MaxBytesPerSecond),
		tiering.MoverConfig{VerifyReadback: cfg.Tiering.VerifyReadback}, a.reg, a.log)
	w.tierEngine = tiering.NewEngine(a.db, s.tiers, tierMover,
		tiering.EngineConfig{Batch: cfg.Tiering.Batch, Concurrency: cfg.Tiering.Concurrency}, a.reg, a.log)
	w.tierWorker = worker.NewTierMoverWorker(w.tierEngine, a.db.GetDB(), a.log, cfg.Tiering.Interval, cfg.Tiering.Enabled)
	w.tierWorker.Start()

	// Blob deletion sweeper: removes the bytes of deleted blobs and retired
	// tier copies after the grace period and once no running backup can still
	// need them. Every replica may run one (no global lock).
	w.blobSweeper = worker.NewBlobDeletionSweeper(a.db, s.sweep, a.log, cfg.BlobDeletion.SweepInterval, cfg.BlobDeletion.SweepBatch).WithMetrics(a.reg)
	w.blobSweeper.Start()

	if s.replSvc != nil {
		s.replSvc.Start()
	}
}

// startJournal starts the disaster-recovery journal (docs/operations/rebuild-from-storage.md):
// database triggers mark changed package rows, this worker mirrors each as a
// sidecar blob so `openartifact rebuild` can re-index an empty database.
// Sidecars go through blobStore, so they are replicated with everything else.
func (a *app) startJournal() {
	w, cfg := &a.workers, a.cfg

	w.journal = recovery.NewJournal(a.db.GetDB(), a.store.blobs, a.log, &a.reg.Recovery, recovery.JournalConfig{
		Batch: cfg.Recovery.Journal.Batch, Concurrency: cfg.Recovery.Journal.Concurrency,
		ReconcileInterval: cfg.Recovery.Journal.ReconcileInterval,
	})

	if err := w.journal.SetEnabled(context.Background(), cfg.Recovery.Journal.Enabled); err != nil {
		a.log.Errorw("recovery journal unavailable (is the schema migrated?)", "error", err)
		return
	}

	if cfg.Recovery.Journal.Enabled {
		w.journalWorker = recovery.NewWorker(w.journal, a.log, cfg.Recovery.Journal.Interval)
		w.journalWorker.Start()
	}
}

// dockerUploadDir is where docker blob uploads are staged.
func dockerUploadDir(cfg *config.Config) string {
	if cfg.Workers.DockerUploadDir != "" {
		return cfg.Workers.DockerUploadDir
	}

	return formatdocker.DefaultUploadDir
}

// fileSigner loads the configured file/env GPG key; nil when none is
// configured or it cannot be loaded (logged).
func fileSigner(cfg *config.Config, log *logger.Logger) gpg.Signer {
	if !cfg.GPG.Enabled || cfg.GPG.KeyPath == "" {
		return nil
	}

	s, err := gpg.NewLocalSignerFromFile(cfg.GPG.KeyPath, []byte(cfg.GPG.Passphrase))
	if err != nil {
		log.Warnw("Failed to initialize the configured GPG signer", "error", err)

		return nil
	}

	log.Infow("GPG signer initialized", "fingerprint", s.KeyFingerprint())

	return s
}

// autoGenerateGPGKey implements gpg.autoGenerate: when no signing key exists
// at all, create one (under a database lock, so replicas starting together
// create exactly one) and make it the global active key. It ends with ctx.
func autoGenerateGPGKey(ctx context.Context, svc *gpgkeys.Service, log *logger.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	created, err := svc.AutoGenerate(ctx)

	switch {
	case errors.Is(err, gpgkeys.ErrSecretKeyRequired):
		log.Warnw("gpg.autoGenerate is on but OPENARTIFACT_SECRET_KEY is not set: private keys are only stored encrypted, so no key was generated")
	case err != nil:
		log.Errorw("gpg.autoGenerate failed", "error", err)
	case created:
		log.Infow("gpg.autoGenerate: generated the repository signing key (RSA 4096) and made it globally active")
	}
}

// groupRefresher sends a key change to the worker that generates the
// repository's metadata: the group worker for a group, else the hosted worker.
type groupRefresher struct {
	db     database.Database
	hosted interface{ NotifyStale(repoID string) }
	group  *worker.GroupMetadataWorker
}

func (g groupRefresher) NotifyStale(repoID string) {
	repo, err := g.db.GetRepositoryByID(context.Background(), repoID)
	if err == nil && repo != nil && repo.RepoType == repotype.Group {
		g.group.NotifyStale(repoID)
		return
	}

	g.hosted.NotifyStale(repoID)
}
