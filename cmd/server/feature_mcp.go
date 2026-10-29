// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package main

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
)

// mcpFeature is what the MCP server adds to the features struct.
type mcpFeature struct {
	mcpServer *aimcp.Server
}

// setupMCP builds the MCP server when it is enabled.
func (a *app) setupMCP() {
	cfg := a.cfg

	if !cfg.MCP.Enabled {
		return
	}

	opts := aimcp.Options{
		Version:            version,
		BaseURL:            cfg.BaseURL,
		MaxBodyBytes:       cfg.MCP.MaxBodyBytes,
		RateLimitPerMinute: cfg.MCP.RateLimitPerMinute,
		AITriggerPerMinute: cfg.MCP.AITriggerPerMinute,
	}
	if cfg.BaseURL != "" {
		opts.ResourceMetadataURL = middleware.ProtectedResourceMetadataURL(cfg.BaseURL, cfg.MCP.HTTPPath)
	}

	a.feat.mcpServer = aimcp.NewServer(a.db, a.mcpAnalyzer(), a.log, opts)
	a.feat.mcpServer.SetExploit(a.workers.exploit)
	a.log.Infow("MCP server enabled", "path", cfg.MCP.HTTPPath)
}

// addMCPMiddleware installs the Origin check of the MCP endpoint.
func (a *app) addMCPMiddleware(srv *api.HTTPServer) {
	if a.feat.mcpServer == nil {
		return
	}

	cfg := a.cfg
	srv.AddMiddleware(aimcp.OriginMiddleware(cfg.MCP.HTTPPath, cfg.BaseURL, cfg.MCP.AllowedOrigins))
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

// registerMCPMetadata serves the RFC 9728 protected-resource metadata of the
// MCP endpoint: public, outside the authenticated router so clients can
// discover how to authenticate.
func (a *app) registerMCPMetadata(router chi.Router) {
	if a.feat.mcpServer == nil {
		return
	}

	cfg := a.cfg
	metadata := aimcp.NewMetadataHandler(a.db, a.log, cfg.BaseURL, cfg.MCP.HTTPPath)
	router.Handle("/.well-known/oauth-protected-resource", metadata)
	router.Handle("/.well-known/oauth-protected-resource"+cfg.MCP.HTTPPath, metadata)
}

// mountMCP mounts the MCP server on the authenticated router (HTTP is the only
// transport), so it inherits the RBAC and audit chain: RBAC accepts only bearer
// credentials here (never the session cookie) and refuses anonymous callers;
// each tool then checks the caller's own permissions.
func (a *app) mountMCP(authedAPI chi.Router) {
	if a.feat.mcpServer == nil {
		return
	}

	authedAPI.Mount(a.cfg.MCP.HTTPPath, a.feat.mcpServer.HTTPHandler())
	a.log.Infow("MCP HTTP endpoint registered", "path", a.cfg.MCP.HTTPPath)
}
