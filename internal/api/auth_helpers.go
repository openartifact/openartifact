// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/openartifact/openartifact/internal/passwd"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
)

const maxRequestBodyBytes int64 = 1 << 20

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// errNULInBody is returned by decodeJSON for a body that carries a U+0000
// character, which PostgreSQL text columns cannot store.
var errNULInBody = errors.New("request body contains a NUL character")

// decodeJSON decodes the first JSON value of the request body into dst. A
// body with a U+0000 escape is rejected so that handlers answer 400 for it
// instead of failing in the database.
func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		return err //nolint:wrapcheck // callers only test for nil
	}

	if hasEscapedNUL(body) {
		return errNULInBody
	}

	return json.NewDecoder(bytes.NewReader(body)).Decode(dst)
}

// hasEscapedNUL reports whether b holds a \u0000 JSON escape that is not itself
// escaped (an even number of backslashes before the one that starts it).
func hasEscapedNUL(b []byte) bool {
	const esc = `\u0000`

	for off := 0; ; {
		i := bytes.Index(b[off:], []byte(esc))
		if i < 0 {
			return false
		}

		i += off
		slashes := 0

		for j := i - 1; j >= 0 && b[j] == '\\'; j-- {
			slashes++
		}

		if slashes%2 == 0 {
			return true
		}

		off = i + len(esc)
	}
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
	return requireAdminCode(w, r, evaluator, "forbidden")
}

// requireAdminCode is requireAdmin with a caller-chosen error code for the 403,
// for the admin surfaces whose clients key off a code other than "forbidden".
func requireAdminCode(w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator, forbiddenCode string) bool {
	isAdmin, err := principalIsAdmin(r.Context(), currentPrincipal(r), evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return false
	}

	if !isAdmin {
		writeError(w, http.StatusForbidden, forbiddenCode, "admin access required")
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
// (passwd.ValidatePassword) and writes a 400 invalid_request carrying the
// sentinel's message when it fails. It returns true when the password is
// acceptable.
func checkPasswordPolicy(w http.ResponseWriter, plain string) bool {
	if err := passwd.ValidatePassword(plain); err != nil {
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
