// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strings"
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

//nolint:gochecknoglobals // immutable lookup table
var validActions = map[string]bool{
	"read": true, "write": true, "delete": true, "manage": true, "annotate": true,
}

type AdminPermissionsHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminPermissionsHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminPermissionsHandler {
	return &AdminPermissionsHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminPermissionsHandler) Register(r chi.Router) {
	r.Post("/api/v1/permission-targets", h.handleCreate)
	r.Get("/api/v1/permission-targets", h.handleList)
	r.With(requireUUIDParams("id")).Get("/api/v1/permission-targets/{id}", h.handleGet)
	r.With(requireUUIDParams("id")).Patch("/api/v1/permission-targets/{id}", h.handlePatch)
	r.With(requireUUIDParams("id")).Delete("/api/v1/permission-targets/{id}", h.handleDelete)
	r.With(requireUUIDParams("id")).Post("/api/v1/permission-targets/{id}/assignments", h.handleAssign)
	r.With(requireUUIDParams("id")).Delete("/api/v1/permission-targets/{id}/assignments", h.handleUnassign)
}

func (h *AdminPermissionsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Name             string   `json:"name"`
		RepositoryFilter string   `json:"repository_filter"`
		PathPattern      string   `json:"path_pattern"`
		Actions          []string `json:"actions"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	now := time.Now()
	target := &models.PermissionTarget{
		Name:             strings.TrimSpace(body.Name),
		RepositoryFilter: body.RepositoryFilter,
		PathPattern:      body.PathPattern,
		Actions:          body.Actions,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if msg := validatePermissionTarget(target); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if existing, err := h.db.GetPermissionTargetByName(r.Context(), target.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	} else if existing != nil {
		writeError(w, http.StatusConflict, "conflict", "a permission target with this name already exists")
		return
	}

	saved, err := h.db.CreatePermissionTarget(r.Context(), target)
	if err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a permission target with this name already exists")
			return
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionPermissionTargetObjCreated, "permission_target", saved.ID, "permission target "+saved.Name+" created")

	writeJSON(w, http.StatusCreated, permTargetToResponse(saved))
}

func (h *AdminPermissionsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	targets, err := h.db.ListPermissionTargets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(targets))
	for _, t := range targets {
		data = append(data, permTargetToResponse(t))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (h *AdminPermissionsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	target, err := h.db.GetPermissionTargetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if target == nil {
		writeError(w, http.StatusNotFound, "not_found", "permission target not found")
		return
	}

	writeJSON(w, http.StatusOK, permTargetToResponse(target))
}

func (h *AdminPermissionsHandler) handlePatch(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	target, err := h.db.GetPermissionTargetByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if target == nil {
		writeError(w, http.StatusNotFound, "not_found", "permission target not found")
		return
	}

	var body struct {
		Name             *string  `json:"name"`
		RepositoryFilter *string  `json:"repository_filter"`
		PathPattern      *string  `json:"path_pattern"`
		Actions          []string `json:"actions"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	// Validate the merged result, not just the supplied fields, so a PATCH can
	// never leave behind a target that would not have passed creation.
	merged := *target

	if body.Name != nil {
		merged.Name = strings.TrimSpace(*body.Name)
	}

	if body.RepositoryFilter != nil {
		merged.RepositoryFilter = *body.RepositoryFilter
	}

	if body.PathPattern != nil {
		merged.PathPattern = *body.PathPattern
	}

	if body.Actions != nil {
		merged.Actions = body.Actions
	}

	if msg := validatePermissionTarget(&merged); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if merged.Name != target.Name {
		existing, err := h.db.GetPermissionTargetByName(ctx, merged.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")
			return
		}

		if existing != nil && existing.ID != merged.ID {
			writeError(w, http.StatusConflict, "conflict", "a permission target with this name already exists")
			return
		}
	}

	merged.UpdatedAt = time.Now()
	if err := h.db.UpdatePermissionTarget(ctx, &merged); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a permission target with this name already exists")
			return
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionPermissionTargetObjUpdated, "permission_target", merged.ID, "permission target "+merged.Name+" updated")

	writeJSON(w, http.StatusOK, permTargetToResponse(&merged))
}

func (h *AdminPermissionsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]
	if err := h.db.DeletePermissionTarget(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	auditAdmin(r, audit.ActionPermissionTargetObjDeleted, "permission_target", id, "permission target deleted")

	writeNoContent(w)
}

func (h *AdminPermissionsHandler) handleAssign(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	var body struct {
		PrincipalType string `json:"principal_type"`
		PrincipalID   string `json:"principal_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.PrincipalType != "user" && body.PrincipalType != "group" {
		writeError(w, http.StatusBadRequest, "validation_failed", "principal_type must be 'user' or 'group'")
		return
	}

	if !isUUIDString(body.PrincipalID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "principal_id must be a user or group id")
		return
	}

	ctx := r.Context()

	if !h.principalAndTargetExist(w, r, id, body.PrincipalType, body.PrincipalID) {
		return
	}

	assignment := &models.PermissionAssignment{
		PermissionTargetID: id,
		PrincipalType:      body.PrincipalType,
		PrincipalID:        body.PrincipalID,
	}
	if err := h.db.CreatePermissionAssignment(ctx, assignment); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	auditAdmin(r, audit.ActionPermissionTargetObjAssigned, "permission_target", id, "assigned to "+body.PrincipalType+" "+body.PrincipalID)

	writeNoContent(w)
}

func (h *AdminPermissionsHandler) handleUnassign(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]
	principalType := r.URL.Query().Get("principal_type")
	principalID := r.URL.Query().Get("principal_id")

	if principalType != "user" && principalType != "group" {
		writeError(w, http.StatusBadRequest, "validation_failed", "principal_type must be 'user' or 'group'")
		return
	}

	if !isUUIDString(principalID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "principal_id must be a user or group id")
		return
	}

	if err := h.db.DeletePermissionAssignment(r.Context(), id, principalType, principalID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	auditAdmin(r, audit.ActionPermissionTargetObjUnassigned, "permission_target", id, "unassigned from "+principalType+" "+principalID)

	writeNoContent(w)
}

// principalAndTargetExist writes a 404 and returns false when the permission
// target or the user/group the assignment refers to does not exist.
func (h *AdminPermissionsHandler) principalAndTargetExist(w http.ResponseWriter, r *http.Request, targetID, principalType, principalID string) bool {
	ctx := r.Context()

	target, err := h.db.GetPermissionTargetByID(ctx, targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return false
	}

	if target == nil {
		writeError(w, http.StatusNotFound, "not_found", "permission target not found")
		return false
	}

	if principalType == "user" {
		user, err := h.db.GetUserByID(ctx, principalID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")
			return false
		}

		if user == nil || user.IsService() {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return false
		}

		return true
	}

	group, err := h.db.GetGroupByID(ctx, principalID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return false
	}

	if group == nil {
		writeError(w, http.StatusNotFound, "not_found", "group not found")
		return false
	}

	return true
}

const maxPermissionNameLength = 128

// validatePermissionTarget checks a fully-populated target (create body or the
// merged result of a PATCH) and returns a user-facing message, or "" when it is
// acceptable. Patterns are compiled with auth.CompilePattern so what is
// accepted here is exactly what the evaluator enforces; in particular an empty
// pattern (which would match everything) is refused.
func validatePermissionTarget(t *models.PermissionTarget) string {
	if t.Name == "" {
		return "name is required"
	}

	if len(t.Name) > maxPermissionNameLength {
		return "name must be at most 128 characters"
	}

	for _, p := range []struct{ field, value string }{
		{"repository_filter", t.RepositoryFilter},
		{"path_pattern", t.PathPattern},
	} {
		if _, err := auth.CompilePattern(p.value); err != nil {
			if errors.Is(err, auth.ErrEmptyPattern) {
				return p.field + " is required (use '*' to match everything)"
			}

			return "invalid " + p.field + " regex"
		}
	}

	return validateActions(t.Actions)
}

func validateActions(actions []string) string {
	if len(actions) == 0 {
		return "actions must contain at least one of read, write, delete, manage, annotate"
	}

	for _, a := range actions {
		if !validActions[a] {
			return "invalid action: " + a + "; must be one of read, write, delete, manage, annotate"
		}
	}

	return ""
}

func permTargetToResponse(t *models.PermissionTarget) responses.PermissionTargetResponse {
	return responses.PermissionTargetResponse{
		ID:               t.ID,
		Name:             t.Name,
		RepositoryFilter: t.RepositoryFilter,
		PathPattern:      t.PathPattern,
		Actions:          t.Actions,
		CreatedAt:        formatTime(t.CreatedAt),
		UpdatedAt:        formatTime(t.UpdatedAt),
	}
}
