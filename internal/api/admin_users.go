// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,64}$`)

type AdminUsersHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminUsersHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminUsersHandler {
	return &AdminUsersHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminUsersHandler) Register(r chi.Router) {
	r.Post("/api/v1/users", h.handleCreate)
	r.Get("/api/v1/users", h.handleList)
	r.With(requireUUIDParams("id")).Get("/api/v1/users/{id}", h.handleGet)
	r.With(requireUUIDParams("id")).Patch("/api/v1/users/{id}", h.handlePatch)
	r.With(requireUUIDParams("id")).Delete("/api/v1/users/{id}", h.handleDelete)
}

func (h *AdminUsersHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Username           string  `json:"username"`
		Email              *string `json:"email"`
		FullName           *string `json:"full_name"`
		Password           *string `json:"password"`
		Disabled           bool    `json:"disabled"`
		MustChangePassword bool    `json:"must_change_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !usernameRe.MatchString(body.Username) {
		writeError(w, http.StatusBadRequest, "validation_failed", "username must be 3-64 alphanumeric/dash/underscore characters")
		return
	}

	if body.Email != nil && !strings.Contains(*body.Email, "@") {
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid email format")
		return
	}

	now := time.Now()

	user := &models.User{
		Username:           body.Username,
		Email:              body.Email,
		FullName:           body.FullName,
		Disabled:           body.Disabled,
		MustChangePassword: body.MustChangePassword,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if body.Password != nil {
		if !checkPasswordPolicy(w, *body.Password) {
			return
		}

		hashed, hashErr := auth.HashPassword(*body.Password)
		if hashErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "password hashing failed")
			return
		}

		user.PasswordHash = &hashed
	}

	saved, err := h.db.CreateUser(r.Context(), user)
	if err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a user with this username or email already exists")
			return
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, "user.created", "user", saved.ID, "user "+saved.Username+" created")

	writeJSON(w, http.StatusCreated, userToResponse(saved))
}

func (h *AdminUsersHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	page := queryIntParam(r, "page", 1, 1, 0)
	perPage := queryIntParam(r, "per_page", 50, 1, 200)

	users, total, err := h.db.ListUsers(r.Context(), page, perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(users))
	for _, u := range users {
		data = append(data, userToResponse(u))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":     data,
		"page":     page,
		"per_page": perPage,
		"total":    total,
	})
}

func (h *AdminUsersHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	user, err := h.db.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if user == nil || user.IsService() {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	writeJSON(w, http.StatusOK, userToResponse(user))
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *AdminUsersHandler) handlePatch(w http.ResponseWriter, r *http.Request) {
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return
	}

	// Self-service is for interactive sessions only: an API token (even one
	// owned by this user) must not be able to edit the account profile.
	isSelf := p != nil && p.UserID != nil && *p.UserID == id && !p.IsToken()

	if !isAdmin && !isSelf {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	user, err := h.db.GetUserByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if user == nil || user.IsService() {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	var body struct {
		Email              *string `json:"email"`
		FullName           *string `json:"full_name"`
		Disabled           *bool   `json:"disabled"`
		MustChangePassword *bool   `json:"must_change_password"`
		Password           *string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	// A non-admin editing their own record may only change full_name.
	// Privileged fields (disabled, must_change_password) are admin-only, and
	// the password is deliberately NOT self-editable here: changing it
	// requires proving knowledge of the current password, which is what
	// POST /api/v1/auth/change-password does.
	if !isAdmin && isSelf {
		// email is admin-only: SSO link_by_email trusts local emails, so a
		// self-set unverified address would let a user pre-claim a victim's
		// identity.
		if body.Disabled != nil || body.MustChangePassword != nil || body.Password != nil || body.Email != nil {
			writeError(w, http.StatusForbidden, "forbidden", "self-edit may only update full_name")
			return
		}
	}

	if isAdmin && body.Disabled != nil && *body.Disabled && p != nil && p.UserID != nil && *p.UserID == id {
		writeError(w, http.StatusBadRequest, "bad_request", "cannot disable your own account")
		return
	}

	if isAdmin && body.Password != nil && !checkPasswordPolicy(w, *body.Password) {
		return
	}

	revokeSessions := false

	var changed []string

	if body.Email != nil {
		if *body.Email != "" && !strings.Contains(*body.Email, "@") {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid email format")
			return
		}

		user.Email = body.Email

		changed = append(changed, "email")
	}

	if body.FullName != nil {
		user.FullName = body.FullName

		changed = append(changed, "full_name")
	}

	if isAdmin {
		if body.Disabled != nil {
			user.Disabled = *body.Disabled
			revokeSessions = revokeSessions || *body.Disabled

			changed = append(changed, "disabled="+strconv.FormatBool(*body.Disabled))
		}

		if body.MustChangePassword != nil {
			user.MustChangePassword = *body.MustChangePassword

			changed = append(changed, "must_change_password")
		}

		if body.Password != nil {
			newHash, err := auth.HashPassword(*body.Password)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "password hashing failed")
				return
			}

			user.PasswordHash = &newHash
			revokeSessions = true

			changed = append(changed, "password_reset")
		}
	}

	user.UpdatedAt = time.Now()
	if err := h.db.UpdateUser(ctx, user); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a user with this email already exists")
			return
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, "user.updated", "user", user.ID, "user "+user.Username+" updated: "+strings.Join(changed, ", "))

	// A password reset or a disable must cut off whoever still holds a
	// session. Failing to do so fails the request: the caller must know the
	// old sessions may still be alive (the update itself is idempotent, so
	// retrying is safe).
	if revokeSessions {
		if err := auth.RevokeAllSessions(ctx, h.db, user.ID); err != nil {
			h.log.Errorw("admin patch user: revoke sessions", "error", err, "user", user.ID)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to revoke sessions")

			return
		}
	}

	writeJSON(w, http.StatusOK, userToResponse(user))
}

func (h *AdminUsersHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	p := currentPrincipal(r)
	if p != nil && p.UserID != nil && *p.UserID == id {
		writeError(w, http.StatusBadRequest, "bad_request", "cannot delete your own account")
		return
	}

	user, err := h.db.GetUserByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if user == nil || user.IsService() {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	user.Disabled = true

	user.UpdatedAt = time.Now()
	if err := h.db.UpdateUser(ctx, user); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	auditAdmin(r, "user.disabled", "user", user.ID, "user "+user.Username+" deleted (disabled)")

	if err := auth.RevokeAllSessions(ctx, h.db, user.ID); err != nil {
		h.log.Errorw("admin delete user: revoke sessions", "error", err, "user", user.ID)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to revoke sessions")

		return
	}

	writeNoContent(w)
}

func queryIntParam(r *http.Request, name string, defaultVal, minVal, maxVal int) int { //nolint:unparam // minVal kept for API clarity and future flexibility
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}

	v, err := strconv.Atoi(s)
	if err != nil || v < minVal {
		return defaultVal
	}

	if maxVal > 0 && v > maxVal {
		return maxVal
	}

	return v
}
