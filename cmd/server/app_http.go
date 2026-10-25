// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/branding"
	"github.com/openartifact/openartifact/internal/database/models"
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/internal/trash"
	webhandler "github.com/openartifact/openartifact/web"
)

// buildHTTP creates the HTTP server with its middleware chain, the
// authorization stack, and registers every route. The server is not started.
func (a *app) buildHTTP() (*api.HTTPServer, error) {
	srv := a.newHTTPServer()

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
	a.registerFormatRoutes()
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
	srv.AddMiddleware(middleware.SecurityHeadersMiddleware(cfg.Server.TLSEnabled || strings.HasPrefix(strings.ToLower(cfg.BaseURL), "https://")))
	srv.AddMiddleware(middleware.RecoveryMiddleware(a.log))
	srv.AddMiddleware(middleware.RequestIDMiddleware)
	srv.AddMiddleware(middleware.MetricsMiddleware(a.reg))
	// OpenTelemetry server spans (no-op while telemetry is disabled); before
	// TracingMiddleware and LoggingMiddleware, which read the span context.
	srv.AddMiddleware(telemetry.HTTPMiddleware())
	srv.AddMiddleware(middleware.TracingMiddleware)
	srv.AddMiddleware(middleware.LoggingMiddleware(a.log))
	srv.AddMiddleware(middleware.CORSMiddleware(cfg.Server.CORSOrigins))

	if a.feat.mcpServer != nil {
		srv.AddMiddleware(aimcp.OriginMiddleware(cfg.MCP.HTTPPath, cfg.BaseURL, cfg.MCP.AllowedOrigins))
	}
}

// addAccessMiddleware installs the optional client-address based middleware:
// the IP allowlist and the rate limiter.
func (a *app) addAccessMiddleware(srv *api.HTTPServer) {
	cfg := a.cfg

	a.web.trustedProxies = middleware.NewTrustedProxies(cfg.Server.TrustedProxies)

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
	}

	a.configureMCPAuth(&rbacCfg)

	w.rbacMiddleware = middleware.RBACMiddleware(rbacCfg, db, w.evaluator, log)

	a.logAuthMode()

	return nil
}

// configureMCPAuth wires the MCP server into authorization: the OIDC bearer
// verifier, the per-repository authorizer, the read filter and the searcher.
func (a *app) configureMCPAuth(rbacCfg *middleware.RBACConfig) {
	mcpServer := a.feat.mcpServer
	if mcpServer == nil {
		return
	}

	cfg, db, evaluator := a.cfg, a.db, a.web.evaluator

	// OIDC access tokens are audience-bound to the canonical MCP resource URI,
	// which needs a configured public base URL.
	if cfg.BaseURL != "" {
		resource := middleware.MCPResourceURL(cfg.BaseURL, cfg.MCP.HTTPPath)
		rbacCfg.MCPBearerVerifier = func(ctx context.Context, token string) (*models.User, error) {
			return auth.VerifyOIDCAccessToken(ctx, db, cfg.BaseURL, token, resource)
		}
	} else {
		a.log.Warnw("BASE_URL is not set: OIDC access tokens are not accepted on the MCP endpoint (API tokens still work)")
	}

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
