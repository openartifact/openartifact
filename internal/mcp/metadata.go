// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	oaauth "github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// issuerCacheTTL bounds how stale the advertised authorization servers can be;
// the endpoint is unauthenticated, so it must not hit the database per request.
const issuerCacheTTL = 30 * time.Second

// MetadataHandler serves the RFC 9728 OAuth 2.0 Protected Resource Metadata of
// the MCP endpoint. Mount it, unauthenticated, at
// /.well-known/oauth-protected-resource{mcpPath} (and the bare well-known path).
//
// authorization_servers lists the issuers of the enabled OIDC providers, whose
// access tokens (audience = the MCP resource URI) the endpoint accepts. With no
// OIDC provider the field is omitted, which RFC 9728 permits: clients then use
// the bearer API tokens created in the web UI.
type MetadataHandler struct {
	db      database.Database
	log     *logger.Logger
	baseURL string
	mcpPath string

	mu      sync.Mutex
	issuers []string
	fetched time.Time
	now     func() time.Time
}

// NewMetadataHandler creates the handler. baseURL is BASE_URL; when empty the
// resource identifier is derived from the request.
func NewMetadataHandler(db database.Database, log *logger.Logger, baseURL, mcpPath string) *MetadataHandler {
	return &MetadataHandler{db: db, log: log, baseURL: baseURL, mcpPath: mcpPath, now: time.Now}
}

func (h *MetadataHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	meta := &oauthex.ProtectedResourceMetadata{
		Resource:               h.resource(r),
		AuthorizationServers:   h.authorizationServers(r.Context()),
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "OpenArtifact MCP",
	}

	w.Header().Set("Cache-Control", "public, max-age=60")
	auth.ProtectedResourceMetadataHandler(meta).ServeHTTP(w, r)
}

func (h *MetadataHandler) resource(r *http.Request) string {
	if h.baseURL != "" {
		return middleware.MCPResourceURL(h.baseURL, h.mcpPath)
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	return scheme + "://" + r.Host + h.mcpPath
}

func (h *MetadataHandler) authorizationServers(ctx context.Context) []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.now().Sub(h.fetched) < issuerCacheTTL {
		return h.issuers
	}

	issuers, err := oaauth.OIDCIssuers(ctx, h.db)
	if err != nil {
		h.log.Warnw("mcp: list oidc issuers for resource metadata", "error", err)

		// Keep serving the previous answer; retry after the TTL.
		h.fetched = h.now()

		return h.issuers
	}

	h.issuers, h.fetched = issuers, h.now()

	return h.issuers
}
