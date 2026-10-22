// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
)

// handleListRepositories lists the project's live repositories to any member
// (viewer and up) and to global admins.
func (h *ProjectsHandler) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	q := r.URL.Query()
	page := min(parseIntMin(q.Get("page"), 1, 1), maxListPage)
	limit := parseIntClamp(q.Get("limit"), 25, 1, 100)

	repos, total, err := h.db.ListProjectRepositories(r.Context(), access.project.ID, page, limit)
	if err != nil {
		h.dbError(w, "list project repositories", err)
		return
	}

	result := make([]responses.RepositoryResponse, 0, len(repos))

	for _, repo := range repos {
		resp := RepositoryToResponse(repo)
		resp.Project = access.project.Name
		result = append(result, resp)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"repositories": result,
		"total":        total,
		"page":         page,
		"limit":        limit,
	})
}

// projectSettingsRequest is the body of PUT /api/v1/admin/projects/settings
// and the shape of its response.
type projectSettingsRequest struct {
	SelfCreate bool `json:"self_create"`
}

func (h *ProjectsHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	p, ok := h.caller(w, r)
	if !ok {
		return false
	}

	if !h.isAdmin(r, p) {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return false
	}

	return true
}

func (h *ProjectsHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	on, err := h.settings.SelfCreate(r.Context())
	if err != nil {
		h.dbError(w, "read projects.self_create", err)
		return
	}

	writeJSON(w, http.StatusOK, projectSettingsRequest{SelfCreate: on})
}

func (h *ProjectsHandler) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	var req projectSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if err := h.settings.SetSelfCreate(r.Context(), req.SelfCreate); err != nil {
		h.dbError(w, "write projects.self_create", err)
		return
	}

	state := "off"
	if req.SelfCreate {
		state = "on"
	}

	h.emit(r, projectEvent{
		action: audit.ActionSystemSettingChanged, resourceType: "system_setting", resourceID: auth.SettingProjectsSelfCreate,
		message: "projects.self_create set " + state,
	})

	writeJSON(w, http.StatusOK, req)
}
