// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

func (h *ProjectsHandler) handleListGroups(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	groups, err := h.db.ListProjectGroups(r.Context(), access.project.ID)
	if err != nil {
		h.dbError(w, "list project groups", err)
		return
	}

	result := make([]responses.ProjectGroupResponse, 0, len(groups))
	for _, g := range groups {
		result = append(result, projectGroupToResponse(g))
	}

	writeJSON(w, http.StatusOK, map[string]any{"groups": result})
}

// findGroup resolves the {group} URL segment: a group ID, or an exact group
// name. It writes the error response and returns nil when there is no match.
func (h *ProjectsHandler) findGroup(w http.ResponseWriter, r *http.Request) *models.Group {
	ref := urlparams.Vars(r)["group"]

	var (
		group *models.Group
		err   error
	)

	if _, parseErr := uuid.Parse(ref); parseErr == nil {
		group, err = h.db.GetGroupByID(r.Context(), ref)
	} else {
		group, err = h.db.GetGroupByName(r.Context(), ref)
	}

	if err != nil {
		h.dbError(w, "get group", err)
		return nil
	}

	if group == nil {
		writeError(w, http.StatusNotFound, "not_found", "group not found")
	}

	return group
}

func (h *ProjectsHandler) handleSetGroup(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionMembersManage)
	if !ok {
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if !validProjectGroupRole(req.Role) {
		writeError(w, http.StatusBadRequest, "validation_failed", projectGroupRoleMessage)
		return
	}

	group := h.findGroup(w, r)
	if group == nil {
		return
	}

	var previous string

	guard := access.guard(either, req.Role, "", "")
	recording := func(current string) error {
		previous = current
		return guard(current)
	}

	if h.writeChangeError(w, "set project group", h.db.ChangeProjectGroup(r.Context(), access.project.ID, group.ID, req.Role, access.userID(), recording)) {
		return
	}

	h.emitGroupSet(r, access, group, previous, req.Role)

	attached, err := h.db.GetProjectGroup(r.Context(), access.project.ID, group.ID)
	if err != nil || attached == nil {
		if err != nil {
			h.log.Errorw("get project group: db error", "error", err)
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, projectGroupToResponse(attached))
}

func (h *ProjectsHandler) emitGroupSet(r *http.Request, access *projectAccess, group *models.Group, previous, role string) {
	switch previous {
	case role:
	case "":
		h.emit(r, projectEvent{
			action: audit.ActionProjectGroupAdded, projectID: &access.project.ID, resourceType: "project_group", resourceID: group.ID,
			message: "group " + group.Name + " attached to project " + access.project.Name + " as " + role,
		})
	default:
		h.emit(r, projectEvent{
			action: audit.ActionProjectGroupRoleChanged, projectID: &access.project.ID, resourceType: "project_group", resourceID: group.ID,
			message: "group " + group.Name + " in project " + access.project.Name + ": " + previous + " -> " + role,
		})
	}
}

func (h *ProjectsHandler) handleRemoveGroup(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionMembersManage)
	if !ok {
		return
	}

	group := h.findGroup(w, r)
	if group == nil {
		return
	}

	var previous string

	guard := access.guard(mustExist, "", "group is not attached to the project", "")
	recording := func(current string) error {
		previous = current
		return guard(current)
	}

	if h.writeChangeError(w, "remove project group", h.db.ChangeProjectGroup(r.Context(), access.project.ID, group.ID, "", nil, recording)) {
		return
	}

	h.emit(r, projectEvent{
		action: audit.ActionProjectGroupRemoved, projectID: &access.project.ID, resourceType: "project_group", resourceID: group.ID,
		message: "group " + group.Name + " detached from project " + access.project.Name + " (was " + previous + ")",
	})

	writeNoContent(w)
}

func projectGroupToResponse(g *models.ProjectGroup) responses.ProjectGroupResponse {
	return responses.ProjectGroupResponse{
		ProjectID: g.ProjectID,
		GroupID:   g.GroupID,
		GroupName: g.GroupName,
		Role:      g.Role,
		CreatedAt: formatTime(g.CreatedAt),
	}
}
