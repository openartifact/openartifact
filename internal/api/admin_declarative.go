// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	declarativeMaxItems    = 1000
	declarativeMaxKind     = 64
	declarativeMaxName     = 512
	declarativeMaxObjectID = 256
	declarativeMaxHash     = 128
)

// declarativeKinds are the manifest kinds `oa apply` manages.
//
//nolint:gochecknoglobals // immutable lookup table
var declarativeKinds = map[string]bool{
	"User": true, "Project": true, "Group": true, "Repository": true, "PermissionTarget": true,
	"SSOProvider": true, "Webhook": true, "StorageTier": true, "TierPolicy": true,
}

// AdminDeclarativeHandler stores the ownership markers of `oa apply`: which
// objects it created or adopted, so --prune only deletes managed objects.
//
// Markers are deliberately not part of backups: a restored instance has none,
// so prune touches nothing until the next apply re-adopts the objects.
type AdminDeclarativeHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminDeclarativeHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminDeclarativeHandler {
	return &AdminDeclarativeHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminDeclarativeHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/declarative", h.handleList)
	r.Put("/api/v1/admin/declarative", h.handlePut)
	r.Delete("/api/v1/admin/declarative", h.handleDelete)
}

func declarativeToResponse(o models.DeclarativeObject) responses.DeclarativeObjectResponse {
	return responses.DeclarativeObjectResponse{
		Kind: o.Kind, Name: o.Name, ObjectID: o.ObjectID, AppliedHash: o.AppliedHash, AppliedAt: o.AppliedAt,
	}
}

func declarativeItems(objs []models.DeclarativeObject) responses.DeclarativeObjectsResponse {
	items := make([]responses.DeclarativeObjectResponse, 0, len(objs))
	for _, o := range objs {
		items = append(items, declarativeToResponse(o))
	}

	return responses.DeclarativeObjectsResponse{Items: items}
}

func (h *AdminDeclarativeHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	objs, err := h.db.ListDeclarativeObjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	writeJSON(w, http.StatusOK, declarativeItems(objs))
}

// validateDeclarativeItem returns a client-facing message, or "" when valid.
func validateDeclarativeItem(it models.DeclarativeObject) string {
	switch {
	case !declarativeKinds[it.Kind] || utf8.RuneCountInString(it.Kind) > declarativeMaxKind:
		return "kind must be one of User, Project, Group, Repository, PermissionTarget, SSOProvider, Webhook, StorageTier, TierPolicy"
	case it.Name == "" || utf8.RuneCountInString(it.Name) > declarativeMaxName:
		return "name must be 1-512 characters"
	case it.ObjectID == "" || utf8.RuneCountInString(it.ObjectID) > declarativeMaxObjectID:
		return "object_id must be 1-256 characters"
	case utf8.RuneCountInString(it.AppliedHash) > declarativeMaxHash:
		return "applied_hash must be at most 128 characters"
	}

	return ""
}

func (h *AdminDeclarativeHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Items []struct {
			Kind        string `json:"kind"`
			Name        string `json:"name"`
			ObjectID    string `json:"object_id"`
			AppliedHash string `json:"applied_hash"`
		} `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if len(body.Items) > declarativeMaxItems {
		writeError(w, http.StatusBadRequest, "validation_failed", "at most 1000 items per request")
		return
	}

	items := make([]models.DeclarativeObject, 0, len(body.Items))

	for _, b := range body.Items {
		it := models.DeclarativeObject{Kind: b.Kind, Name: b.Name, ObjectID: b.ObjectID, AppliedHash: b.AppliedHash}
		if msg := validateDeclarativeItem(it); msg != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", msg)
			return
		}

		items = append(items, it)
	}

	stored, err := h.db.UpsertDeclarativeObjects(r.Context(), items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	writeJSON(w, http.StatusOK, declarativeItems(stored))
}

func (h *AdminDeclarativeHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	q := r.URL.Query()
	kind, name := q.Get("kind"), q.Get("name")

	if !declarativeKinds[kind] || name == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "kind and name query parameters are required")
		return
	}

	deleted, err := h.db.DeleteDeclarativeObject(r.Context(), kind, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "no such declarative object")
		return
	}

	writeNoContent(w)
}
