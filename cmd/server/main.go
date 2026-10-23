// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatcargo "github.com/openartifact/openartifact/internal/format/cargo"
	formatconan "github.com/openartifact/openartifact/internal/format/conan"
	formatconda "github.com/openartifact/openartifact/internal/format/conda"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	formathelm "github.com/openartifact/openartifact/internal/format/helm"
	formativy "github.com/openartifact/openartifact/internal/format/ivy"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	formatnuget "github.com/openartifact/openartifact/internal/format/nuget"
	formatpypi "github.com/openartifact/openartifact/internal/format/pypi"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	formatrubygems "github.com/openartifact/openartifact/internal/format/rubygems"
	formatsbt "github.com/openartifact/openartifact/internal/format/sbt"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/license"
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repodispatch"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/secrets"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
	webhandler "github.com/openartifact/openartifact/web"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// telemetryFlushTimeout bounds the final span/metric export at shutdown.
const telemetryFlushTimeout = 5 * time.Second

var (
	version = "dev"
	//nolint:gochecknoglobals // build-time injection via -ldflags
	commit = "unknown"
	//nolint:gochecknoglobals // build-time injection via -ldflags
	buildTime = "unknown"
)

// versionLine renders the build metadata stamped via -ldflags in the form
// "<version> <commit> <build time> <go>/<os>/<arch>"; unstamped builds print
// the "dev"/"unknown" defaults.
func versionLine() string {
	return fmt.Sprintf("%s %s %s %s/%s/%s", version, commit, buildTime, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if _, reported := errors.AsType[reportedError](err); !reported {
			fmt.Fprintf(os.Stderr, "openartifact-server: %v\n", err)
		}

		os.Exit(1)
	}
}

var (
	errGomodRepairBusy       = errors.New("gomod repair: another instance is already running it")
	errGomodRepairUnrepaired = errors.New("go module versions could not be repaired and must be re-uploaded")
)

var errVerifyUsage = errors.New("usage: openartifact-server verify <config.yaml>")

// reportedError marks a failure the operator has already seen (structured log
// line or flag usage), so main exits non-zero without printing it again.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// run is the whole server lifetime: it returns nil after a clean shutdown (or
// for -migrate, verify and -h) and an error for every startup or runtime
// failure, so the process never exits 0 while the service is not running.
func run(args []string) error {
	flags := flag.NewFlagSet("openartifact-server", flag.ContinueOnError)
	migrateFlag := flags.Bool("migrate", false, "run migrations and exit")
	repairGomodFlag := flags.Bool("repair-gomod", false, "relink Go module zips damaged by the <=0.4.1 shared-blob bug, then exit")
	versionFlag := flags.Bool("version", false, "print version, commit, build time and Go runtime, then exit")
	healthcheckFlag := flags.Bool("healthcheck", false, "GET /health on 127.0.0.1 at the configured port and exit 0 on 200, 1 otherwise")
	configFile := flags.String("config", os.Getenv("OPENARTIFACT_CONFIG"), "path to YAML config file")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		// The FlagSet already printed the problem and the usage text.
		return reportedError{err}
	}

	if *versionFlag {
		fmt.Println(versionLine())

		return nil
	}

	if *healthcheckFlag {
		return runHealthcheck(*configFile)
	}

	// Handle 'verify' subcommand: openartifact-server verify config.yaml
	if flags.NArg() > 0 && flags.Arg(0) == "verify" {
		path := *configFile
		if flags.NArg() > 1 {
			path = flags.Arg(1)
		}

		if path == "" {
			return errVerifyUsage
		}

		if err := config.ValidateFile(path); err != nil {
			return fmt.Errorf("config validation failed: %w", err)
		}

		fmt.Printf("Config file %q is valid.\n", path)

		return nil
	}

	migrateOnStart := os.Getenv("MIGRATE_ON_START") != "false"

	// Load configuration
	cfg, err := config.Load(*configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	if err := crypto.InitFromEnv(); err != nil {
		return fmt.Errorf("invalid %s: %w", crypto.EnvKey, err)
	}

	// Initialize logger
	log, err := logger.New(cfg.Logger.Level, cfg.Logger.Format, cfg.Logger.OutputPath)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	// fail logs a fatal startup/runtime error through the structured logger and
	// returns it for main to turn into a non-zero exit status.
	fail := func(msg string, err error) error {
		log.Errorw(msg, "error", err)

		return reportedError{fmt.Errorf("%s: %w", msg, err)}
	}

	if !crypto.Enabled() {
		log.Warnw("OPENARTIFACT_SECRET_KEY not set: webhook secrets, upstream passwords and other stored credentials are NOT encrypted at rest")
	}

	log.Infow("Starting artifact repository manager",
		"version", version,
		"commit", commit,
		"build_time", buildTime,
		"server_host", cfg.Server.Host,
		"server_port", cfg.Server.Port,
	)

	// OpenTelemetry (off unless telemetry.otlp.enabled): installed before the
	// database and the HTTP clients are used, flushed on the way out.
	shutdownTelemetry, err := telemetry.Setup(context.Background(), cfg.Telemetry,
		telemetry.Build{Version: version, InstanceID: cfg.Server.InstanceID}, prometheus.DefaultGatherer, log)
	if err != nil {
		return fail("failed to initialize telemetry", err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
		defer cancel()

		if err := shutdownTelemetry(ctx); err != nil {
			log.Warnw("telemetry shutdown", "error", err)
		}
	}()

	if cfg.Telemetry.OTLP.Enabled {
		log.Infow("OpenTelemetry enabled",
			"protocol", cfg.Telemetry.OTLP.Protocol,
			"service_name", cfg.Telemetry.OTLP.ServiceName,
			"sample_ratio", cfg.Telemetry.OTLP.SampleRatio,
			"metrics", cfg.Telemetry.OTLP.Metrics,
		)
	}

	// Initialize database
	db, err := database.NewPostgresDB(cfg.Database, log)
	if err != nil {
		return fail("failed to connect to database", err)
	}
	defer func() { _ = db.Close() }()

	db.SetTrashRetentionDays(cfg.Trash.RetentionDays)
	db.SetBlobDeletionGrace(cfg.BlobDeletion.Grace)

	log.Infow("Database connected",
		"host", cfg.Database.Host,
		"database", cfg.Database.Database,
	)

	if *migrateFlag {
		if err := db.Migrate(context.Background()); err != nil {
			return fail("migration failed", err)
		}

		log.Infow("Migrations applied successfully")

		return nil
	}

	if migrateOnStart {
		if err := db.Migrate(context.Background()); err != nil {
			return fail("auto-migrate failed", err)
		}
	} else {
		log.Warnw("MIGRATE_ON_START=false: automatic migrations skipped")
	}

	auth.ConfigureSessionDuration(time.Duration(cfg.Auth.SessionDurationHours) * time.Hour)

	if err := auth.BootstrapIfEmpty(context.Background(), db, log); err != nil {
		return fail("bootstrap failed", err)
	}

	reg := metrics.NewRegistry()

	auditLogger := audit.NewLogger(db, log, reg, 1000)
	audit.SetDefault(auditLogger)

	// Initialize storage
	rawBlobStore, err := storage.NewBlobStore(cfg.Storage)
	if err != nil {
		return fail("failed to initialize storage", err)
	}

	// Storage tiers (docs/operations/storage-tiering.md): reads find a blob on
	// whichever tier holds it, new blobs land on the primary store. With no
	// tier besides the primary one the wrapper forwards straight to it.
	tierRegistry := tiering.NewRegistry(db, rawBlobStore, cfg.Storage, log)
	tierRegistry.Start(context.Background(), cfg.Tiering.RefreshInterval)

	tierAccess := tiering.NewAccessTracker(db, cfg.Tiering.AccessFlushInterval, log)
	tierAccess.Start()

	tieredStore := tiering.NewStore(tierRegistry, db, tierAccess, reg, log)
	blobStore := storage.NewMetricsBlobStore(tieredStore, reg)

	log.Infow("Storage initialized", "type", cfg.Storage.Type, "path", cfg.Storage.Path)

	if *repairGomodFlag {
		return runGomodRepair(context.Background(), db, blobStore, log)
	}

	// Repair Go module zips left behind by the <=0.4.1 shared-blob bug. A no-op
	// (one cheap query) when nothing is affected; one replica at a time.
	go func() {
		if _, err := formatgomod.RepairZipBlobs(context.Background(), db, blobStore, log); err != nil {
			log.Errorw("gomod repair failed; run `openartifact-server -repair-gomod` to retry", "error", err)
		}
	}()

	// servedStore is what the download paths (format handlers, proxy cache)
	// read from: the same store, plus 307 redirects to presigned URLs for
	// repositories that have them enabled (storage.presignDownloads or the
	// per-repository override).
	servedStore := presign.Wrap(blobStore, db, presign.SettingsFromConfig(cfg.Storage), reg, log)
	if cfg.Storage.PresignDownloads && !servedStore.Supported() {
		log.Warnw("storage.presignDownloads is on but the storage backend cannot presign URLs; downloads are streamed", "type", cfg.Storage.Type)
	}

	// Initialize GPG signer (optional)
	var signer gpg.Signer

	if cfg.GPG.Enabled && cfg.GPG.KeyPath != "" {
		s, err := gpg.NewLocalSignerFromFile(cfg.GPG.KeyPath, []byte(cfg.GPG.Passphrase))
		if err != nil {
			log.Warnw("Failed to initialize GPG signer, signing disabled", "error", err)
		} else {
			signer = s
			log.Infow("GPG signer initialized", "fingerprint", s.KeyFingerprint())
		}
	}

	// Initialize metadata workers
	// One limiter bounds regenerations across every format: each pins up to two
	// database connections while it runs.
	regenLimiter := worker.NewRegenLimiter(cfg.Workers.MetadataRegenConcurrency)

	rpmWorker := worker.NewRPMetadataWorker(db, blobStore, signer, log, reg).WithRegenLimiter(regenLimiter)
	go rpmWorker.RegenerateStale(context.Background())

	debWorker := worker.NewDebMetadataWorker(db, blobStore, signer, log).WithMetrics(reg).WithRegenLimiter(regenLimiter)
	mavenWorker := worker.NewMavenMetadataWorker(db, blobStore, db.GetDB(), log, reg).WithRegenLimiter(regenLimiter)
	npmWorker := worker.NewNPMMetadataWorker(db, log, db.GetDB(), reg).WithRegenLimiter(regenLimiter)
	proxyCacheWorker := worker.NewProxyCacheWorker(db, blobStore, log)
	proxyCacheWorker.Start()

	authPurgeWorker := worker.NewAuthPurgeWorker(db, log)
	authPurgeWorker.SetWebhookDeliveryRetention(cfg.Workers.WebhookDeliveryRetentionDays)
	authPurgeWorker.Start()

	// Initialize cleanup worker
	cleanupWorker := worker.NewCleanupWorker(db, blobStore, log)
	cleanupWorker.SetMetadataNotifiers(rpmWorker, debWorker)

	cleanupWorker.Start()

	presignedUploadWorker := worker.NewPresignedUploadWorker(db, blobStore, cfg.Storage.PresignUploadTTL, log)
	presignedUploadWorker.Start()

	// Docker registry GC: untagged manifests past the grace period and the
	// blobs only they referenced. Also runs on demand from the admin API.
	dockerGCWorker := worker.NewDockerGCWorker(db, blobStore, log, cfg.Workers.DockerGCInterval, cfg.Workers.DockerGCGracePeriod)

	dockerGCWorker.Start()

	// Abandoned docker blob uploads: idle sessions and their staged files.
	dockerUploadSweeper := worker.NewDockerUploadSweeper(db, dockerUploadDir(cfg), cfg.Workers.DockerUploadTTL, log)
	dockerUploadSweeper.Start()

	// Initialize webhook dispatcher
	webhookDispatcher := webhook.NewDispatcher(db, log)
	webhookDispatcher.SetBaseURL(cfg.BaseURL)

	webhookDispatcher.Start()

	// Retention purge: hard-deletes trash items past their window (see
	// docs/operations/trash.md).
	trashPurgeWorker := worker.NewTrashPurgeWorker(db, blobStore, log, cfg.Trash.PurgeInterval).WithMetrics(reg)
	trashPurgeWorker.SetPublisher(webhookDispatcher)
	trashPurgeWorker.Start()

	// Storage tier mover: relocates blobs between tiers per policy, crash-safe
	// and throttled; one replica at a time (advisory lock).
	tierMover := tiering.NewMover(db, tierRegistry, tieredStore, tiering.NewThrottle(cfg.Tiering.MaxBytesPerSecond),
		tiering.MoverConfig{VerifyReadback: cfg.Tiering.VerifyReadback}, reg, log)
	tierEngine := tiering.NewEngine(db, tierRegistry, tierMover,
		tiering.EngineConfig{Batch: cfg.Tiering.Batch, Concurrency: cfg.Tiering.Concurrency}, reg, log)
	tierWorker := worker.NewTierMoverWorker(tierEngine, db.GetDB(), log, cfg.Tiering.Interval, cfg.Tiering.Enabled)
	tierWorker.Start()

	// Blob deletion sweeper: removes the bytes of deleted blobs and retired
	// tier copies after the grace period and once no running backup can still
	// need them. Every replica may run one (no global lock).
	blobSweeper := worker.NewBlobDeletionSweeper(db, tieredStore, log, cfg.BlobDeletion.SweepInterval, cfg.BlobDeletion.SweepBatch).WithMetrics(reg)
	blobSweeper.Start()

	// stopWorkers stops every background worker exactly once. It runs in the
	// shutdown sequence (after HTTP stops, before the audit logger flushes) and
	// also via defer so early-return paths never leak goroutines.
	var (
		stopWorkersOnce sync.Once
		extraStops      []func()
	)

	stopWorkers := func() {
		stopWorkersOnce.Do(func() {
			for _, stop := range []func(){
				webhookDispatcher.Stop, cleanupWorker.Stop, presignedUploadWorker.Stop, dockerGCWorker.Stop, dockerUploadSweeper.Stop, trashPurgeWorker.Stop, authPurgeWorker.Stop, proxyCacheWorker.Stop,
				tierWorker.Stop, blobSweeper.Stop, tierRegistry.Stop, tierAccess.Stop,
			} {
				stop()
			}

			// The metadata workers drain pending regenerations (bounded wait each),
			// so stop them in parallel to keep shutdown within one drain window.
			var drain sync.WaitGroup

			for _, stop := range []func(){npmWorker.Stop, mavenWorker.Stop, debWorker.Stop, rpmWorker.Stop} {
				drain.Go(stop)
			}

			drain.Wait()

			for _, stop := range extraStops {
				stop()
			}
		})
	}
	defer stopWorkers()

	// Initialize AI analysis worker. The effective settings (env/YAML layered
	// under the admin-saved ones) are applied at startup and again by the admin
	// API after every change, so no restart is needed.
	envAI := ai.Settings{
		Enabled:     cfg.AI.Enabled,
		AutoAnalyze: cfg.AI.AutoAnalyze,
		Config: ai.Config{
			Provider:     cfg.AI.Provider,
			BaseURL:      cfg.AI.BaseURL,
			APIKey:       cfg.AI.APIKey,
			Model:        cfg.AI.Model,
			MaxTokens:    cfg.AI.MaxTokens,
			TimeoutSecs:  cfg.AI.TimeoutSecs,
			SystemPrompt: cfg.AI.SystemPrompt,
		},
	}

	aiSettings := ai.LoadSettings(context.Background(), db, envAI)
	aiWorker := worker.NewAIAnalysisWorker(db, nil, log).WithBlobStore(blobStore)
	aiWorker.Configure(aiSettings)

	if aiSettings.Enabled {
		log.Infow("AI analysis enabled", "provider", aiSettings.Config.Provider, "model", aiSettings.Config.Model, "auto_analyze", aiSettings.AutoAnalyze)
	} else {
		log.Infow("AI analysis disabled")
	}

	// Uploads of every format announce themselves through the publisher; the
	// wrapper feeds them to the AI worker when auto-analysis is on.
	uploadPublisher := worker.NewAIAutoAnalyzePublisher(webhookDispatcher, aiWorker)

	aiWorker.Start()

	extraStops = append(extraStops, aiWorker.Stop)

	// Initialize MCP server
	var mcpServer *aimcp.Server
	if cfg.MCP.Enabled {
		mcpServer = aimcp.NewServer(db, aiWorker, log, aimcp.Options{Version: version, BaseURL: cfg.BaseURL})
		log.Infow("MCP server enabled", "path", cfg.MCP.HTTPPath)
	}

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := api.NewHTTPServer(addr, log)
	server.SetTimeouts(
		time.Duration(cfg.Server.ReadTimeout)*time.Second,
		time.Duration(cfg.Server.WriteTimeout)*time.Second,
		time.Duration(cfg.Server.ReadHeaderTimeout)*time.Second,
	)

	if cfg.Server.TLSEnabled {
		server.EnableTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
	}

	server.AddMiddleware(middleware.MaxBodyMiddleware(cfg.Server.MaxBodyBytes))
	server.AddMiddleware(middleware.SecurityHeadersMiddleware(cfg.Server.TLSEnabled || strings.HasPrefix(strings.ToLower(cfg.BaseURL), "https://")))
	server.AddMiddleware(middleware.RecoveryMiddleware(log))
	server.AddMiddleware(middleware.RequestIDMiddleware)
	server.AddMiddleware(middleware.MetricsMiddleware(reg))
	// OpenTelemetry server spans (no-op while telemetry is disabled); before
	// TracingMiddleware and LoggingMiddleware, which read the span context.
	server.AddMiddleware(telemetry.HTTPMiddleware())
	server.AddMiddleware(middleware.TracingMiddleware)
	server.AddMiddleware(middleware.LoggingMiddleware(log))
	server.AddMiddleware(middleware.CORSMiddleware(cfg.Server.CORSOrigins))

	trustedProxies := middleware.NewTrustedProxies(cfg.Server.TrustedProxies)

	// Add IP allowlist if configured
	if len(cfg.Server.IPAllowlist) > 0 {
		server.AddMiddleware(middleware.IPAllowlistMiddleware(cfg.Server.IPAllowlist, trustedProxies))
		log.Infow("IP allowlist middleware enabled", "cidrs", cfg.Server.IPAllowlist)
	}

	// Add rate limiting if configured
	if cfg.Server.RateLimitRPS > 0 {
		server.AddMiddleware(middleware.RateLimitMiddleware(cfg.Server.RateLimitRPS, time.Second, trustedProxies))
		log.Infow("Rate limiting middleware enabled", "rps", cfg.Server.RateLimitRPS)
	}

	// Login, logout and SSO callbacks are on the bare router, outside the
	// authenticated chain's AuditMiddleware, so audit them explicitly.
	server.AddMiddleware(middleware.AuthAuditMiddleware(auditLogger, db, trustedProxies))

	// One SPA handler for both the RBAC page-load bypass and the router
	// fallback: building it hashes and precompresses every embedded asset.
	spaHandler := webhandler.Handler()

	evaluator := auth.NewEvaluator(db, log, cfg.Auth.AllowAnonymousRead)
	trash.SetEvaluator(evaluator)
	projectSettings := auth.NewProjectSettings(db, cfg.Projects.SelfCreate)
	rbacCfg := middleware.RBACConfig{
		ProjectSettings:     projectSettings,
		LegacyAPIKey:        cfg.Server.APIKey,
		LegacyAPIKeyEnabled: cfg.Auth.LegacyAPIKeyEnabled,
		AllowAnonymousRead:  cfg.Auth.AllowAnonymousRead,
		MCPPath:             cfg.MCP.HTTPPath,
		SPAHandler:          spaHandler,
		BaseURL:             cfg.BaseURL,
	}
	if mcpServer != nil {
		mcpServer.SetAuthorizer(func(ctx context.Context, repoName, action string) bool {
			principal := middleware.PrincipalFromContext(ctx)
			if principal == nil {
				return false
			}

			ok, err := evaluator.Evaluate(ctx, *principal, repoName, "", action)

			return err == nil && ok
		})
		mcpServer.SetReadFilter(func(ctx context.Context) (func(*models.Repository) (bool, error), error) {
			principal := middleware.PrincipalFromContext(ctx)
			if principal == nil {
				return func(*models.Repository) (bool, error) { return false, nil }, nil
			}

			return evaluator.RepoReadFilter(ctx, *principal)
		})
		mcpServer.SetSearcher(search.NewExecutor(db.GetDB()))
	}

	rbacMiddleware := middleware.RBACMiddleware(rbacCfg, db, evaluator, log)
	legacyAuthMode := "disabled"
	if cfg.Auth.LegacyAPIKeyEnabled {
		legacyAuthMode = "enabled"
	}

	if cfg.Server.APIKey != "" && !cfg.Auth.LegacyAPIKeyEnabled {
		log.Warnw("API_KEY is set but ignored: the legacy shared key is disabled by default; " +
			"set LEGACY_API_KEY_ENABLED=true to keep using it, or migrate to API tokens")
	}

	log.Infow("RBAC middleware enabled",
		"allow_anonymous_read", cfg.Auth.AllowAnonymousRead,
		"legacy_auth", legacyAuthMode,
	)

	router := server.GetRouter()

	router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		type componentStatus struct {
			Status  string `json:"status"`
			Message string `json:"message,omitempty"`
		}

		type healthResponse struct {
			Status     string                     `json:"status"`
			Version    string                     `json:"version"`
			InstanceID string                     `json:"instance_id,omitempty"`
			Components map[string]componentStatus `json:"components"`
		}

		components := make(map[string]componentStatus)

		// Check database
		dbStatus := componentStatus{Status: "ok"}
		if err := db.Ping(r.Context()); err != nil {
			dbStatus.Status = "error"
			log.Errorw("health: database ping failed", "error", err)
			dbStatus.Message = "unavailable"
		}

		components["database"] = dbStatus

		// Check storage
		storageStatus := componentStatus{Status: "ok"}

		if s3store, ok := rawBlobStore.(*storage.S3BlobStore); ok {
			if err := s3store.HealthCheck(r.Context()); err != nil {
				storageStatus.Status = "error"
				log.Errorw("health: storage check failed", "error", err)
				storageStatus.Message = "unavailable"
			}
		}

		components["storage"] = storageStatus

		// Determine overall status
		overallStatus := "healthy"

		for _, comp := range components {
			if comp.Status != "ok" {
				overallStatus = "degraded"
				break
			}
		}

		resp := healthResponse{
			Status:  overallStatus,
			Version: version,
			// Only an operator-chosen name: the hostname and uptime are not
			// disclosed on this unauthenticated endpoint.
			InstanceID: cfg.Server.InstanceID,
			Components: components,
		}

		w.Header().Set("Content-Type", "application/json")

		statusCode := http.StatusOK
		if overallStatus == "degraded" {
			statusCode = http.StatusOK // Health != readiness, return 200 even if degraded
		}

		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(resp)
	})

	metricsToken := os.Getenv("METRICS_AUTH_TOKEN")
	metricsAnonymous := os.Getenv("METRICS_ALLOW_ANONYMOUS") == "true"

	if metricsToken == "" && !metricsAnonymous {
		log.Warnw("/metrics is disabled: set METRICS_AUTH_TOKEN (or METRICS_ALLOW_ANONYMOUS=true to expose it without authentication)")
	}

	metricsHandler := metricsAuthMiddleware(metricsToken, metricsAnonymous)(promhttp.Handler())
	router.Method("GET", "/metrics", metricsHandler)

	router.Get("/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not ready","reason":"database unavailable"}`))

			return
		}

		if s3store, ok := rawBlobStore.(*storage.S3BlobStore); ok {
			if err := s3store.HealthCheck(r.Context()); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"not ready","reason":"storage unavailable"}`))

				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	authHandler := api.NewAuthHandler(db, log, evaluator, trustedProxies).WithBaseURL(cfg.BaseURL)
	authHandler.RegisterPublic(router)

	hmacKey := []byte(cfg.Auth.HMACKey)
	api.NewOIDCHandler(db, log, evaluator, hmacKey, cfg.BaseURL).WithTrustedProxies(trustedProxies).RegisterPublic(router)
	api.NewSAMLHandler(db, log, evaluator, hmacKey, cfg.BaseURL, cfg).WithTrustedProxies(trustedProxies).RegisterPublic(router)

	sysInfoHandler := api.NewSystemInfoHandler(version, commit, buildTime, db, blobStore, cfg.Storage.Type)
	sysInfoHandler.RegisterPublic(router)

	// Public SSO provider list (no auth required — used by login page)
	api.NewAdminSSOHandler(db, log, evaluator, cfg.BaseURL, cfg).RegisterPublic(router)

	// OpenAPI documentation
	openapiHandler := api.NewOpenAPIHandler()
	openapiHandler.RegisterRoutes(router)

	authedAPI := chi.NewRouter()
	authedAPI.Use(rbacMiddleware)
	// Marks client downloads so the tiered store records blob access.
	authedAPI.Use(middleware.TrackBlobAccess)
	// Admin-only origin timestamps for migrations (X-OA-Import-Created-At).
	authedAPI.Use(importmeta.Middleware(evaluator))
	authedAPI.Use(middleware.CSRFMiddleware(cfg.BaseURL))
	authedAPI.Use(middleware.AuditMiddleware(auditLogger, trustedProxies))

	authHandler.RegisterAuthed(authedAPI)

	ssoHandler := api.NewAdminSSOHandler(db, log, evaluator, cfg.BaseURL, cfg)
	ssoHandler.RegisterAuthed(authedAPI)

	api.NewAdminUsersHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminGroupsHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminPermissionsHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminTokensHandler(db, log, evaluator).Register(authedAPI)
	api.NewSelfTokensHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminAuditHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminStorageHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminTieringHandler(db, tierRegistry, tierEngine, tierWorker, log, evaluator).Register(authedAPI)
	api.NewAdminGPGHandler(db, log, evaluator).Register(authedAPI)
	sysInfoHandler.RegisterAuthed(authedAPI)

	if cfg.Server.PprofEnabled {
		registerPprof(authedAPI)
		log.Warnw("pprof enabled at /api/v1/admin/pprof/ (admin only); disable outside diagnostics")
	}

	api.NewAdminAIHandler(db, log, evaluator).WithRuntime(aiWorker, envAI).Register(authedAPI)

	api.NewProjectsHandler(db, log, evaluator, projectSettings).RegisterRoutes(authedAPI)

	searchHandler := search.NewHandler(db.GetDB(), evaluator, log, search.WithAuthCache(db))
	authedAPI.Get("/api/v1/search", searchHandler.ServeHTTP)

	rpmUpload := api.NewRPMUploadHandler(log, db, blobStore, reg)
	rpmUpload.SetDebWorker(debWorker)
	rpmUpload.SetRPMWorker(rpmWorker)
	rpmUpload.Register(authedAPI)

	debUpload := api.NewDebUploadHandler(log, db, blobStore, reg)
	debUpload.SetDebWorker(debWorker)
	debUpload.Register(authedAPI)

	presignedUploads := api.NewPresignedUploadHandler(log, db, blobStore, evaluator, cfg.Storage.PresignUploadTTL, cfg.Storage.PresignMaxPendingUploads, reg)
	presignedUploads.Register(authedAPI)

	rpmHTTP := formatrpm.NewHTTPHandler(log, db, servedStore)
	debHTTP := formatdeb.NewHTTPHandler(log, db, servedStore)
	helmHTTP := formathelm.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	pypiHTTP := formatpypi.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	nugetHTTP := formatnuget.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	npmHTTP := formatnpm.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	cargoHTTP := formatcargo.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	rubygemsHTTP := formatrubygems.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	rawHTTP := formatraw.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	condaHTTP := formatconda.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	conanHTTP := formatconan.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	ivyHTTP := formativy.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	sbtHTTP := formatsbt.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	mavenHTTP := formatmaven.NewHTTPHandler(log, db, servedStore)

	// All repo-scoped format handlers, including the npm publish/unpublish
	// catch-all, share the /repos/{name} subrouter. A sibling /repos/{x}/*
	// route outside it is shadowed by the mount and 404s.
	npmUpload := api.NewNPMUploadHandler(log, db, blobStore, cfg.BaseURL, reg)
	npmUpload.SetNPMWorker(npmWorker)
	gomodHTTP := formatgomod.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)

	// Proxy and group repositories are served by the dispatcher in front of the
	// per-format routes; hosted repositories fall through to them. Groups read
	// their hosted members through in-memory routers of the same handlers.
	mavenHosted, rawHosted, npmHosted, dockerHosted, nugetHosted := chi.NewRouter(), chi.NewRouter(), chi.NewRouter(), chi.NewRouter(), chi.NewRouter()
	mavenHTTP.RegisterRepoRoutes(mavenHosted)
	rawHTTP.RegisterRepoRoutes(rawHosted)
	npmHTTP.RegisterRepoRoutes(npmHosted)
	nugetHTTP.RegisterRepoRoutes(nugetHosted)

	cargoHosted := chi.NewRouter()
	cargoHTTP.RegisterRepoRoutes(cargoHosted)

	pypiHosted := chi.NewRouter()
	pypiHTTP.RegisterRepoRoutes(pypiHosted)

	helmHosted := chi.NewRouter()
	helmHTTP.RegisterRepoRoutes(helmHosted)

	// Read-only view of the GOPROXY handler: group members are only ever read.
	goHosted := chi.NewRouter()
	gomodHTTP.RegisterRepoRoutes(goHosted)

	dockerHTTP := formatdocker.NewHTTPHandler(log, db, servedStore, cfg.BaseURL)
	dockerHTTP.SetUploadDir(dockerUploadDir(cfg))
	dockerHTTP.SetAuthorizer(evaluator)
	dockerHTTP.RegisterRoutes(dockerHosted)

	repoDispatch := repodispatch.New(log, db, proxy.NewService(log, db, servedStore).WithDockerDigestTTL(cfg.Workers.ProxyDockerDigestTTL), evaluator, map[string]http.Handler{
		"maven":  mavenHosted,
		"pypi":   pypiHosted,
		"raw":    rawHosted,
		"npm":    npmHosted,
		"cargo":  cargoHosted,
		"go":     goHosted,
		"nuget":  nugetHosted,
		"docker": dockerHosted,
		"helm":   helmHosted,
	}).WithBaseURL(cfg.BaseURL)

	authedAPI.Route("/repos/{name}", func(r chi.Router) {
		r.Use(repoDispatch.Middleware)
		r.Use(npmHTTP.ShadowGuard)
		rpmHTTP.RegisterRepoRoutes(r)
		debHTTP.RegisterRepoRoutes(r)
		helmHTTP.RegisterRepoRoutes(r)
		pypiHTTP.RegisterRepoRoutes(r)
		nugetHTTP.RegisterRepoRoutes(r)
		npmHTTP.RegisterRepoRoutes(r)
		cargoHTTP.RegisterRepoRoutes(r)
		rubygemsHTTP.RegisterRepoRoutes(r)
		condaHTTP.RegisterRepoRoutes(r)
		conanHTTP.RegisterRepoRoutes(r)
		gomodHTTP.RegisterRepoRoutes(r)
		npmUpload.RegisterRepoRoutes(r)
		// npm publish and GOPROXY upload both need PUT "/*"; dispatch by tail.
		r.Put("/*", func(w http.ResponseWriter, req *http.Request) {
			if formatgomod.IsUploadPath(chi.URLParam(req, "*")) {
				gomodHTTP.HandlePut(w, req)
				return
			}
			npmUpload.HandlePublish(w, req)
		})
	})
	log.Infow("Repository HTTP handlers registered", "prefix", "/repos/{name}")

	// Wildcard handlers isolated to avoid shadowing specific routes.
	authedAPI.Route("/repos/raw/{name}", func(r chi.Router) {
		r.Use(repoDispatch.Middleware)
		rawHTTP.RegisterRepoRoutes(r)
	})
	log.Infow("Raw handler registered", "prefix", "/repos/raw/{name}")

	// Maven, SBT, Ivy use wildcard paths that shadow other handlers — isolate them.
	authedAPI.Route("/repos/maven/{name}", func(r chi.Router) {
		r.Use(repoDispatch.Middleware)
		mavenHTTP.RegisterRepoRoutes(r)
	})
	authedAPI.Route("/repos/sbt/{name}", func(r chi.Router) { sbtHTTP.RegisterRepoRoutes(r) })
	authedAPI.Route("/repos/ivy/{name}", func(r chi.Router) { ivyHTTP.RegisterRepoRoutes(r) })
	log.Infow("Maven/SBT/Ivy handlers registered (isolated prefixes)")
	// The registry (/v2/...) sits behind the dispatcher too: docker proxy and
	// group repositories are served there, hosted ones by dockerHTTP.
	authedAPI.Group(func(r chi.Router) {
		r.Use(repoDispatch.OCIMiddleware)
		dockerHTTP.RegisterRoutes(r)
	})

	repoHandler := api.NewRepositoriesHandler(db, log, evaluator).WithBlobStore(blobStore).WithPublisher(webhookDispatcher)
	repoHandler.RegisterAuthed(authedAPI)

	cleanupHandler := api.NewCleanupHandler(db, log, evaluator, cleanupWorker)
	cleanupHandler.RegisterAuthed(authedAPI)

	api.NewDockerGCHandler(db, log, evaluator, dockerGCWorker).RegisterAuthed(authedAPI)

	api.NewTrashHandler(db, log, evaluator).
		WithBlobStore(blobStore).
		WithPublisher(webhookDispatcher).
		WithRegenerator(api.NewWorkerRegenerator(debWorker, rpmWorker, mavenWorker, npmWorker)).
		RegisterAuthed(authedAPI)
	// The retention purge worker is started above; the manual purge lives in
	// TrashHandler.

	api.NewBundlesHandler(db, log, evaluator).RegisterAuthed(authedAPI)

	sbomGen := sbom.NewGenerator(db, log).WithBlobStore(blobStore).WithToolVersion(version).WithBaseURL(cfg.BaseURL)
	sbomHandler := api.NewSBOMHandler(db, log, evaluator, sbomGen, blobStore)
	sbomHandler.RegisterAuthed(authedAPI)
	api.NewVEXHandler(db, log, evaluator, sbomGen).RegisterAuthed(authedAPI)

	webhooksHandler := api.NewWebhooksHandler(db, log, evaluator, webhookDispatcher)
	webhooksHandler.RegisterAuthed(authedAPI)

	backupEngine := backup.NewEngine(db, blobStore, log, cfg.Backup.BackupDir)
	backupEngine.SetLeaseTTL(cfg.Backup.LeaseTTL)
	backupEngine.SetRegenerator(api.NewWorkerRegenerator(debWorker, rpmWorker, mavenWorker, npmWorker))
	backupHandler := api.NewBackupHandler(db, log, evaluator, backupEngine)
	backupHandler.RegisterAuthed(authedAPI)

	licenseDetector := license.NewDetector(log)
	licenseHandler := api.NewLicenseHandler(db, log, evaluator, licenseDetector, blobStore)
	licenseHandler.RegisterRoutes(authedAPI)

	secretScanner := secrets.NewScanner(log)
	secretsHandler := api.NewSecretsHandler(db, log, secretScanner)
	secretsHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	// Trivy and grype are external binaries; the registry downloads and
	// refreshes their databases in the background (docs/security/scanning.md).
	scannerRegistry := vuln.NewRegistry(log, cfg.Scanners.VulnOptions()).WithMetrics(reg)
	scannerRegistry.LogStatus()
	scannerRegistry.Start(context.Background())

	extraStops = append(extraStops, scannerRegistry.Stop)

	vulnHandler := api.NewVulnHandler(db, log, evaluator, scannerRegistry)
	vulnHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")
	vulnHandler.RegisterOverviewRoutes(authedAPI)

	quotasHandler := api.NewQuotasHandler(log, db, evaluator)
	quotasHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	propertiesHandler := api.NewPropertiesHandler(log, db, blobStore)
	propertiesHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	aiHandler := api.NewAIHandler(db, log, evaluator, aiWorker)
	aiHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")
	deltaHandler := api.NewDeltaHandler(log, db, blobStore)
	deltaHandler.Register(authedAPI)

	mavenUpload := api.NewMavenUploadHandler(log, db, blobStore, reg)
	mavenUpload.SetMavenWorker(mavenWorker)
	mavenUpload.Register(authedAPI)

	// Every handler that stores or removes artifacts announces artifact.uploaded /
	// artifact.deleted through the webhook dispatcher. The download-only rpm and
	// deb HTTP handlers do not publish events.
	for _, h := range []interface{ SetPublisher(webhook.Publisher) }{
		rpmUpload, debUpload, presignedUploads, npmUpload, mavenUpload,
		helmHTTP, pypiHTTP, nugetHTTP, npmHTTP, cargoHTTP, rubygemsHTTP, rawHTTP,
		condaHTTP, conanHTTP, ivyHTTP, sbtHTTP, mavenHTTP, dockerHTTP, gomodHTTP,
	} {
		h.SetPublisher(uploadPublisher)
	}

	// Mount the MCP server if enabled (HTTP is the only transport). Mounted under authedAPI
	// (not the bare router) so it inherits the same RBAC/CSRF/audit chain as
	// the rest of the API — MCP tools must require auth like any other route.
	if mcpServer != nil {
		authedAPI.Mount(cfg.MCP.HTTPPath, mcpServer.HTTPHandler())
		log.Infow("MCP HTTP endpoint registered", "path", cfg.MCP.HTTPPath)
	}

	// SPA fallback on the authed sub-router: paths not matched by any API or
	// repo route serve the Vue SPA (client-side routing). RBAC is bypassed for
	// these paths by the early-exit in RBACMiddleware below.
	authedAPI.NotFound(spaHandler.ServeHTTP)
	router.Mount("/", authedAPI)

	// Start server
	if err := server.Start(context.Background()); err != nil {
		return fail("failed to start server", err)
	}

	log.Infow("Server started", "address", addr)

	// Wait for an interrupt, or for the accept loop to die underneath us. The
	// latter still runs the ordered shutdown below, then exits non-zero.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	var serveErr error

	select {
	case <-sigChan:
		log.Infow("Shutdown signal received, gracefully stopping")
	case serveErr = <-server.ServeErrors():
		log.Errorw("HTTP server stopped unexpectedly, shutting down", "error", serveErr)
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Server.ShutdownTimeout)*time.Second)
	defer cancel()

	// Ordered shutdown: stop accepting HTTP, stop workers (they may still emit
	// audit events), then flush audit; the DB closes last via defer. Audit is
	// flushed even if a prior step failed.
	if err := server.Stop(ctx); err != nil {
		log.Errorw("Server shutdown error", "error", err)
	}

	stopWorkers()

	auditCtx, auditCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer auditCancel()

	if err := auditLogger.Shutdown(auditCtx); err != nil {
		log.Warnw("Audit logger shutdown incomplete", "error", err)
	}

	if serveErr != nil {
		return reportedError{serveErr}
	}

	log.Infow("Server stopped successfully")

	return nil
}

// runGomodRepair is the -repair-gomod subcommand: one repair pass in the
// foreground, with a summary on stdout and a non-zero exit when anything
// could not be relinked.
func runGomodRepair(ctx context.Context, db *database.PostgresDB, store storage.BlobStore, log *logger.Logger) error {
	res, err := formatgomod.RepairZipBlobs(ctx, db, store, log)
	if err != nil {
		return fmt.Errorf("gomod repair: %w", err)
	}

	if res.LockBusy {
		return errGomodRepairBusy
	}

	fmt.Printf("gomod repair: %d affected, %d repaired, %d unrepairable, %d invalid blob row(s) removed\n",
		res.Affected, res.Repaired, len(res.Unrepairable), res.BlobRowsDeleted)

	for _, i := range res.Unrepairable {
		fmt.Printf("  unrepairable: %s\n", i)
	}

	if res.ListUnsupported {
		fmt.Println("  the storage backend cannot list objects: only the shared blob's own zip could be recovered")
	}

	if len(res.Unrepairable) > 0 {
		return reportedError{fmt.Errorf("%w: %d", errGomodRepairUnrepaired, len(res.Unrepairable))}
	}

	return nil
}

// metricsAuthMiddleware fails closed: with no token configured the endpoint
// answers 401 unless anonymous access was explicitly enabled.
// dockerUploadDir is where docker blob uploads are staged.
func dockerUploadDir(cfg *config.Config) string {
	if cfg.Workers.DockerUploadDir != "" {
		return cfg.Workers.DockerUploadDir
	}

	return formatdocker.DefaultUploadDir
}

func metricsAuthMiddleware(token string, allowAnonymous bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				if allowAnonymous {
					next.ServeHTTP(w, r)
					return
				}

				w.WriteHeader(http.StatusUnauthorized)

				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") ||
				subtle.ConstantTimeCompare([]byte(authHeader[len("Bearer "):]), []byte(token)) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
