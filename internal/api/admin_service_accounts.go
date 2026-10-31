// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const maxServiceAccountDescription = 255

// AdminServiceAccountsStore is the part of the database AdminServiceAccountsHandler needs.
type AdminServiceAccountsStore interface {
	CreateServiceAccount(ctx context.Context, name string, description *string, disabled bool) (*models.User, error)
	GetServiceAccount(ctx context.Context, id string) (*models.User, error)
	ListServiceAccounts(ctx context.Context, page, perPage int) ([]*models.User, int, error)
	UpdateServiceAccount(ctx context.Context, id string, description *string, disabled *bool) (*models.User, error)
	DeleteServiceAccount(ctx context.Context, id string) (bool, error)
}

// AdminServiceAccountsHandler manages service accounts: named non-human
// principals for automation. They hold project roles, group memberships and
// permission assignments like users do and own API tokens (through the
// /api/v1/users/{id}/tokens routes), but cannot sign in.
type AdminServiceAccountsHandler struct {
	db        AdminServiceAccountsStore
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminServiceAccountsHandler(
	db AdminServiceAccountsStore, log *logger.Logger, evaluator *auth.Evaluator,
) *AdminServiceAccountsHandler {
	return &AdminServiceAccountsHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminServiceAccountsHandler) Register(r chi.Router) {
	registerAdminCollection(r, "/api/v1/service-accounts", h.handleCreate, h.handleList, h.handleGet, h.handlePatch, h.handleDelete)
}

// registerAdminCollection wires the five routes of an administrator-managed
// collection: create and list on the collection path, get, patch and delete on
// the member path keyed by a UUID {id}.
func registerAdminCollection(r chi.Router, path string, create, list, get, patch, del http.HandlerFunc) {
	member := path + "/{id}"
	byID := r.With(requireUUIDParams("id"))

	r.Post(path, create)
	r.Get(path, list)
	byID.Get(member, get)
	byID.Patch(member, patch)
	byID.Delete(member, del)
}

type serviceAccountResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func serviceAccountToResponse(u *models.User) serviceAccountResponse {
	description := ""
	if u.FullName != nil {
		description = *u.FullName
	}

	return serviceAccountResponse{
		ID:          u.ID,
		Name:        u.Username,
		Description: description,
		Disabled:    u.Disabled,
		CreatedAt:   formatTime(u.CreatedAt),
		UpdatedAt:   formatTime(u.UpdatedAt),
	}
}

// validServiceAccountDescription reports whether a description is short enough
// and free of control characters.
func validServiceAccountDescription(s string) bool {
	return utf8.RuneCountInString(s) <= maxServiceAccountDescription &&
		!strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f })
}

func (h *AdminServiceAccountsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Disabled    bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	if !usernameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name must be 3-64 alphanumeric/dash/underscore characters")
		return
	}

	if !validServiceAccountDescription(body.Description) {
		writeError(w, http.StatusBadRequest, "validation_failed", "description must be at most 255 characters without control characters")
		return
	}

	saved, err := h.db.CreateServiceAccount(r.Context(), body.Name, &body.Description, body.Disabled)
	if err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a user or service account with this name already exists")
			return
		}

		h.log.Errorw("create service account failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionServiceAccountCreated, "service_account", saved.ID, "service account "+saved.Username+" created")

	writeJSON(w, http.StatusCreated, serviceAccountToResponse(saved))
}

func (h *AdminServiceAccountsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	page := queryIntParam(r, "page", 1, 1, 10000)
	perPage := queryIntParam(r, "per_page", 50, 1, 200)

	accounts, total, err := h.db.ListServiceAccounts(r.Context(), page, perPage)
	if err != nil {
		h.log.Errorw("list service accounts failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	data := make([]serviceAccountResponse, 0, len(accounts))
	for _, u := range accounts {
		data = append(data, serviceAccountToResponse(u))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data, "page": page, "per_page": perPage, "total": total})
}

func (h *AdminServiceAccountsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	account, err := h.db.GetServiceAccount(r.Context(), urlparams.Vars(r)["id"])
	if err != nil {
		h.log.Errorw("get service account failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if account == nil {
		writeError(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}

	writeJSON(w, http.StatusOK, serviceAccountToResponse(account))
}

func (h *AdminServiceAccountsHandler) handlePatch(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Description *string `json:"description"`
		Disabled    *bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	if body.Description != nil && !validServiceAccountDescription(*body.Description) {
		writeError(w, http.StatusBadRequest, "validation_failed", "description must be at most 255 characters without control characters")
		return
	}

	id := urlparams.Vars(r)["id"]

	account, err := h.db.UpdateServiceAccount(r.Context(), id, body.Description, body.Disabled)
	if err != nil {
		h.log.Errorw("update service account failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if account == nil {
		writeError(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}

	details := "service account " + account.Username + " updated"

	switch {
	case body.Disabled == nil:
	case *body.Disabled:
		details += " (disabled)"
	default:
		details += " (enabled)"
	}

	auditAdmin(r, audit.ActionServiceAccountUpdated, "service_account", account.ID, details)

	writeJSON(w, http.StatusOK, serviceAccountToResponse(account))
}

func (h *AdminServiceAccountsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	// The name is read first so the audit entry stays readable after the row is gone.
	account, err := h.db.GetServiceAccount(r.Context(), id)
	if err != nil {
		h.log.Errorw("get service account failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if account == nil {
		writeError(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}

	deleted, err := h.db.DeleteServiceAccount(r.Context(), id)
	if err != nil {
		h.log.Errorw("delete service account failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}

	auditAdmin(r, audit.ActionServiceAccountDeleted, "service_account", id, "service account "+account.Username+" deleted")

	writeNoContent(w)
}
