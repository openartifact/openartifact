// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
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

// requireRepoAdmin gates the quota limit writes on {name}: a global admin or a
// maintainer/owner of the repository's project may set them; the usage
// reconcile stays admin-only (requireAdmin).
func (h *QuotasHandler) requireRepoAdmin(w http.ResponseWriter, r *http.Request) bool {
	return requireRepoAdmin(w, r, h.evaluator, urlparams.Vars(r)["name"])
}

func (h *QuotasHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	return requireAdmin(w, r, h.evaluator)
}

func (h *QuotasHandler) RegisterRoutes(r chi.Router, pathPrefix string) {
	r.Get(pathPrefix+"/quota", h.handleGetQuota)
	r.Put(pathPrefix+"/quota", h.handleUpdateQuota)
	r.Delete(pathPrefix+"/quota", h.handleDeleteQuota)
	r.Post(pathPrefix+"/quota/reconcile", h.handleReconcileQuota)
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
	if !h.requireRepoAdmin(w, r) {
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

	if msg, err := h.projectCeilingViolation(ctx, repo, quota); err != nil {
		h.log.Errorw("update quota: project quota lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	} else if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if err := h.db.UpdateStorageQuota(ctx, quota); err != nil {
		h.log.Errorw("update quota: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, quotaToMap(quota))
}

// projectCeilingViolation returns why quota's limits do not fit the repository
// project's ceiling ("" when they do): a repository limit above the project's
// is refused, since the project limit would silently win. 0 (unlimited) is
// always accepted, the project ceiling then bounds the repository.
func (h *QuotasHandler) projectCeilingViolation(ctx context.Context, repo *models.Repository, quota *models.StorageQuota) (string, error) {
	if repo.ProjectID == nil {
		return "", nil
	}

	pq, err := h.db.GetProjectQuota(ctx, *repo.ProjectID)
	if err != nil {
		return "", err //nolint:wrapcheck // logged by the caller
	}

	if pq.MaxSizeBytes > 0 && quota.MaxSizeBytes > pq.MaxSizeBytes {
		return fmt.Sprintf("max_size_bytes must not exceed the project limit (%d)", pq.MaxSizeBytes), nil
	}

	if pq.MaxArtifactCount > 0 && int64(quota.MaxArtifactCount) > pq.MaxArtifactCount {
		return fmt.Sprintf("max_artifact_count must not exceed the project limit (%d)", pq.MaxArtifactCount), nil
	}

	return "", nil
}

func (h *QuotasHandler) handleDeleteQuota(w http.ResponseWriter, r *http.Request) {
	if !h.requireRepoAdmin(w, r) {
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

// handleReconcileQuota recomputes the repository's usage counters from its
// package rows. The counters are incremental and written with asynchronous
// commits, so a database crash (or a restore) can leave them off; this repairs
// them without touching the limits.
func (h *QuotasHandler) handleReconcileQuota(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("reconcile quota: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	quota, err := h.db.ReconcileStorageUsage(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("reconcile quota: db error", "repository", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, quotaToMap(quota))
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
