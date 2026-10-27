// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	quotaOverviewDefaultLimit = 500
	quotaOverviewMaxLimit     = 2000
)

// quotaLine renders limits and live usage. A limit of 0 is unlimited and is
// left out; the exceeded rule is the one of models.StorageQuota.IsExceeded.
func quotaLine(maxSize, maxCount int64, warnPercent int, usedBytes, artifactCount int64) responses.QuotaLine {
	line := responses.QuotaLine{
		UsedBytes:            usedBytes,
		WarnThresholdPercent: warnPercent,
		ArtifactCount:        artifactCount,
		Exceeded:             (maxSize > 0 && usedBytes >= maxSize) || (maxCount > 0 && artifactCount >= maxCount),
	}

	if maxSize > 0 {
		soft := int64(float64(maxSize) * float64(warnPercent) / 100)
		line.LimitBytes, line.SoftLimitBytes = &maxSize, &soft
		line.UsagePercent = int(float64(usedBytes) / float64(maxSize) * 100)
	}

	if maxCount > 0 {
		line.MaxArtifactCount = &maxCount
	}

	return line
}

// handleGetUsage reports what a repository holds (packages, versions, bytes,
// newest publish) next to its quota. Package and version counts scan the
// repository's package rows, so this is a per-repository read, never a bulk
// one. Admins and the repository's project maintainers only, like the quota
// routes the middleware gates the same way.
//
//	GET /api/v1/repositories/{name}/usage
func (h *QuotasHandler) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoAdminFirst(w, r, "repository usage")
	if !ok {
		return
	}

	ctx := r.Context()

	quota, err := h.db.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		h.dbError(w, "repository usage: db error getting quota", err)
		return
	}

	usage, err := h.db.GetRepositoryUsage(ctx, repo.ID)
	if err != nil {
		h.dbError(w, "repository usage: db error", err)
		return
	}

	writeJSON(w, http.StatusOK, responses.RepositoryUsageResponse{
		PackageCount:    usage.PackageCount,
		VersionCount:    usage.VersionCount,
		SizeBytes:       quota.CurrentSizeBytes,
		LastPublishedAt: formatTimePtr(usage.LastPublishedAt),
		Quota: quotaLine(quota.MaxSizeBytes, int64(quota.MaxArtifactCount), quota.WarnThresholdPercent,
			quota.CurrentSizeBytes, int64(quota.CurrentArtifactCount)),
	})
}

// handleListOverview lists every live repository and every project with its
// quota and live usage, largest usage first, so the settings page needs one
// request instead of one per repository. Admin only.
//
//	GET /api/v1/admin/quotas?scope=repository|project&limited=true&q=&page=&limit=
func (h *QuotasHandler) handleListOverview(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	q := r.URL.Query()
	params := models.QuotaListParams{
		Scope:       q.Get("scope"),
		LimitedOnly: q.Get("limited") == "true",
		Query:       q.Get("q"),
		Page:        queryIntParam(r, "page", 1, 1, 10000),
		Limit:       queryIntParam(r, "limit", quotaOverviewDefaultLimit, 1, quotaOverviewMaxLimit),
	}

	if params.Scope != "" && params.Scope != models.QuotaScopeRepository && params.Scope != models.QuotaScopeProject {
		writeError(w, http.StatusBadRequest, "validation_failed", "scope must be repository or project")
		return
	}

	items, total, err := h.db.ListQuotaOverview(r.Context(), params)
	if err != nil {
		h.dbError(w, "list quotas failed", err)
		return
	}

	resp := responses.QuotaOverviewList{
		Quotas: make([]responses.QuotaOverviewItem, 0, len(items)),
		Total:  total,
		Page:   params.Page,
		Limit:  params.Limit,
	}

	for _, it := range items {
		resp.Quotas = append(resp.Quotas, responses.QuotaOverviewItem{
			Scope: it.Scope, Name: it.Name, Project: it.Project,
			QuotaLine: quotaLine(it.MaxSizeBytes, it.MaxArtifactCount, it.WarnThresholdPercent, it.UsedBytes, it.ArtifactCount),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}
