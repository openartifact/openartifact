// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// changeError is a member or group change vetoed by a guard.
type changeError struct {
	status  int
	code    string
	message string
}

func (e *changeError) Error() string { return e.message }

// writeChangeError maps the outcome of a locked project change to a response.
// It returns false when err is nil.
func (h *ProjectsHandler) writeChangeError(w http.ResponseWriter, msg string, err error) bool {
	var ce *changeError

	switch {
	case err == nil:
		return false
	case errors.As(err, &ce):
		writeError(w, ce.status, ce.code, ce.message)
	case errors.Is(err, database.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	case errors.Is(err, database.ErrProjectOwnerLocked):
		writeError(w, http.StatusConflict, "conflict", "the project owner cannot be demoted or removed")
	case errors.Is(err, database.ErrLastProjectOwner):
		writeError(w, http.StatusConflict, "conflict", "cannot remove or demote the last owner")
	default:
		h.dbError(w, msg, err)
	}

	return true
}

// presence says whether the target of a change must already hold a role.
type presence int

const (
	mustNotExist presence = iota // adding
	mustExist                    // changing or removing
	either                       // attach-or-change
)

// guard returns the check that runs under the project lock: the target's
// presence, then the escalation rule. Owners may touch any role; maintainers
// only roles below maintainer, both the one the target holds and the one it
// gets. newRole "" means removal.
func (a *projectAccess) guard(want presence, newRole, notFound, conflict string) database.ProjectRoleGuard {
	return func(current string) error {
		switch {
		case want == mustNotExist && current != "":
			return &changeError{http.StatusConflict, "conflict", conflict}
		case want == mustExist && current == "":
			return &changeError{http.StatusNotFound, "not_found", notFound}
		}

		if (current != "" && !a.canGrant(current)) || (newRole != "" && !a.canGrant(newRole)) {
			return &changeError{http.StatusForbidden, "forbidden", "not allowed to grant or change this role"}
		}

		return nil
	}
}

func (h *ProjectsHandler) handleListMembers(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	members, err := h.db.ListProjectMembers(r.Context(), access.project.ID)
	if err != nil {
		h.dbError(w, "list project members", err)
		return
	}

	result := make([]responses.ProjectMemberResponse, 0, len(members))
	for _, m := range members {
		result = append(result, memberToResponse(m))
	}

	writeJSON(w, http.StatusOK, map[string]any{"members": result})
}

func (h *ProjectsHandler) handleAddMember(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionMembersManage)
	if !ok {
		return
	}

	var req struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if (req.UserID == "") == (req.Username == "") {
		writeError(w, http.StatusBadRequest, "validation_failed", "exactly one of user_id and username is required")
		return
	}

	if req.UserID != "" && !isUUIDString(req.UserID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id must be a UUID")
		return
	}

	if req.Role == "" {
		req.Role = models.ProjectRoleViewer
	}

	if !validProjectRole(req.Role) {
		writeError(w, http.StatusBadRequest, "validation_failed", projectRoleMessage)
		return
	}

	user, err := h.findUser(r, req.UserID, req.Username)
	if err != nil {
		h.dbError(w, "get user", err)
		return
	}

	// Service accounts are not people: they cannot be added by hand.
	if user == nil || user.IsService() {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	guard := access.guard(mustNotExist, req.Role, "", "user is already a member")
	if h.writeChangeError(w, "add project member", h.db.ChangeProjectMember(r.Context(), access.project.ID, user.ID, req.Role, guard)) {
		return
	}

	h.emit(r, projectEvent{
		action: "project.member_added", projectID: &access.project.ID, resourceType: "project_member", resourceID: user.ID,
		message: "user " + user.Username + " added to project " + access.project.Name + " as " + req.Role,
	})

	h.respondMember(w, r, access, user.ID, http.StatusCreated)
}

// memberPathID returns the {user_id} route parameter. A value that is not a
// UUID cannot name a member, so it is answered as an unknown member.
func memberPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := urlparams.Vars(r)["user_id"]
	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "member not found")
		return "", false
	}

	return id, true
}

func (h *ProjectsHandler) findUser(r *http.Request, id, username string) (*models.User, error) {
	if id != "" {
		return h.db.GetUserByID(r.Context(), id) //nolint:wrapcheck // logged by caller
	}

	return h.db.GetUserByUsername(r.Context(), username) //nolint:wrapcheck // logged by caller
}

func (h *ProjectsHandler) respondMember(w http.ResponseWriter, r *http.Request, access *projectAccess, userID string, status int) {
	member, err := h.db.GetProjectMember(r.Context(), access.project.ID, userID)
	if err != nil || member == nil {
		if err != nil {
			h.dbError(w, "get project member", err)
			return
		}

		writeError(w, http.StatusNotFound, "not_found", "member not found")

		return
	}

	writeJSON(w, status, memberToResponse(member))
}

func (h *ProjectsHandler) handleUpdateMember(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionMembersManage)
	if !ok {
		return
	}

	userID, ok := memberPathID(w, r)
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

	if !validProjectRole(req.Role) {
		writeError(w, http.StatusBadRequest, "validation_failed", projectRoleMessage)
		return
	}

	target, ok := h.existingMember(w, r, access, userID)
	if !ok {
		return
	}

	var previous string

	guard := access.guard(mustExist, req.Role, "member not found", "")
	recording := func(current string) error {
		previous = current
		return guard(current)
	}

	if h.writeChangeError(w, "update project member role", h.db.ChangeProjectMember(r.Context(), access.project.ID, userID, req.Role, recording)) {
		return
	}

	if previous != req.Role {
		h.emit(r, projectEvent{
			action: "project.member_role_changed", projectID: &access.project.ID, resourceType: "project_member", resourceID: userID,
			message: "user " + target.Username + " in project " + access.project.Name + ": " + previous + " -> " + req.Role,
		})
	}

	h.respondMember(w, r, access, userID, http.StatusOK)
}

// existingMember loads the member row, answering 404 when the user holds no
// direct membership (the guard re-checks under the lock).
func (h *ProjectsHandler) existingMember(w http.ResponseWriter, r *http.Request, access *projectAccess, userID string) (*models.ProjectMember, bool) {
	member, err := h.db.GetProjectMember(r.Context(), access.project.ID, userID)
	if err != nil {
		h.dbError(w, "get project member", err)
		return nil, false
	}

	if member == nil {
		writeError(w, http.StatusNotFound, "not_found", "member not found")
		return nil, false
	}

	return member, true
}

func (h *ProjectsHandler) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionMembersManage)
	if !ok {
		return
	}

	userID, ok := memberPathID(w, r)
	if !ok {
		return
	}

	target, ok := h.existingMember(w, r, access, userID)
	if !ok {
		return
	}

	guard := access.guard(mustExist, "", "member not found", "")
	if h.writeChangeError(w, "remove project member", h.db.ChangeProjectMember(r.Context(), access.project.ID, userID, "", guard)) {
		return
	}

	h.emit(r, projectEvent{
		action: "project.member_removed", projectID: &access.project.ID, resourceType: "project_member", resourceID: userID,
		message: "user " + target.Username + " removed from project " + access.project.Name + " (was " + target.Role + ")",
	})

	writeNoContent(w)
}

func memberToResponse(member *models.ProjectMember) responses.ProjectMemberResponse {
	return responses.ProjectMemberResponse{
		ID:        member.ID,
		ProjectID: member.ProjectID,
		UserID:    member.UserID,
		Username:  member.Username,
		Role:      member.Role,
		CreatedAt: formatTime(member.CreatedAt),
	}
}
