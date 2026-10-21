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
	"strconv"
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
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repodispatch"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/secrets"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
	webhandler "github.com/openartifact/openartifact/web"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	version = "dev"
	//nolint:gochecknoglobals // build-time injection via -ldflags
	commit = "unknown"
	//nolint:gochecknoglobals // build-time injection via -ldflags
	buildTime = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if _, reported := errors.AsType[reportedError](err); !reported {
			fmt.Fprintf(os.Stderr, "openartifact-server: %v\n", err)
		}

		os.Exit(1)
	}
}

var errVerifyUsage = errors.New("usage: openartifact-server verify <config.yaml>")

// reportedError marks a failure the operator has already seen (structured log
// line or flag usage), so main exits non-zero without printing it again.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// run is the whole server lifetime: it returns nil after a clean shutdown (or
// for -migrate, verify and -h) and an error for every startup or runtime
// failure, so the process never exits 0 while the service is not running.
func run(args []string) error {
	startTime := time.Now()

	flags := flag.NewFlagSet("openartifact-server", flag.ContinueOnError)
	migrateFlag := flags.Bool("migrate", false, "run migrations and exit")
	configFile := flags.String("config", os.Getenv("OPENARTIFACT_CONFIG"), "path to YAML config file")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		// The FlagSet already printed the problem and the usage text.
		return reportedError{err}
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

	// Initialize database
	db, err := database.NewPostgresDB(cfg.Database, log)
	if err != nil {
		return fail("failed to connect to database", err)
	}
	defer func() { _ = db.Close() }()

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

	blobStore := storage.NewMetricsBlobStore(rawBlobStore, reg)

	log.Infow("Storage initialized", "type", cfg.Storage.Type, "path", cfg.Storage.Path)

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
	authPurgeWorker.Start()

	// Initialize cleanup worker
	cleanupWorker := worker.NewCleanupWorker(db, blobStore, log)

	cleanupWorker.Start()

	// Docker registry GC: untagged manifests past the grace period and the
	// blobs only they referenced. Also runs on demand from the admin API.
	dockerGCWorker := worker.NewDockerGCWorker(db, blobStore, log, cfg.Workers.DockerGCInterval, cfg.Workers.DockerGCGracePeriod)

	dockerGCWorker.Start()

	// Initialize webhook dispatcher
	webhookDispatcher := webhook.NewDispatcher(db, log)

	webhookDispatcher.Start()

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
				webhookDispatcher.Stop, cleanupWorker.Stop, dockerGCWorker.Stop, authPurgeWorker.Stop, proxyCacheWorker.Stop,
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

	// Initialize AI analysis worker
	var (
		aiWorker *worker.AIAnalysisWorker
		aiClient *ai.Client
	)

	// Try to load AI config from DB first, fall back to env vars
	dbEnabled, _ := db.GetSystemSetting(context.Background(), "ai.enabled")
	aiEnabled := false

	if dbEnabled != "" {
		// DB config exists, try to parse it
		if b, err := strconv.ParseBool(dbEnabled); err == nil {
			aiEnabled = b
		}
	} else {
		// Fall back to env config
		aiEnabled = cfg.AI.Enabled
	}

	//nolint:nestif // AI config loading from DB requires nested checks
	if aiEnabled {
		// Load config from DB if available, otherwise use env
		var aiCfg ai.Config

		if dbEnabled != "" {
			// Load from DB
			aiCfg = ai.Config{}
			if provider, _ := db.GetSystemSetting(context.Background(), "ai.provider"); provider != "" {
				aiCfg.Provider = provider
			} else {
				aiCfg.Provider = cfg.AI.Provider
			}

			if baseURL, _ := db.GetSystemSetting(context.Background(), "ai.base_url"); baseURL != "" {
				aiCfg.BaseURL = baseURL
			} else {
				aiCfg.BaseURL = cfg.AI.BaseURL
			}

			if apiKey, _ := db.GetSystemSetting(context.Background(), "ai.api_key"); apiKey != "" {
				aiCfg.APIKey = apiKey
			} else {
				aiCfg.APIKey = cfg.AI.APIKey
			}

			if model, _ := db.GetSystemSetting(context.Background(), "ai.model"); model != "" {
				aiCfg.Model = model
			} else {
				aiCfg.Model = cfg.AI.Model
			}

			if maxTokensStr, _ := db.GetSystemSetting(context.Background(), "ai.max_tokens"); maxTokensStr != "" {
				if n, err := strconv.Atoi(maxTokensStr); err == nil {
					aiCfg.MaxTokens = n
				} else {
					aiCfg.MaxTokens = cfg.AI.MaxTokens
				}
			} else {
				aiCfg.MaxTokens = cfg.AI.MaxTokens
			}

			if timeoutSecsStr, _ := db.GetSystemSetting(context.Background(), "ai.timeout_secs"); timeoutSecsStr != "" {
				if n, err := strconv.Atoi(timeoutSecsStr); err == nil {
					aiCfg.TimeoutSecs = n
				} else {
					aiCfg.TimeoutSecs = cfg.AI.TimeoutSecs
				}
			} else {
				aiCfg.TimeoutSecs = cfg.AI.TimeoutSecs
			}

			if systemPrompt, _ := db.GetSystemSetting(context.Background(), "ai.system_prompt"); systemPrompt != "" {
				aiCfg.SystemPrompt = systemPrompt
			} else {
				aiCfg.SystemPrompt = cfg.AI.SystemPrompt
			}
		} else {
			// Use env config
			aiCfg = ai.Config{
				Provider:     cfg.AI.Provider,
				BaseURL:      cfg.AI.BaseURL,
				APIKey:       cfg.AI.APIKey,
				Model:        cfg.AI.Model,
				MaxTokens:    cfg.AI.MaxTokens,
				TimeoutSecs:  cfg.AI.TimeoutSecs,
				SystemPrompt: cfg.AI.SystemPrompt,
			}
		}

		aiClient = ai.NewClient(aiCfg)
		log.Infow("AI analysis enabled", "provider", aiCfg.Provider, "model", aiCfg.Model)
	} else {
		log.Infow("AI analysis disabled")
	}

	aiWorker = worker.NewAIAnalysisWorker(db, aiClient, log)

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

	server.AddMiddleware(middleware.SecurityHeadersMiddleware(cfg.Server.TLSEnabled))
	server.AddMiddleware(middleware.RecoveryMiddleware(log))
	server.AddMiddleware(middleware.RequestIDMiddleware)
	server.AddMiddleware(middleware.TracingMiddleware)
	server.AddMiddleware(middleware.MetricsMiddleware(reg))
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
	rbacCfg := middleware.RBACConfig{
		LegacyAPIKey:        cfg.Server.APIKey,
		LegacyAPIKeyEnabled: cfg.Auth.LegacyAPIKeyEnabled,
		AllowAnonymousRead:  cfg.Auth.AllowAnonymousRead,
		MCPPath:             cfg.MCP.HTTPPath,
		SPAHandler:          spaHandler,
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
	}

	rbacMiddleware := middleware.RBACMiddleware(rbacCfg, db, evaluator, log)
	log.Infow("RBAC middleware enabled",
		"allow_anonymous_read", cfg.Auth.AllowAnonymousRead,
		"legacy_api_key_enabled", cfg.Auth.LegacyAPIKeyEnabled,
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
			InstanceID string                     `json:"instance_id"`
			Uptime     string                     `json:"uptime"`
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

		// Get instance ID
		instanceID := cfg.Server.InstanceID
		if instanceID == "" {
			if hostname, err := os.Hostname(); err == nil {
				instanceID = hostname
			}
		}

		resp := healthResponse{
			Status:     overallStatus,
			Version:    version,
			InstanceID: instanceID,
			Uptime:     time.Since(startTime).String(),
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

	authHandler := api.NewAuthHandler(db, log, evaluator, trustedProxies)
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
	// Admin-only origin timestamps for migrations (X-OA-Import-Created-At).
	authedAPI.Use(importmeta.Middleware(evaluator))
	authedAPI.Use(middleware.CSRFMiddleware)
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
	api.NewAdminGPGHandler(db, log, evaluator).Register(authedAPI)
	sysInfoHandler.RegisterAuthed(authedAPI)

	if cfg.Server.PprofEnabled {
		registerPprof(authedAPI)
		log.Warnw("pprof enabled at /api/v1/admin/pprof/ (admin only); disable outside diagnostics")
	}

	api.NewAdminAIHandler(db, log, evaluator).Register(authedAPI)

	orgsHandler := api.NewOrgsHandler(db, log)
	orgsHandler.RegisterRoutes(authedAPI)

	searchHandler := search.NewHandler(db.GetDB(), evaluator, log)
	authedAPI.Get("/api/v1/search", searchHandler.ServeHTTP)

	rpmUpload := api.NewRPMUploadHandler(log, db, blobStore, reg)
	rpmUpload.SetDebWorker(debWorker)
	rpmUpload.SetRPMWorker(rpmWorker)
	rpmUpload.Register(authedAPI)

	debUpload := api.NewDebUploadHandler(log, db, blobStore, reg)
	debUpload.SetDebWorker(debWorker)
	debUpload.Register(authedAPI)

	rpmHTTP := formatrpm.NewHTTPHandler(log, db, blobStore)
	debHTTP := formatdeb.NewHTTPHandler(log, db, blobStore)
	helmHTTP := formathelm.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	pypiHTTP := formatpypi.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	nugetHTTP := formatnuget.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	npmHTTP := formatnpm.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	cargoHTTP := formatcargo.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	rubygemsHTTP := formatrubygems.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	rawHTTP := formatraw.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	condaHTTP := formatconda.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	conanHTTP := formatconan.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	ivyHTTP := formativy.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	sbtHTTP := formatsbt.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	mavenHTTP := formatmaven.NewHTTPHandler(log, db, blobStore)

	// All repo-scoped format handlers, including the npm publish/unpublish
	// catch-all, share the /repos/{name} subrouter. A sibling /repos/{x}/*
	// route outside it is shadowed by the mount and 404s.
	npmUpload := api.NewNPMUploadHandler(log, db, blobStore, cfg.BaseURL, reg)
	npmUpload.SetNPMWorker(npmWorker)
	gomodHTTP := formatgomod.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)

	// Proxy and group repositories are served by the dispatcher in front of the
	// per-format routes; hosted repositories fall through to them. Groups read
	// their hosted members through in-memory routers of the same handlers.
	mavenHosted, rawHosted := chi.NewRouter(), chi.NewRouter()
	mavenHTTP.RegisterRepoRoutes(mavenHosted)
	rawHTTP.RegisterRepoRoutes(rawHosted)

	repoDispatch := repodispatch.New(log, db, proxy.NewService(log, db, blobStore), evaluator, map[string]http.Handler{
		"maven": mavenHosted,
		"raw":   rawHosted,
	})

	authedAPI.Route("/repos/{name}", func(r chi.Router) {
		r.Use(repoDispatch.Middleware)
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
	dockerHTTP := formatdocker.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	dockerHTTP.SetAuthorizer(evaluator)
	dockerHTTP.RegisterRoutes(authedAPI)

	repoHandler := api.NewRepositoriesHandler(db, log, evaluator).WithBlobStore(blobStore).WithPublisher(webhookDispatcher)
	repoHandler.RegisterAuthed(authedAPI)

	cleanupHandler := api.NewCleanupHandler(db, log, evaluator, cleanupWorker)
	cleanupHandler.RegisterAuthed(authedAPI)

	api.NewDockerGCHandler(db, log, evaluator, dockerGCWorker).RegisterAuthed(authedAPI)

	api.NewBundlesHandler(db, log, evaluator).RegisterAuthed(authedAPI)

	sbomGen := sbom.NewGenerator(db, log)
	sbomHandler := api.NewSBOMHandler(db, log, evaluator, sbomGen, blobStore)
	sbomHandler.RegisterAuthed(authedAPI)

	webhooksHandler := api.NewWebhooksHandler(db, log, evaluator, webhookDispatcher)
	webhooksHandler.RegisterAuthed(authedAPI)

	backupEngine := backup.NewEngine(db, blobStore, log, cfg.Backup.BackupDir)
	backupHandler := api.NewBackupHandler(db, log, evaluator, backupEngine)
	backupHandler.RegisterAuthed(authedAPI)

	licenseDetector := license.NewDetector(log)
	licenseHandler := api.NewLicenseHandler(db, log, evaluator, licenseDetector)
	licenseHandler.RegisterRoutes(authedAPI)

	secretScanner := secrets.NewScanner(log)
	secretsHandler := api.NewSecretsHandler(db, log, secretScanner)
	secretsHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	vulnHandler := api.NewVulnHandler(db, log, evaluator)
	vulnHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	quotasHandler := api.NewQuotasHandler(log, db, evaluator)
	quotasHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	propertiesHandler := api.NewPropertiesHandler(log, db)
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
		rpmUpload, debUpload, npmUpload, mavenUpload,
		helmHTTP, pypiHTTP, nugetHTTP, npmHTTP, cargoHTTP, rubygemsHTTP, rawHTTP,
		condaHTTP, conanHTTP, ivyHTTP, sbtHTTP, mavenHTTP, dockerHTTP, gomodHTTP,
	} {
		h.SetPublisher(webhookDispatcher)
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

// metricsAuthMiddleware fails closed: with no token configured the endpoint
// answers 401 unless anonymous access was explicitly enabled.
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
