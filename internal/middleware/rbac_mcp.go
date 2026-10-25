// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/pkg/logger"
)

// MCPBearerVerifier maps a bearer credential that is not an API token (an OIDC
// access token) to the local user it identifies. It returns auth.ErrAccessTokenNotAccepted
// (or any error) for credentials it does not accept.
type MCPBearerVerifier func(ctx context.Context, token string) (*models.User, error)

// protectedResourceWellKnown is the RFC 9728 well-known path segment.
const protectedResourceWellKnown = "/.well-known/oauth-protected-resource"

// ProtectedResourceMetadataURL is where the RFC 9728 metadata of the MCP
// endpoint at mcpPath is served: the well-known segment is inserted between the
// host and the path (RFC 9728 section 3.1). baseURL is the public URL of the
// server without a path.
func ProtectedResourceMetadataURL(baseURL, mcpPath string) string {
	return strings.TrimRight(baseURL, "/") + protectedResourceWellKnown + mcpPath
}

// MCPResourceURL is the canonical resource identifier (RFC 8707) of the MCP
// endpoint: the base URL plus its mount path. OIDC access tokens must name it in
// their aud claim.
func MCPResourceURL(baseURL, mcpPath string) string {
	return strings.TrimRight(baseURL, "/") + mcpPath
}

// isMCPPath reports whether urlPath belongs to the MCP endpoint.
func isMCPPath(urlPath string, cfg RBACConfig) bool {
	return cfg.MCPPath != "" && (urlPath == cfg.MCPPath || strings.HasPrefix(urlPath, cfg.MCPPath+"/"))
}

// resolveMCPPrincipal authenticates a request to the MCP endpoint. Only an
// Authorization: Bearer credential counts: an API token, an OIDC access token
// for this resource, or (deprecated) the legacy API key. The session cookie and
// Basic credentials are ambient, so a page on another origin could make the
// browser send them; ignoring them removes the CSRF surface of the endpoint.
func resolveMCPPrincipal(ctx context.Context, r *http.Request, cfg RBACConfig, db database.Database, log *logger.Logger) (*auth.Principal, string) {
	bearer, _ := authorizationSecrets(r)
	if bearer == "" {
		return &auth.Principal{IsAnonymous: true, Credential: auth.CredentialNone}, auth.CredentialNone
	}

	if p := tokenPrincipal(ctx, bearer, auth.CredentialBearerToken, db, log); p != nil {
		return p, auth.CredentialBearerToken
	}

	if cfg.MCPBearerVerifier != nil && !strings.HasPrefix(bearer, auth.TokenPrefixAccess) && !strings.HasPrefix(bearer, auth.TokenPrefixDeploy) {
		user, err := cfg.MCPBearerVerifier(ctx, bearer)

		switch {
		case err == nil && user != nil:
			p := principalForUser(ctx, db, log, principalUser{id: user.ID, username: user.Username, mustChangePassword: user.MustChangePassword})
			p.Credential = auth.CredentialBearerToken

			return p, auth.CredentialBearerToken
		case err != nil && !errors.Is(err, auth.ErrAccessTokenNotAccepted):
			log.Warnw("mcp bearer verification error", "error", err)
		}
	}

	if cfg.LegacyAPIKeyEnabled && cfg.LegacyAPIKey != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(cfg.LegacyAPIKey)) == 1 {
		log.Warnw("request authenticated via deprecated API_KEY; migrate to user tokens",
			"event", "legacy_api_key_used",
			"path", r.URL.Path,
			"method", r.Method,
			"remote_addr", r.RemoteAddr,
		)

		return &auth.Principal{Username: "system/legacy-api-key", IsLegacy: true, Credential: auth.CredentialLegacy}, auth.CredentialLegacy
	}

	return &auth.Principal{IsAnonymous: true, Credential: auth.CredentialNone}, auth.CredentialNone
}

// gateMCP admits an authenticated, non-service principal to the MCP endpoint.
// It does not decide what the caller may do: each MCP tool checks the caller's
// own permission on the repository it touches, so a read-only token can use the
// read tools. It reports whether the request may proceed.
func gateMCP(w http.ResponseWriter, r *http.Request, cfg RBACConfig, log *logger.Logger, principal *auth.Principal, credential string) bool {
	if principal.IsAnonymous {
		challenge := `Bearer realm="` + AuthRealm + `"`

		if cfg.BaseURL != "" {
			challenge += `, resource_metadata="` + ProtectedResourceMetadataURL(cfg.BaseURL, cfg.MCPPath) + `"`
		}

		if r.Header.Get("Authorization") != "" {
			challenge += `, error="invalid_token"`
		}

		log.Warnw("access denied", "event", "auth_deny", "principal", "anonymous", "credential", credential, "path", r.URL.Path, "action", "mcp")
		w.Header().Set("WWW-Authenticate", challenge)
		httpx.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})

		return false
	}

	// A project token's service account is a repository credential only.
	if principal.ServiceProjectID != "" {
		log.Warnw("access denied", "event", "auth_deny", "principal", principal.Username, "credential", credential, "path", r.URL.Path, "action", "mcp")
		httpx.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})

		return false
	}

	return true
}

// serveMCP authenticates a request to the MCP endpoint (bearer credentials
// only) and lets it through when it carries an acceptable principal. The tools
// authorize every repository access themselves, so the evaluator is not
// consulted here.
func serveMCP(w http.ResponseWriter, r *http.Request, next http.Handler, cfg RBACConfig, db database.Database, log *logger.Logger) {
	principal, credential := resolveMCPPrincipal(r.Context(), r, cfg, db, log)
	r = r.WithContext(WithPrincipal(r.Context(), principal))

	if passwordChangeBlocked(r, principal) {
		httpx.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "password_change_required"})

		return
	}

	if gateMCP(w, r, cfg, log, principal, credential) {
		next.ServeHTTP(w, r)
	}
}
