// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type QuotasHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewQuotasHandler(log *logger.Logger, db database.Database, evaluator *auth.Evaluator) *QuotasHandler {
	return &QuotasHandler{db: db, log: log, evaluator: evaluator}
}

// requireAdmin gates quota writes: limits are an administrative control.
func (h *QuotasHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	isAdmin, err := principalIsAdmin(r.Context(), currentPrincipal(r), h.evaluator)
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

func (h *QuotasHandler) RegisterRoutes(r chi.Router, pathPrefix string) {
	r.Get(pathPrefix+"/quota", h.handleGetQuota)
	r.Put(pathPrefix+"/quota", h.handleUpdateQuota)
	r.Delete(pathPrefix+"/quota", h.handleDeleteQuota)
}

func (h *QuotasHandler) handleGetQuota(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	if ok, err := h.evaluator.Evaluate(ctx, *p, name, "", "read"); err != nil || !ok {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get quota: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	quota, err := h.db.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("get quota: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, quotaToMap(quota))
}

func (h *QuotasHandler) handleUpdateQuota(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("update quota: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	var body struct {
		MaxSizeBytes         *int64 `json:"max_size_bytes"`
		MaxArtifactCount     *int   `json:"max_artifact_count"`
		WarnThresholdPercent *int   `json:"warn_threshold_percent"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if (body.MaxSizeBytes != nil && *body.MaxSizeBytes < 0) ||
		(body.MaxArtifactCount != nil && *body.MaxArtifactCount < 0) {
		writeError(w, http.StatusBadRequest, "validation_failed", "limits must not be negative (0 means unlimited)")
		return
	}

	if body.WarnThresholdPercent != nil && (*body.WarnThresholdPercent < 0 || *body.WarnThresholdPercent > 100) {
		writeError(w, http.StatusBadRequest, "validation_failed", "warn_threshold_percent must be between 0 and 100")
		return
	}

	quota, err := h.db.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("update quota: db error getting quota", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if body.MaxSizeBytes != nil {
		quota.MaxSizeBytes = *body.MaxSizeBytes
	}

	if body.MaxArtifactCount != nil {
		quota.MaxArtifactCount = *body.MaxArtifactCount
	}

	if body.WarnThresholdPercent != nil {
		quota.WarnThresholdPercent = *body.WarnThresholdPercent
	}

	if err := h.db.UpdateStorageQuota(ctx, quota); err != nil {
		h.log.Errorw("update quota: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, quotaToMap(quota))
}

func (h *QuotasHandler) handleDeleteQuota(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete quota: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	quota, err := h.db.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("delete quota: db error getting quota", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Reset to unlimited
	quota.MaxSizeBytes = 0
	quota.MaxArtifactCount = 0

	if err := h.db.UpdateStorageQuota(ctx, quota); err != nil {
		h.log.Errorw("delete quota: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

func quotaToMap(q *models.StorageQuota) responses.StorageQuotaResponse {
	return responses.StorageQuotaResponse{
		ID:                   q.ID,
		RepositoryID:         q.RepositoryID,
		MaxSizeBytes:         q.MaxSizeBytes,
		MaxArtifactCount:     q.MaxArtifactCount,
		WarnThresholdPercent: q.WarnThresholdPercent,
		CurrentSizeBytes:     q.CurrentSizeBytes,
		CurrentArtifactCount: q.CurrentArtifactCount,
		UsagePercent:         int(q.UsagePercent()),
		IsExceeded:           q.IsExceeded(),
		CreatedAt:            formatTime(q.CreatedAt),
		UpdatedAt:            formatTime(q.UpdatedAt),
	}
}
