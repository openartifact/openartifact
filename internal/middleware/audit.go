// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
)

func AuditMiddleware(auditLogger *audit.Logger, trusted ...*TrustedProxies) func(http.Handler) http.Handler {
	var proxies *TrustedProxies
	if len(trusted) > 0 {
		proxies = trusted[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(wrapped, r)

			if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
				return
			}

			routeTemplate := "unknown"

			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if p := rctx.RoutePattern(); p != "" {
					routeTemplate = strings.TrimSuffix(p, "/*")
				}
			}

			actor := "anonymous"
			if p := PrincipalFromContext(r.Context()); p != nil && !p.IsAnonymous {
				actor = p.Username
			}

			action := routeToAction(r.Method, routeTemplate)
			// An auth.* action asserts the thing happened (token created,
			// password changed) and so does repository.deleted (a delete
			// refused for a missing confirmation did not); a rejected request
			// did not.
			if action == "" || ((strings.HasPrefix(action, "auth.") || action == "repository.deleted") && wrapped.statusCode >= http.StatusBadRequest) {
				action = audit.ActionHTTPRequest
			}
			clientIP := ClientIP(r, proxies)

			details := fmt.Sprintf(`{"method":"%s","path":"%s","status":%d}`, r.Method, routeTemplate, wrapped.statusCode)
			if wrapped.statusCode >= 400 {
				details = fmt.Sprintf(`{"method":"%s","path":"%s","status":%d,"error":true}`, r.Method, routeTemplate, wrapped.statusCode)
			}

			auditLogger.Log(r.Context(), audit.AuditEvent{
				Action:    action,
				Actor:     actor,
				Details:   details,
				IPAddress: &clientIP,
			})
		})
	}
}

func routeToAction(method, routeTemplate string) string {
	switch {
	case method == "POST" && routeTemplate == "/api/v1/repositories":
		return audit.ActionRepositoryCreated
	case method == "DELETE" && routeTemplate == "/api/v1/repository/{name}":
		return audit.ActionRepositoryDeleted
	case method == "POST" && routeTemplate == "/api/v1/repositories/{name}/debs":
		return audit.ActionArtifactUploaded
	case method == "PUT" && routeTemplate == "/repos/{repo}/{pkg:.*}":
		return audit.ActionArtifactUploaded
	case method == "POST" && routeTemplate == "/api/v1/auth/change-password":
		return audit.ActionAuthPasswordChanged
	case method == "POST" && (routeTemplate == "/api/v1/auth/tokens" || routeTemplate == "/api/v1/users/{id}/tokens"):
		return audit.ActionAuthTokenCreated
	case method == "DELETE" && (routeTemplate == "/api/v1/auth/tokens/{id}" || routeTemplate == "/api/v1/users/{id}/tokens/{tokenId}"):
		return audit.ActionAuthTokenRevoked
	}
	return ""
}

// Authentication endpoints live on the bare (unauthenticated) router, so the
// authedAPI AuditMiddleware never sees them. AuthAuditMiddleware wraps the
// whole server and records login, logout and SSO sign-in outcomes explicitly.
type authEndpoint int

const (
	authEndpointNone authEndpoint = iota
	authEndpointLogin
	authEndpointLogout
	authEndpointSSO
)

func classifyAuthEndpoint(method, path string) authEndpoint {
	switch {
	case method == http.MethodPost && path == "/api/v1/auth/login":
		return authEndpointLogin
	case method == http.MethodPost && path == "/api/v1/auth/logout":
		return authEndpointLogout
	case method == http.MethodGet && strings.HasPrefix(path, "/api/v1/auth/sso/oidc/") && strings.HasSuffix(path, "/callback"):
		return authEndpointSSO
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/auth/sso/saml/") && strings.HasSuffix(path, "/acs"):
		return authEndpointSSO
	}

	return authEndpointNone
}

const (
	loginPeekLimit  = 16 << 10
	maxActorNameLen = 128
)

// AuthAuditMiddleware audits auth.login, auth.login_failed, auth.logout and
// auth.sso_login. Success is decided from what the handler did to the session
// cookie (a Set-Cookie carrying a fresh oa_session value), not from the status
// code, because the SSO callbacks redirect on both success and failure.
func AuthAuditMiddleware(auditLogger *audit.Logger, db database.Database, trusted ...*TrustedProxies) func(http.Handler) http.Handler {
	var proxies *TrustedProxies
	if len(trusted) > 0 {
		proxies = trusted[0]
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind := classifyAuthEndpoint(r.Method, r.URL.Path)
			if kind == authEndpointNone || auditLogger == nil {
				next.ServeHTTP(w, r)
				return
			}

			var (
				attempted string
				logoutBy  auditActorInfo
			)

			switch kind {
			case authEndpointLogin:
				attempted = peekLoginUsername(r)
			case authEndpointLogout:
				// Resolve the actor before the handler deletes the session.
				if c, err := r.Cookie(auth.SessionCookieName); err == nil {
					logoutBy = actorFromSession(r.Context(), db, c.Value)
				}
			case authEndpointNone, authEndpointSSO:
			}

			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(wrapped, r)

			clientIP := ClientIP(r, proxies)
			ev := audit.AuditEvent{IPAddress: &clientIP}

			newSession := newSessionCookie(w.Header())

			switch kind {
			case authEndpointLogin:
				if newSession != "" {
					setActor(&ev, actorFromSession(r.Context(), db, newSession), "user")
					ev.Action = audit.ActionAuthLogin
					ev.Details = "login succeeded"
				} else {
					ev.Action = audit.ActionAuthLoginFailed
					ev.Actor = attempted
					ev.Details = fmt.Sprintf("login failed (status %d)", wrapped.statusCode)
				}
			case authEndpointLogout:
				if wrapped.statusCode >= http.StatusBadRequest {
					return
				}

				setActor(&ev, logoutBy, "user")
				ev.Action = audit.ActionAuthLogout
				ev.Details = "logout"
			case authEndpointSSO:
				if newSession != "" {
					setActor(&ev, actorFromSession(r.Context(), db, newSession), "sso")
					ev.Action = audit.ActionAuthSSOLogin
					ev.Details = "sso login succeeded"
				} else {
					ev.Action = audit.ActionAuthLoginFailed
					ev.ActorType = "sso"
					ev.Details = fmt.Sprintf("sso callback failed (status %d)", wrapped.statusCode)
				}
			case authEndpointNone:
				return
			}

			// Rate-limited attempts are not audited: one row per rejected
			// request would let an attacker flood the audit table.
			if wrapped.statusCode == http.StatusTooManyRequests {
				return
			}

			auditLogger.Log(r.Context(), ev)
		})
	}
}

type auditActorInfo struct {
	id       string
	username string
}

func setActor(ev *audit.AuditEvent, a auditActorInfo, actorType string) {
	ev.ActorType = actorType
	ev.Actor = a.username

	if a.id != "" {
		id := a.id
		ev.ActorID = &id
	}
}

// actorFromSession resolves the user behind a raw session cookie value. It
// returns the zero value when the cookie is not a valid live session.
func actorFromSession(ctx context.Context, db database.Database, cookieValue string) auditActorInfo {
	if cookieValue == "" || db == nil {
		return auditActorInfo{}
	}

	session, err := auth.ValidateSession(ctx, db, cookieValue)
	if err != nil || session == nil {
		return auditActorInfo{}
	}

	user, err := db.GetUserByID(ctx, session.UserID)
	if err != nil || user == nil {
		return auditActorInfo{}
	}

	return auditActorInfo{id: user.ID, username: user.Username}
}

// newSessionCookie returns the value of a non-empty oa_session cookie set in the
// response headers, or "" when the response sets none (or clears it).
func newSessionCookie(h http.Header) string {
	resp := http.Response{Header: http.Header{"Set-Cookie": h.Values("Set-Cookie")}}

	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" && c.MaxAge >= 0 {
			return c.Value
		}
	}

	return ""
}

// peekLoginUsername reads the attempted username out of a login request body
// without consuming it: the body is restored for the real handler. Only the
// username is ever extracted, never the password.
func peekLoginUsername(r *http.Request) string {
	if r.Body == nil || r.Body == http.NoBody {
		return ""
	}

	buf, err := io.ReadAll(io.LimitReader(r.Body, loginPeekLimit))
	if err != nil {
		buf = nil
	}

	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}

	var body struct {
		Username string `json:"username"`
	}

	if json.Unmarshal(buf, &body) != nil {
		return ""
	}

	name := body.Username
	if len(name) > maxActorNameLen {
		name = name[:maxActorNameLen]
	}

	return name
}
