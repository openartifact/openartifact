// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"time"

	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/exploit"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/nixkeys"
	"github.com/openartifact/openartifact/internal/osv"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/internal/workq"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// workerSet is the background workers and the services built with them.
type workerSet struct {
	gpgKeys  *gpgkeys.Service
	apkKeys  *apkkeys.Service
	nixKeys  *nixkeys.Service
	proxySvc *proxy.Service

	webhook *webhook.Dispatcher

	rpm       *worker.RPMetadataWorker
	deb       *worker.DebMetadataWorker
	pacman    *worker.PacmanMetadataWorker
	apk       *worker.ApkMetadataWorker
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
	osvMirror           *worker.OSVMirrorWorker
	auditRetention      *worker.AuditRetentionWorker
	exploitFeeds        *worker.ExploitFeedsWorker
	// exploit marks findings with the KEV and EPSS signals; it is never nil.
	exploit *exploit.Enricher

	tierEngine  *tiering.Engine
	tierWorker  *worker.TierMoverWorker
	blobSweeper *worker.BlobDeletionSweeper

	// journalWorker is nil unless the recovery journal is enabled.
	journal       *recovery.Journal
	journalWorker *recovery.Worker

	// queue is where a process hands work to the worker processes (and where
	// they find it); requests takes it on a process that runs the workers and
	// is nil on a web one. See role.go.
	queue    *workq.Store
	requests *workq.Consumer

	// scan is built with the routes of an API process, or on its own on a worker.
	scan scanServices
}

// setupWorkers builds every background worker, starts them where server.role
// runs them, and registers their stop for the end of serve.
func (a *app) setupWorkers() {
	a.setupSigning()
	a.setupWorkRequests()
	a.startMetadataWorkers()
	a.startHousekeepingWorkers()
	a.startStorageWorkers()
	a.startExploitFeeds()
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
	a.background(w.rpm.RegenerateStale)

	w.deb = worker.NewDebMetadataWorker(db, blobStore, w.gpgKeys, log).WithMetrics(reg).WithRegenLimiter(regenLimiter)
	w.pacman = worker.NewPacmanMetadataWorker(db, blobStore, w.gpgKeys, log).WithMetrics(reg).WithRegenLimiter(regenLimiter)

	// Alpine apk repositories sign their index with a key of their own, not a GPG one.
	w.apkKeys = apkkeys.New(db, log)
	w.nixKeys = nixkeys.New(db, log)
	w.apk = worker.NewApkMetadataWorker(db, blobStore, w.apkKeys, log).WithMetrics(reg).WithRegenLimiter(regenLimiter)

	if a.cfg.GPG.AutoGenerate {
		a.background(func(ctx context.Context) { autoGenerateGPGKey(ctx, w.gpgKeys, log) })
	}

	// Deb, rpm and pacman group repositories: their merged metadata is regenerated when
	// a member changes (the hosted workers' stale hook) and read from the
	// proxy members' caches.
	w.proxySvc = proxy.NewService(log, db, a.store.served).WithDockerDigestTTL(a.cfg.Workers.ProxyDockerDigestTTL)
	w.groupMeta = worker.NewGroupMetadataWorker(db, blobStore, w.proxySvc, w.rpm, w.gpgKeys, log).
		WithApkSigners(w.apkKeys).WithMetrics(reg).WithRegenLimiter(regenLimiter)

	// A web process hands a hosted regeneration over without running the hook:
	// the worker that regenerates runs it (NotifyStale) and marks the groups.
	if a.cfg.Server.RunsWorkers() {
		w.rpm.WithStaleHook(w.groupMeta.NotifyMemberChanged)
		w.deb.WithStaleHook(w.groupMeta.NotifyMemberChanged)
		w.pacman.WithStaleHook(w.groupMeta.NotifyMemberChanged)
		w.apk.WithStaleHook(w.groupMeta.NotifyMemberChanged)
	}

	// An apk key change re-signs a group through the group worker too.
	w.apkKeys.SetRefresher(groupRefresher{db: db, hosted: w.apk, group: w.groupMeta})

	// A key change re-signs a group through the group worker, not the hosted ones.
	w.gpgKeys.SetRefreshers(
		groupRefresher{db: db, hosted: w.rpm, group: w.groupMeta},
		groupRefresher{db: db, hosted: w.deb, group: w.groupMeta},
		groupRefresher{db: db, hosted: w.pacman, group: w.groupMeta},
	)

	a.background(w.groupMeta.RegenerateStale)

	w.maven = worker.NewMavenMetadataWorker(db, blobStore, db.GetDB(), log, reg).WithRegenLimiter(regenLimiter)
	w.npm = worker.NewNPMMetadataWorker(db, log, db.GetDB(), reg).WithRegenLimiter(regenLimiter)

	a.linkRegens()
}

// startHousekeepingWorkers starts the cache, purge, cleanup and webhook
// workers.
func (a *app) startHousekeepingWorkers() {
	w := &a.workers
	cfg, db, blobStore, log, reg := a.cfg, a.db, a.store.blobs, a.log, a.reg

	w.proxyCache = worker.NewProxyCacheWorker(db, blobStore, log)
	a.start(w.proxyCache.Start)

	w.authPurge = worker.NewAuthPurgeWorker(db, log)
	w.authPurge.SetWebhookDeliveryRetention(cfg.Workers.WebhookDeliveryRetentionDays)
	a.start(w.authPurge.Start)

	// Initialize cleanup worker
	w.cleanup = worker.NewCleanupWorker(db, blobStore, log)
	w.cleanup.SetMetadataNotifiers(w.rpm, w.deb)
	w.cleanup.SetPacmanNotifier(w.pacman)
	w.cleanup.SetApkNotifier(w.apk)

	w.presignedUpload = worker.NewPresignedUploadWorker(db, blobStore, cfg.Storage.PresignUploadTTL, log)
	a.start(w.presignedUpload.Start)

	// Docker registry GC: untagged manifests past the grace period and the
	// blobs only they referenced. Also runs on demand from the admin API.
	w.dockerGC = worker.NewDockerGCWorker(db, blobStore, log, cfg.Workers.DockerGCInterval, cfg.Workers.DockerGCGracePeriod)

	// A cleanup run refreshes the maven metadata and npm packuments of what it
	// removed and collects the docker manifests it left untagged.
	w.cleanup.SetRegenNotifiers(w.maven, w.npm, w.dockerGC)
	a.start(w.cleanup.Start)
	a.start(w.dockerGC.Start)

	// Abandoned docker blob uploads: idle sessions and their staged files.
	w.dockerUploadSweeper = worker.NewDockerUploadSweeper(db, dockerUploadDir(cfg), cfg.Workers.DockerUploadTTL, log)
	a.start(w.dockerUploadSweeper.Start)

	// Initialize webhook dispatcher
	w.webhook = webhook.NewDispatcher(db, log)
	w.webhook.SetBaseURL(cfg.BaseURL)

	switch {
	case !cfg.Server.RunsWorkers():
		// A worker process delivers what this one records.
		w.webhook.DeferDeliveries()
	case cfg.Server.Role == config.RoleWorker:
		w.webhook.WithPollInterval(workerWebhookPoll)
	}

	a.start(w.webhook.Start)

	// Retention purge: hard-deletes trash items past their window (see
	// docs/operations/trash.md).
	w.trashPurge = worker.NewTrashPurgeWorker(db, blobStore, log, cfg.Trash.PurgeInterval).WithMetrics(reg)
	w.trashPurge.SetPublisher(w.webhook)
	a.start(w.trashPurge.Start)

	w.buildOSVMirror(db, cfg, log, reg)
	a.start(w.osvMirror.Start)

	// Audit log retention: deletes entries older than audit.retentionDays in
	// paced batches, one replica at a time (advisory lock). Off by default (see
	// docs/operations/audit-retention.md).
	w.auditRetention = worker.NewAuditRetentionWorker(db, log, cfg.Audit.RetentionDays, cfg.Audit.PurgeInterval, cfg.Audit.PurgeBatchSize).
		WithMetrics(reg)
	a.start(w.auditRetention.Start)
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
	a.start(w.tierWorker.Start)

	// Blob deletion sweeper: removes the bytes of deleted blobs and retired
	// tier copies after the grace period and once no running backup can still
	// need them. Every replica may run one (no global lock).
	w.blobSweeper = worker.NewBlobDeletionSweeper(a.db, s.sweep, a.log, cfg.BlobDeletion.SweepInterval, cfg.BlobDeletion.SweepBatch).
		WithMetrics(a.reg)
	a.start(w.blobSweeper.Start)

	if s.replSvc != nil {
		a.start(s.replSvc.Start)
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
		a.start(w.journalWorker.Start)
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
		log.Warnw(
			"gpg.autoGenerate is on but OPENARTIFACT_SECRET_KEY is not set: private keys are only stored encrypted, so no key was generated",
		)
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

// buildOSVMirror builds the OSV mirror worker; it does something only when
// osvMirror is enabled. A disabled worker is still built (and does nothing), so
// shutdown stops it like the others.
func (w *workerSet) buildOSVMirror(db database.Database, cfg *config.Config, log *logger.Logger, reg *metrics.Registry) {
	var importer *osv.Importer

	interval := time.Duration(0)

	if store, ok := db.(database.OSVStore); ok && cfg.OSVMirror.Enabled {
		var err error

		importer, err = osv.NewImporter(store, osv.Options{
			BaseURL:         cfg.OSVMirror.BaseURL,
			Ecosystems:      cfg.OSVMirror.Ecosystems,
			TempDir:         cfg.OSVMirror.TempDir,
			MaxArchiveBytes: cfg.OSVMirror.MaxArchiveBytes,
		}, log)
		if err != nil {
			log.Errorw("osv mirror disabled: invalid configuration", "error", err)

			importer = nil
		} else {
			interval = cfg.OSVMirror.Interval
		}
	}

	w.osvMirror = worker.NewOSVMirrorWorker(db, importer, cfg.OSVMirror.Ecosystems, log, interval).WithMetrics(reg)
	w.osvMirror.SetPublisher(w.webhook)
}
