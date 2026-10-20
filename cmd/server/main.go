// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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
	"github.com/openartifact/openartifact/internal/license"
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
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
	startTime := time.Now()
	migrateFlag := flag.Bool("migrate", false, "run migrations and exit")
	configFile := flag.String("config", os.Getenv("OPENARTIFACT_CONFIG"), "path to YAML config file")

	flag.Parse()

	// Handle 'verify' subcommand: openartifact-server verify config.yaml
	if flag.NArg() > 0 && flag.Arg(0) == "verify" {
		path := *configFile
		if flag.NArg() > 1 {
			path = flag.Arg(1)
		}

		if path == "" {
			fmt.Fprintln(os.Stderr, "Usage: openartifact-server verify <config.yaml>")
			os.Exit(1)
		}

		if err := config.ValidateFile(path); err != nil {
			fmt.Fprintf(os.Stderr, "Config validation failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Config file %q is valid.\n", path)
		os.Exit(0)
	}

	migrateOnStart := os.Getenv("MIGRATE_ON_START") != "false"

	// Load configuration
	cfg, err := config.Load(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}

	if err := crypto.InitFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid %s: %v\n", crypto.EnvKey, err)
		os.Exit(1)
	}

	// Initialize logger
	log, err := logger.New(cfg.Logger.Level, cfg.Logger.Format, cfg.Logger.OutputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = log.Sync() }()

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
		log.Errorw("Failed to connect to database", "error", err)
		return
	}
	defer func() { _ = db.Close() }()

	log.Infow("Database connected",
		"host", cfg.Database.Host,
		"database", cfg.Database.Database,
	)

	if *migrateFlag {
		if err := db.Migrate(context.Background()); err != nil {
			log.Errorw("Migration failed", "error", err)
			return
		}

		log.Infow("Migrations applied successfully")

		return
	}

	if migrateOnStart {
		if err := db.Migrate(context.Background()); err != nil {
			log.Errorw("Auto-migrate failed", "error", err)
			return
		}
	} else {
		log.Warnw("MIGRATE_ON_START=false: automatic migrations skipped")
	}

	auth.ConfigureSessionDuration(time.Duration(cfg.Auth.SessionDurationHours) * time.Hour)

	if err := auth.BootstrapIfEmpty(context.Background(), db, log); err != nil {
		log.Errorw("Bootstrap failed", "error", err)
		return
	}

	reg := metrics.NewRegistry()

	auditLogger := audit.NewLogger(db, log, reg, 1000)
	audit.SetDefault(auditLogger)

	// Initialize storage
	rawBlobStore, err := storage.NewBlobStore(cfg.Storage)
	if err != nil {
		log.Errorw("Failed to initialize storage", "error", err)
		return
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
	rpmWorker := worker.NewRPMetadataWorker(db, blobStore, signer, log, reg)
	debWorker := worker.NewDebMetadataWorker(db, blobStore, signer, log)
	mavenWorker := worker.NewMavenMetadataWorker(db, blobStore, db.GetDB(), log, reg)
	npmWorker := worker.NewNPMMetadataWorker(db, log, db.GetDB(), reg)
	proxyCacheWorker := worker.NewProxyCacheWorker(db, blobStore, log)
	proxyCacheWorker.Start()

	authPurgeWorker := worker.NewAuthPurgeWorker(db, log)
	authPurgeWorker.Start()

	// Initialize cleanup worker
	cleanupWorker := worker.NewCleanupWorker(db, blobStore, log)

	cleanupWorker.Start()

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
				webhookDispatcher.Stop, cleanupWorker.Stop, authPurgeWorker.Stop, proxyCacheWorker.Stop,
				npmWorker.Stop, mavenWorker.Stop, debWorker.Stop, rpmWorker.Stop,
			} {
				stop()
			}

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
		mcpServer = aimcp.NewServer(db, aiWorker, log)
		log.Infow("MCP server enabled", "transport", cfg.MCP.Transport, "path", cfg.MCP.HTTPPath)
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

	evaluator := auth.NewEvaluator(db, log, cfg.Auth.AllowAnonymousRead)
	rbacCfg := middleware.RBACConfig{
		LegacyAPIKey:        cfg.Server.APIKey,
		LegacyAPIKeyEnabled: cfg.Auth.LegacyAPIKeyEnabled,
		AllowAnonymousRead:  cfg.Auth.AllowAnonymousRead,
		MCPPath:             cfg.MCP.HTTPPath,
		SPAHandler:          webhandler.Handler(),
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

	authedAPI.Route("/repos/{name}", func(r chi.Router) {
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
	authedAPI.Route("/repos/raw/{name}", func(r chi.Router) { rawHTTP.RegisterRepoRoutes(r) })
	log.Infow("Raw handler registered", "prefix", "/repos/raw/{name}")

	// Maven, SBT, Ivy use wildcard paths that shadow other handlers — isolate them.
	authedAPI.Route("/repos/maven/{name}", func(r chi.Router) { mavenHTTP.RegisterRepoRoutes(r) })
	authedAPI.Route("/repos/sbt/{name}", func(r chi.Router) { sbtHTTP.RegisterRepoRoutes(r) })
	authedAPI.Route("/repos/ivy/{name}", func(r chi.Router) { ivyHTTP.RegisterRepoRoutes(r) })
	log.Infow("Maven/SBT/Ivy handlers registered (isolated prefixes)")
	dockerHTTP := formatdocker.NewHTTPHandler(log, db, blobStore, cfg.BaseURL)
	dockerHTTP.RegisterRoutes(authedAPI)

	repoHandler := api.NewRepositoriesHandler(db, log, evaluator).WithBlobStore(blobStore).WithPublisher(webhookDispatcher)
	repoHandler.RegisterAuthed(authedAPI)

	cleanupHandler := api.NewCleanupHandler(db, log, evaluator, cleanupWorker)
	cleanupHandler.RegisterAuthed(authedAPI)

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

	// Mount MCP server if enabled with HTTP transport. Mounted under authedAPI
	// (not the bare router) so it inherits the same RBAC/CSRF/audit chain as
	// the rest of the API — MCP tools must require auth like any other route.
	if mcpServer != nil && cfg.MCP.Transport == "http" {
		authedAPI.Mount(cfg.MCP.HTTPPath, mcpServer.HTTPHandler())
		log.Infow("MCP HTTP endpoint registered", "path", cfg.MCP.HTTPPath)
	}

	// SPA fallback on the authed sub-router: paths not matched by any API or
	// repo route serve the Vue SPA (client-side routing). RBAC is bypassed for
	// these paths by the early-exit in RBACMiddleware below.
	authedAPI.NotFound(webhandler.Handler().ServeHTTP)
	router.Mount("/", authedAPI)

	// Start server
	if err := server.Start(context.Background()); err != nil {
		log.Errorw("Failed to start server", "error", err)
		return
	}

	log.Infow("Server started", "address", addr)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Infow("Shutdown signal received, gracefully stopping")

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

	log.Infow("Server stopped successfully")
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
