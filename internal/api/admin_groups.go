// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

var groupNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{2,64}$`)

type AdminGroupsHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminGroupsHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminGroupsHandler {
	return &AdminGroupsHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminGroupsHandler) Register(r chi.Router) {
	r.Post("/api/v1/groups", h.handleCreate)
	r.Get("/api/v1/groups", h.handleList)
	r.With(requireUUIDParams("id")).Get("/api/v1/groups/{id}", h.handleGet)
	r.With(requireUUIDParams("id")).Patch("/api/v1/groups/{id}", h.handlePatch)
	r.With(requireUUIDParams("id")).Delete("/api/v1/groups/{id}", h.handleDelete)
	r.With(requireUUIDParams("id", "userId")).Post("/api/v1/groups/{id}/members/{userId}", h.handleAddMember)
	r.With(requireUUIDParams("id", "userId")).Delete("/api/v1/groups/{id}/members/{userId}", h.handleRemoveMember)
	r.With(requireUUIDParams("id")).Get("/api/v1/groups/{id}/members", h.handleListMembers)
	r.With(requireUUIDParams("id")).Put("/api/v1/groups/{id}/members", h.handleSetMembers)
	r.With(requireUUIDParams("id")).Get("/api/v1/users/{id}/groups", h.handleListUserGroups)
}

func (h *AdminGroupsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !groupNameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "group name must be 2-64 alphanumeric/dash/underscore characters")
		return
	}

	now := time.Now()
	group := &models.Group{
		Name:        body.Name,
		Description: body.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	saved, err := h.db.CreateGroup(r.Context(), group)
	if err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a group with this name already exists")
			return
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionGroupCreated, "group", saved.ID, "group "+saved.Name+" created")

	writeJSON(w, http.StatusCreated, groupToResponse(saved))
}

func (h *AdminGroupsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	groups, err := h.db.ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(groups))
	for _, g := range groups {
		data = append(data, groupToResponse(g))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (h *AdminGroupsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	group, err := h.db.GetGroupByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if group == nil {
		writeError(w, http.StatusNotFound, "not_found", "group not found")
		return
	}

	writeJSON(w, http.StatusOK, groupToResponse(group))
}

func (h *AdminGroupsHandler) handlePatch(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	group, err := h.db.GetGroupByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if group == nil {
		writeError(w, http.StatusNotFound, "not_found", "group not found")
		return
	}

	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.Name != nil {
		if !groupNameRe.MatchString(*body.Name) {
			writeError(w, http.StatusBadRequest, "validation_failed", "group name must be 2-64 alphanumeric/dash/underscore characters")
			return
		}

		group.Name = *body.Name
	}

	if body.Description != nil {
		group.Description = body.Description
	}

	group.UpdatedAt = time.Now()
	if err := h.db.UpdateGroup(ctx, group); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a group with this name already exists")
			return
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionGroupUpdated, "group", group.ID, "group "+group.Name+" updated")

	writeJSON(w, http.StatusOK, groupToResponse(group))
}

func (h *AdminGroupsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]
	if err := h.db.DeleteGroup(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	auditAdmin(r, audit.ActionGroupDeleted, "group", id, "group deleted")

	writeNoContent(w)
}

func (h *AdminGroupsHandler) handleAddMember(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	vars := urlparams.Vars(r)
	if err := h.db.AddUserToGroup(r.Context(), vars["userId"], vars["id"]); err != nil {
		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusNotFound, "not_found", "user or group not found")
			return
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionGroupMemberAdded, "group", vars["id"], "user "+vars["userId"]+" added to group")

	writeNoContent(w)
}

func (h *AdminGroupsHandler) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	vars := urlparams.Vars(r)
	if err := h.db.RemoveUserFromGroup(r.Context(), vars["userId"], vars["id"]); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	auditAdmin(r, audit.ActionGroupMemberRemoved, "group", vars["id"], "user "+vars["userId"]+" removed from group")

	writeNoContent(w)
}

func (h *AdminGroupsHandler) handleListMembers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	users, err := h.db.ListUsersByGroup(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(users))
	for _, u := range users {
		data = append(data, userToResponse(u))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (h *AdminGroupsHandler) handleSetMembers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	group, err := h.db.GetGroupByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if group == nil {
		writeError(w, http.StatusNotFound, "not_found", "group not found")
		return
	}

	var body struct {
		Members []string `json:"members"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	for _, m := range body.Members {
		if !isUUIDString(m) {
			writeError(w, http.StatusBadRequest, "validation_failed", "members must be user ids")
			return
		}
	}

	// Get current members
	currentUsers, err := h.db.ListUsersByGroup(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	// Build sets
	currentSet := make(map[string]bool, len(currentUsers))
	for _, u := range currentUsers {
		currentSet[u.ID] = true
	}

	desiredSet := make(map[string]bool, len(body.Members))
	for _, m := range body.Members {
		desiredSet[m] = true
	}

	// Remove members not in desired set
	for _, u := range currentUsers {
		if !desiredSet[u.ID] {
			if err := h.db.RemoveUserFromGroup(ctx, u.ID, id); err != nil {
				h.log.Errorw("set members: remove failed", "error", err, "user", u.ID)
				writeError(w, http.StatusInternalServerError, "internal_error", "database error")

				return
			}
		}
	}

	// Add members not in current set
	for _, m := range body.Members {
		if !currentSet[m] {
			if err := h.db.AddUserToGroup(ctx, m, id); err != nil {
				h.log.Errorw("set members: add failed", "error", err, "user", m)
				writeError(w, http.StatusInternalServerError, "internal_error", "database error")

				return
			}
		}
	}

	auditAdmin(r, audit.ActionGroupMembersSet, "group", id, "group membership replaced: "+strconv.Itoa(len(desiredSet))+" members")

	writeNoContent(w)
}

func (h *AdminGroupsHandler) handleListUserGroups(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	groups, err := h.db.ListGroupsByUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(groups))
	for _, g := range groups {
		data = append(data, groupToResponse(g))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func groupToResponse(g *models.Group) responses.GroupResponse {
	return responses.GroupResponse{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		CreatedAt:   formatTime(g.CreatedAt),
		UpdatedAt:   formatTime(g.UpdatedAt),
	}
}
