// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
)

const maxRequestBodyBytes int64 = 1 << 20

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(r *http.Request, dst any) error {
	limited := io.LimitReader(r.Body, maxRequestBodyBytes)
	return json.NewDecoder(limited).Decode(dst)
}

func currentPrincipal(r *http.Request) *auth.Principal {
	return middleware.PrincipalFromContext(r.Context())
}

func principalIsAdmin(ctx context.Context, p *auth.Principal, evaluator *auth.Evaluator) (bool, error) {
	if p == nil || p.IsAnonymous {
		return false, nil
	}

	return evaluator.Evaluate(ctx, *p, "", "", "manage")
}

// requireAdmin writes a 500/403 and returns false unless the request's
// principal holds the global `manage` permission. The RBAC middleware only
// performs a coarse check (and lets sessions reach a few self-service
// routes), so every administrative handler must call this itself.
func requireAdmin(w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator) bool {
	isAdmin, err := principalIsAdmin(r.Context(), currentPrincipal(r), evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return false
	}

	if !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return false
	}

	return true
}

// requireRepoAdmin writes a 500/403 and returns false unless the principal is a
// global admin or a maintainer/owner of the project that owns repoName
// (auth.EvaluateRepoAdmin). The RBAC middleware gates the delegated routes the
// same way; this keeps each handler safe on its own.
func requireRepoAdmin(w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator, repoName string) bool {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return false
	}

	ok, err := evaluator.EvaluateRepoAdmin(r.Context(), *p, repoName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return false
	}

	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return false
	}

	return true
}

// checkPasswordPolicy validates plain against the shared password policy
// (auth.ValidatePassword) and writes a 400 invalid_request carrying the
// sentinel's message when it fails. It returns true when the password is
// acceptable.
func checkPasswordPolicy(w http.ResponseWriter, plain string) bool {
	if err := auth.ValidatePassword(plain); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}

	return true
}

// sessionCookieValue returns the raw oa_session cookie value, or "" if absent.
func sessionCookieValue(r *http.Request) string {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}

	return c.Value
}
