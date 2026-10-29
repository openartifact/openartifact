// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// writeAdminList is the shared body of an admin list endpoint: it requires the
// global manage permission, loads the rows with fetch and answers 200
// {"data": [toResp(row)...]}. A fetch failure is a 500 "database error".
func writeAdminList[T, R any](
	w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator,
	fetch func(ctx context.Context) ([]*T, error), toResp func(*T) R,
) {
	if !requireAdmin(w, r, evaluator) {
		return
	}

	rows, err := fetch(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(rows))
	for _, row := range rows {
		data = append(data, toResp(row))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// deleteAdminObject is the shared body of an admin delete-by-{id} endpoint: it
// requires the global manage permission, answers 404 "<noun> not found" when
// get finds no row (nothing is deleted or audited), deletes the row with del,
// records the audit event (resourceType, details) and answers 204.
func deleteAdminObject[T any](
	w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator,
	get func(ctx context.Context, id string) (*T, error), del func(ctx context.Context, id string) error,
	auditAction, resourceType, noun, details string,
) {
	if !requireAdmin(w, r, evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	row, err := get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if row == nil {
		writeError(w, http.StatusNotFound, "not_found", noun+" not found")
		return
	}

	if err := del(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	auditAdmin(r, auditAction, resourceType, id, details)

	writeNoContent(w)
}

// writeAdminObject is the shared body of an admin get-by-{id} endpoint: it
// requires the global manage permission, loads the row with get and answers 200
// with toResp(row), or 404 "<noun> not found" when there is none.
func writeAdminObject[T, R any](
	w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator,
	get func(ctx context.Context, id string) (*T, error), toResp func(*T) R, noun string,
) {
	if !requireAdmin(w, r, evaluator) {
		return
	}

	row, err := get(r.Context(), urlparams.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if row == nil {
		writeError(w, http.StatusNotFound, "not_found", noun+" not found")
		return
	}

	writeJSON(w, http.StatusOK, toResp(row))
}

// writeAdminListByID is writeAdminList for the rows that hang off the {id} URL
// segment (a group's members, a user's groups).
func writeAdminListByID[T, R any](
	w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator,
	fetch func(ctx context.Context, id string) ([]*T, error), toResp func(*T) R,
) {
	id := urlparams.Vars(r)["id"]

	writeAdminList(w, r, evaluator, func(ctx context.Context) ([]*T, error) { return fetch(ctx, id) }, toResp)
}
