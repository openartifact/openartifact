// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/branding"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/publicurl"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/internal/trash"
	webhandler "github.com/openartifact/openartifact/web"
)

// buildHTTP creates the HTTP server with its middleware chain, the
// authorization stack, and registers every route. The server is not started.
func (a *app) buildHTTP() (*api.HTTPServer, error) {
	srv := a.newHTTPServer()

	// A worker process serves only the operational routes: no authorization
	// stack, no API, no format endpoints. It builds the services whose
	// background work it runs instead of their routes.
	if !a.cfg.Server.ServesAPI() {
		a.web.router = srv.GetRouter()
		a.registerOperationalRoutes()
		a.buildWorkerServices()

		return srv, nil
	}

	if err := a.setupAuthorization(); err != nil {
		return nil, err
	}

	a.web.router = srv.GetRouter()

	a.registerOperationalRoutes()

	if err := a.registerPublicRoutes(); err != nil {
		return nil, err
	}

	a.newAuthedRouter()
	a.registerAdminRoutes()
	if err := a.registerFormatRoutes(); err != nil {
		return nil, err
	}

	a.registerRepositoryRoutes()
	a.registerSupplyChainRoutes()
	a.registerDependencyTrack()
	a.registerWebhooksAndBackup()
	a.registerScanningRoutes()
	a.finishRoutes()

	return srv, nil
}

// newHTTPServer creates the server and installs its middleware chain.
func (a *app) newHTTPServer() *api.HTTPServer {
	cfg := a.cfg

	a.web.addr = net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	srv := api.NewHTTPServer(a.web.addr, a.log)
	srv.SetTimeouts(
		time.Duration(cfg.Server.ReadTimeout)*time.Second,
		time.Duration(cfg.Server.WriteTimeout)*time.Second,
		time.Duration(cfg.Server.ReadHeaderTimeout)*time.Second,
	)

	srv.SetConnLimits(time.Duration(cfg.Server.IdleTimeout)*time.Second, cfg.Server.MaxHeaderBytes)

	if cfg.Server.TLSEnabled {
		srv.EnableTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
	}

	a.addBaseMiddleware(srv)
	a.addAccessMiddleware(srv)

	// Login, logout and SSO callbacks are on the bare router, outside the
	// authenticated chain's AuditMiddleware, so audit them explicitly.
	srv.AddMiddleware(middleware.AuthAuditMiddleware(a.auditLogger, a.db, a.web.trustedProxies))

	return srv
}

// addBaseMiddleware installs the middleware every request passes through:
// limits, headers, recovery, metrics, tracing, logging and CORS.
func (a *app) addBaseMiddleware(srv *api.HTTPServer) {
	cfg := a.cfg

	srv.AddMiddleware(middleware.MaxBodyMiddleware(cfg.Server.MaxBodyBytes))
	srv.AddMiddleware(
		middleware.SecurityHeadersMiddleware(cfg.Server.TLSEnabled || strings.HasPrefix(strings.ToLower(cfg.BaseURL), "https://")),
	)
	srv.AddMiddleware(middleware.RecoveryMiddleware(a.log))
	srv.AddMiddleware(middleware.RequestIDMiddleware)
	srv.AddMiddleware(middleware.MetricsMiddleware(a.reg))
	// OpenTelemetry server spans (no-op while telemetry is disabled); before
	// TracingMiddleware and LoggingMiddleware, which read the span context.
	srv.AddMiddleware(telemetry.HTTPMiddleware())
	srv.AddMiddleware(middleware.TracingMiddleware)
	srv.AddMiddleware(middleware.LoggingMiddleware(a.log))
	srv.AddMiddleware(middleware.CORSMiddleware(cfg.Server.CORSOrigins))
	srv.AddMiddleware(middleware.RejectNULBytes)

	a.addMCPMiddleware(srv)
}

// addAccessMiddleware installs the optional client-address based middleware:
// the IP allowlist and the rate limiter.
func (a *app) addAccessMiddleware(srv *api.HTTPServer) {
	cfg := a.cfg

	a.web.trustedProxies = middleware.NewTrustedProxies(cfg.Server.TrustedProxies)

	// Package-client URLs follow the request unless BASE_URL overrides them;
	// forwarded headers count only from a trusted proxy.
	srv.AddMiddleware(publicurl.Middleware(cfg.ClientBaseURL(), cfg.BaseURL, a.web.trustedProxies.TrustsPeer))

	if len(cfg.Server.IPAllowlist) > 0 {
		srv.AddMiddleware(middleware.IPAllowlistMiddleware(cfg.Server.IPAllowlist, a.web.trustedProxies))
		a.log.Infow("IP allowlist middleware enabled", "cidrs", cfg.Server.IPAllowlist)
	}

	if cfg.Server.RateLimitRPS > 0 {
		srv.AddMiddleware(middleware.RateLimitMiddleware(a.tasks.Context(), cfg.Server.RateLimitRPS, time.Second, a.web.trustedProxies))
		a.log.Infow("Rate limiting middleware enabled", "rps", cfg.Server.RateLimitRPS)
	}
}

// setupAuthorization builds the SPA/branding shell, the permission evaluator
// and the RBAC middleware that gates the authenticated router.
func (a *app) setupAuthorization() error {
	cfg, db, log := a.cfg, a.db, a.log

	// One SPA handler for both the RBAC page-load bypass and the router
	// fallback: building it hashes and precompresses every embedded asset.
	brandDefaults, err := cfg.Branding.Defaults()
	if err != nil {
		return a.fail("invalid branding configuration", err)
	}

	w := &a.web
	w.brandManager = branding.NewManager(db, brandDefaults, log)
	w.spaHandler = webhandler.Handler(w.brandManager)

	w.evaluator = auth.NewEvaluator(db, log, cfg.Auth.AllowAnonymousRead)
	trash.SetEvaluator(w.evaluator)
	w.projectSettings = auth.NewProjectSettings(db, cfg.Projects.SelfCreate)

	rbacCfg := middleware.RBACConfig{
		ProjectSettings:     w.projectSettings,
		LegacyAPIKey:        cfg.Server.APIKey,
		LegacyAPIKeyEnabled: cfg.Auth.LegacyAPIKeyEnabled,
		AllowAnonymousRead:  cfg.Auth.AllowAnonymousRead,
		MCPPath:             cfg.MCP.HTTPPath,
		SPAHandler:          w.spaHandler,
		BaseURL:             cfg.BaseURL,
		ClientBaseURL:       cfg.ClientBaseURL(),
	}

	a.configureMCPAuth(&rbacCfg)

	w.rbacMiddleware = middleware.RBACMiddleware(rbacCfg, db, w.evaluator, log)

	a.logAuthMode()

	return nil
}

// logAuthMode reports the RBAC setup and warns about an ignored legacy key.
func (a *app) logAuthMode() {
	cfg := a.cfg

	legacyAuthMode := "disabled"
	if cfg.Auth.LegacyAPIKeyEnabled {
		legacyAuthMode = "enabled"
	}

	if cfg.Server.APIKey != "" && !cfg.Auth.LegacyAPIKeyEnabled {
		a.log.Warnw("API_KEY is set but ignored: the legacy shared key is disabled by default; " +
			"set LEGACY_API_KEY_ENABLED=true to keep using it, or migrate to API tokens")
	}

	a.log.Infow("RBAC middleware enabled",
		"allow_anonymous_read", cfg.Auth.AllowAnonymousRead,
		"legacy_auth", legacyAuthMode,
	)
}
