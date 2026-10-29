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
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// QuotasStore is the part of the database QuotasHandler needs.
type QuotasStore interface {
	repoAccessStore
	GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error)
	GetProjectQuota(ctx context.Context, projectID string) (*models.ProjectQuota, error)
	GetRepositoryUsage(ctx context.Context, repoID string) (*models.RepositoryUsage, error)
	ListQuotaOverview(ctx context.Context, params models.QuotaListParams) ([]*models.QuotaOverview, int, error)
	ReconcileStorageUsage(ctx context.Context, repoID string) (*models.StorageQuota, error)
	UpdateStorageQuota(ctx context.Context, quota *models.StorageQuota) error
}

type QuotasHandler struct {
	repoAccess

	db QuotasStore
}

func NewQuotasHandler(log *logger.Logger, db QuotasStore, evaluator *auth.Evaluator) *QuotasHandler {
	return &QuotasHandler{repoAccess: newRepoAccess(db, log, evaluator), db: db}
}

func (h *QuotasHandler) RegisterRoutes(r chi.Router, pathPrefix string) {
	r.Get(pathPrefix+"/quota", h.handleGetQuota)
	r.Put(pathPrefix+"/quota", h.handleUpdateQuota)
	r.Delete(pathPrefix+"/quota", h.handleDeleteQuota)
	r.Post(pathPrefix+"/quota/reconcile", h.handleReconcileQuota)
	r.Get(pathPrefix+"/usage", h.handleGetUsage)
}

// RegisterAdminRoutes registers the instance-wide quota overview.
func (h *QuotasHandler) RegisterAdminRoutes(r chi.Router) {
	r.Get("/api/v1/admin/quotas", h.handleListOverview)
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

	repo, ok := h.loadRepo(w, r, "get quota")
	if !ok {
		return
	}

	quota, err := h.db.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		h.dbError(w, "get quota: db error", err)
		return
	}

	writeJSON(w, http.StatusOK, quotaToMap(quota))
}

func (h *QuotasHandler) handleUpdateQuota(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoAdminFirst(w, r, "update quota")
	if !ok {
		return
	}

	ctx := r.Context()

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
		h.dbError(w, "update quota: db error getting quota", err)
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
		h.dbError(w, "update quota: project quota lookup failed", err)
		return
	} else if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if err := h.db.UpdateStorageQuota(ctx, quota); err != nil {
		h.dbError(w, "update quota: db error", err)
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
	repo, ok := h.repoAdminFirst(w, r, "delete quota")
	if !ok {
		return
	}

	ctx := r.Context()

	quota, err := h.db.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		h.dbError(w, "delete quota: db error getting quota", err)
		return
	}

	// Reset to unlimited
	quota.MaxSizeBytes = 0
	quota.MaxArtifactCount = 0

	if err := h.db.UpdateStorageQuota(ctx, quota); err != nil {
		h.dbError(w, "delete quota: db error", err)
		return
	}

	writeNoContent(w)
}

// handleReconcileQuota recomputes the repository's usage counters from its
// package rows. The counters are incremental and written with asynchronous
// commits, so a database crash (or a restore) can leave them off; this repairs
// them without touching the limits.
func (h *QuotasHandler) handleReconcileQuota(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminFirstRepo(w, r, "reconcile quota")
	if !ok {
		return
	}

	quota, err := h.db.ReconcileStorageUsage(r.Context(), repo.ID)
	if err != nil {
		h.log.Errorw("reconcile quota: db error", "repository", urlparams.Vars(r)["name"], "error", err)
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
