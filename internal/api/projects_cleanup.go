// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// Project cleanup policies are defaults: one applies to every live hosted
// repository of the project that has no enabled policy of its own (the worker
// expands it per repository). Members read them; owners (and global admins)
// change them.

// emitCleanupPolicy records project.cleanup_policy_changed.
func (h *ProjectsHandler) emitCleanupPolicy(r *http.Request, project *models.Project, policyID, verb, name string) {
	h.emit(r, projectEvent{
		action: "project.cleanup_policy_changed", projectID: &project.ID,
		resourceType: "cleanup_policy", resourceID: policyID,
		message: "project " + project.Name + " cleanup policy " + name + " " + verb,
	})
}

// loadProjectPolicy loads the policy {id} of the project; a policy of another
// project or of a repository is 404. It writes the response and returns nil on
// failure.
func (h *ProjectsHandler) loadProjectPolicy(w http.ResponseWriter, r *http.Request, project *models.Project) *models.CleanupPolicy {
	policy, err := h.db.GetCleanupPolicy(r.Context(), urlparams.Vars(r)["id"])
	if err != nil {
		h.dbError(w, "get project cleanup policy", err)
		return nil
	}

	if policy == nil || policy.ProjectID != project.ID {
		writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
		return nil
	}

	return policy
}

// handleListCleanupPolicies lists the project's default cleanup policies.
//
//	GET /api/v1/projects/{project}/cleanup-policies
func (h *ProjectsHandler) handleListCleanupPolicies(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	policies, err := h.db.ListProjectCleanupPolicies(r.Context(), access.project.ID)
	if err != nil {
		h.dbError(w, "list project cleanup policies", err)
		return
	}

	result := make([]responses.CleanupPolicyResponse, 0, len(policies))
	for _, p := range policies {
		result = append(result, policyToMap(p))
	}

	writeJSON(w, http.StatusOK, map[string]any{"policies": result})
}

// handleCreateCleanupPolicy creates a project default cleanup policy.
//
//	POST /api/v1/projects/{project}/cleanup-policies
func (h *ProjectsHandler) handleCreateCleanupPolicy(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionUpdate)
	if !ok {
		return
	}

	var body struct {
		Name                    string `json:"name"`
		Enabled                 bool   `json:"enabled"`
		KeepLastN               *int   `json:"keep_last_n"`
		KeepSinceDays           *int   `json:"keep_since_days"`
		KeepDownloadedSinceDays *int   `json:"keep_downloaded_since_days"`
		NamePattern             string `json:"name_pattern"`
		ScheduleCron            string `json:"schedule_cron"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	cron := strings.TrimSpace(body.ScheduleCron)
	if cron == "" {
		cron = models.DefaultCleanupCron
	}

	policy := &models.CleanupPolicy{
		ProjectID:               access.project.ID,
		Name:                    body.Name,
		Enabled:                 body.Enabled,
		KeepLastN:               body.KeepLastN,
		KeepSinceDays:           body.KeepSinceDays,
		KeepDownloadedSinceDays: body.KeepDownloadedSinceDays,
		NamePattern:             body.NamePattern,
		ScheduleCron:            cron,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
	}

	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if err := h.db.CreateCleanupPolicy(r.Context(), policy); err != nil {
		h.dbError(w, "create project cleanup policy", err)
		return
	}

	h.emitCleanupPolicy(r, access.project, policy.ID, "created", policy.Name)

	writeJSON(w, http.StatusCreated, policyToMap(policy))
}

// handleUpdateCleanupPolicy partially updates a project default policy.
//
//	PUT /api/v1/projects/{project}/cleanup-policies/{id}
func (h *ProjectsHandler) handleUpdateCleanupPolicy(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionUpdate)
	if !ok {
		return
	}

	policy := h.loadProjectPolicy(w, r, access.project)
	if policy == nil {
		return
	}

	var body cleanupPolicyPatch
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	body.apply(policy)

	// Disabling must always work, even for a policy stored before validation.
	if !body.onlyDisables(policy) {
		if err := policy.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
	}

	if err := h.db.UpdateCleanupPolicy(r.Context(), policy); err != nil {
		h.dbError(w, "update project cleanup policy", err)
		return
	}

	h.emitCleanupPolicy(r, access.project, policy.ID, "updated", policy.Name)

	writeJSON(w, http.StatusOK, policyToMap(policy))
}

// handleDeleteCleanupPolicy deletes a project default policy.
//
//	DELETE /api/v1/projects/{project}/cleanup-policies/{id}
func (h *ProjectsHandler) handleDeleteCleanupPolicy(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionUpdate)
	if !ok {
		return
	}

	policy := h.loadProjectPolicy(w, r, access.project)
	if policy == nil {
		return
	}

	if err := h.db.DeleteCleanupPolicy(r.Context(), policy.ID); err != nil {
		h.dbError(w, "delete project cleanup policy", err)
		return
	}

	h.emitCleanupPolicy(r, access.project, policy.ID, "deleted", policy.Name)

	writeNoContent(w)
}
