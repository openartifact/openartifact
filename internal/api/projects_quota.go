// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"net/http"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

const meteredRepoType = models.ProjectRepoTypeHosted

func projectQuotaResponse(project string, q *models.ProjectQuota, size, artifacts int64) responses.ProjectQuotaResponse {
	percent := 0
	if q.MaxSizeBytes > 0 {
		percent = int(min(size*100/q.MaxSizeBytes, 100))
	}

	return responses.ProjectQuotaResponse{
		Project:              project,
		MaxSizeBytes:         q.MaxSizeBytes,
		MaxArtifactCount:     q.MaxArtifactCount,
		WarnThresholdPercent: q.WarnThresholdPercent,
		CurrentSizeBytes:     size,
		CurrentArtifactCount: artifacts,
		UsagePercent:         percent,
		Enforced:             q.Limited(),
		UpdatedAt:            formatTime(q.UpdatedAt),
	}
}

// handleGetUsage reports the project's usage to any member: totals, the
// ceiling and one line per live repository (sizes from the repository
// counters, which every format maintains).
//
//	GET /api/v1/projects/{project}/usage
func (h *ProjectsHandler) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	usage, err := h.db.GetProjectUsage(r.Context(), access.project.ID)
	if err != nil {
		h.dbError(w, "get project usage", err)
		return
	}

	repos := make([]responses.ProjectRepoUsageResponse, 0, len(usage.Repositories))

	for _, u := range usage.Repositories {
		repos = append(repos, responses.ProjectRepoUsageResponse{
			Name:                 u.Name,
			Format:               u.Format,
			RepoType:             u.RepoType,
			SizeBytes:            u.SizeBytes,
			ArtifactCount:        u.ArtifactCount,
			MaxSizeBytes:         u.MaxSizeBytes,
			MaxArtifactCount:     u.MaxArtifactCount,
			WarnThresholdPercent: u.WarnThresholdPercent,
			QuotaEnforced:        u.RepoType == meteredRepoType,
		})
	}

	writeJSON(w, http.StatusOK, responses.ProjectUsageResponse{
		Project:       access.project.Name,
		SizeBytes:     usage.SizeBytes,
		ArtifactCount: usage.ArtifactCount,
		Quota:         projectQuotaResponse(access.project.Name, &usage.Quota, usage.SizeBytes, usage.ArtifactCount),
		Repositories:  repos,
	})
}

// handleGetQuota returns the project ceiling and live usage to any member.
//
//	GET /api/v1/projects/{project}/quota
func (h *ProjectsHandler) handleGetQuota(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	usage, err := h.db.GetProjectUsage(r.Context(), access.project.ID)
	if err != nil {
		h.dbError(w, "get project quota", err)
		return
	}

	writeJSON(w, http.StatusOK, projectQuotaResponse(access.project.Name, &usage.Quota, usage.SizeBytes, usage.ArtifactCount))
}

// handlePutQuota sets the project ceiling. Only a global admin may: owners
// cannot raise their own ceiling. Omitted fields keep their stored value.
//
//	PUT /api/v1/projects/{project}/quota
func (h *ProjectsHandler) handlePutQuota(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionUpdate)
	if !ok {
		return
	}

	if !access.admin {
		writeError(w, http.StatusForbidden, "forbidden", "only an administrator can set a project quota")
		return
	}

	var body struct {
		MaxSizeBytes         *int64 `json:"max_size_bytes"`
		MaxArtifactCount     *int64 `json:"max_artifact_count"`
		WarnThresholdPercent *int   `json:"warn_threshold_percent"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if (body.MaxSizeBytes != nil && *body.MaxSizeBytes < 0) || (body.MaxArtifactCount != nil && *body.MaxArtifactCount < 0) {
		writeError(w, http.StatusBadRequest, "validation_failed", "limits must not be negative (0 means unlimited)")
		return
	}

	if body.WarnThresholdPercent != nil && (*body.WarnThresholdPercent < 0 || *body.WarnThresholdPercent > 100) {
		writeError(w, http.StatusBadRequest, "validation_failed", "warn_threshold_percent must be between 0 and 100")
		return
	}

	ctx := r.Context()

	cur, err := h.db.GetProjectQuota(ctx, access.project.ID)
	if err != nil {
		h.dbError(w, "get project quota", err)
		return
	}

	before := *cur

	if body.MaxSizeBytes != nil {
		cur.MaxSizeBytes = *body.MaxSizeBytes
	}

	if body.MaxArtifactCount != nil {
		cur.MaxArtifactCount = *body.MaxArtifactCount
	}

	if body.WarnThresholdPercent != nil {
		cur.WarnThresholdPercent = *body.WarnThresholdPercent
	}

	saved, err := h.db.SetProjectQuota(ctx, access.project.ID, cur.MaxSizeBytes, cur.MaxArtifactCount, cur.WarnThresholdPercent)
	if err != nil {
		h.dbError(w, "set project quota", err)
		return
	}

	h.emit(r, projectEvent{
		action: audit.ActionProjectQuotaChanged, projectID: &access.project.ID, resourceID: access.project.ID,
		message: fmt.Sprintf("project %s quota changed: max_size_bytes %d -> %d, max_artifact_count %d -> %d",
			access.project.Name, before.MaxSizeBytes, saved.MaxSizeBytes, before.MaxArtifactCount, saved.MaxArtifactCount),
	})

	usage, err := h.db.GetProjectUsage(ctx, access.project.ID)
	if err != nil {
		h.dbError(w, "get project usage", err)
		return
	}

	writeJSON(w, http.StatusOK, projectQuotaResponse(access.project.Name, saved, usage.SizeBytes, usage.ArtifactCount))
}
