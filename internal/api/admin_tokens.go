// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type AdminTokensHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminTokensHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminTokensHandler {
	return &AdminTokensHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminTokensHandler) Register(r chi.Router) {
	r.Post("/api/v1/users/{id}/tokens", h.handleCreate)
	r.Get("/api/v1/users/{id}/tokens", h.handleList)
	r.Delete("/api/v1/users/{id}/tokens/{tokenId}", h.handleRevoke)
	r.Get("/api/v1/admin/tokens", h.handleAdminList)
	r.Delete("/api/v1/admin/tokens/{tokenId}", h.handleAdminRevoke)
}

// checkOwnerOrAdmin reports whether the caller may manage tokens owned by
// targetUserID: either they are that user (interactive session only — an API
// token must not manage the owner's tokens) or a global admin.
func (h *AdminTokensHandler) checkOwnerOrAdmin(r *http.Request, targetUserID string) (bool, error) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		return false, nil
	}

	if p.UserID != nil && *p.UserID == targetUserID && !p.IsToken() {
		return true, nil
	}

	return principalIsAdmin(r.Context(), p, h.evaluator)
}

func (h *AdminTokensHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	targetUserID := urlparams.Vars(r)["id"]

	ok, err := h.checkOwnerOrAdmin(r, targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return
	}

	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	issueToken(w, r, h.db, h.log, h.evaluator, targetUserID)
}

func (h *AdminTokensHandler) handleList(w http.ResponseWriter, r *http.Request) {
	targetUserID := urlparams.Vars(r)["id"]

	ok, err := h.checkOwnerOrAdmin(r, targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return
	}

	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	tokens, err := h.db.ListTokensByUser(r.Context(), targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(tokens))
	for _, t := range tokens {
		if t.RevokedAt == nil {
			data = append(data, tokenToResponse(t))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (h *AdminTokensHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	targetUserID := vars["id"]
	tokenID := vars["tokenId"]
	ctx := r.Context()

	ok, err := h.checkOwnerOrAdmin(r, targetUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return
	}

	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	token, err := h.db.GetTokenByID(ctx, tokenID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if token == nil {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}

	if token.UserID != targetUserID {
		writeError(w, http.StatusForbidden, "forbidden", "token does not belong to specified user")
		return
	}

	if err := h.db.RevokeToken(ctx, tokenID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	writeNoContent(w)
}

func (h *AdminTokensHandler) handleAdminList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	page := queryIntParam(r, "page", 1, 1, 10000)
	perPage := queryIntParam(r, "per_page", 25, 1, 100)

	tokens, total, err := h.db.ListAllTokens(r.Context(), page, perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(tokens))
	for _, t := range tokens {
		item := responses.AdminTokenResponse{
			TokenResponse: tokenToResponse(t),
			UserID:        t.UserID,
			ProjectRole:   t.ProjectRole,
		}

		if t.ProjectID != nil {
			item.Project = &t.ProjectName
			item.CreatedBy = nonEmptyString(t.CreatedByName)
		}

		data = append(data, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":     data,
		"page":     page,
		"per_page": perPage,
		"total":    total,
	})
}

func (h *AdminTokensHandler) handleAdminRevoke(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	tokenID := urlparams.Vars(r)["tokenId"]

	token, err := h.db.GetTokenByID(r.Context(), tokenID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if token == nil {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}

	if err := h.db.RevokeToken(r.Context(), tokenID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	writeNoContent(w)
}

const (
	maxTokenNameLength = 100
	maxTokenScopes     = 64
	// maxTokenLifetime bounds an explicit expires_at. Omitting expires_at
	// still issues a non-expiring token (the UI's "never" option).
	maxTokenLifetime = 2 * 365 * 24 * time.Hour
)

type tokenCreateBody struct {
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// validate checks the request shape and returns a user-facing message, or ""
// when the body is acceptable. It also normalises Name.
func (b *tokenCreateBody) validate(now time.Time) string {
	b.Name = strings.TrimSpace(b.Name)

	switch {
	case b.Name == "":
		return "name is required"
	case len(b.Name) > maxTokenNameLength:
		return "name must be at most 100 characters"
	case b.Kind != auth.TokenKindAccess && b.Kind != auth.TokenKindDeploy:
		return "kind must be 'access' or 'deploy'"
	case len(b.Scopes) > maxTokenScopes:
		return "too many scopes"
	}

	if slices.Contains(b.Scopes, "") {
		return "scopes must not contain empty values"
	}

	if b.ExpiresAt != nil {
		if !b.ExpiresAt.After(now) {
			return "expires_at must be in the future"
		}

		if b.ExpiresAt.After(now.Add(maxTokenLifetime)) {
			return "expires_at must be within 2 years"
		}
	}

	return ""
}

// authorizeTokenScopes verifies the requested scope IDs. Admins may name any
// existing permission target (the evaluator still intersects a token's scopes
// with its owner's targets, so a scope can only ever narrow access); everyone
// else may only name targets they hold themselves. A non-zero status means the
// request must be rejected with that status/code/message.
func authorizeTokenScopes(ctx context.Context, db database.Database, p *auth.Principal, isAdmin bool, scopes []string) (int, string, string) {
	if len(scopes) == 0 {
		return 0, "", ""
	}

	if isAdmin {
		for _, id := range scopes {
			target, err := db.GetPermissionTargetByID(ctx, id)
			if err != nil {
				return http.StatusInternalServerError, "internal_error", "database error"
			}

			if target == nil {
				return http.StatusBadRequest, "validation_failed", "unknown scope: " + id
			}
		}

		return 0, "", ""
	}

	if p.UserID == nil {
		return http.StatusForbidden, "forbidden", "requested scope not in caller's permissions"
	}

	callerTargets, err := db.GetPermissionTargetsForPrincipal(ctx, *p.UserID, p.GroupIDs)
	if err != nil {
		return http.StatusInternalServerError, "internal_error", "database error"
	}

	allowed := make(map[string]bool, len(callerTargets))
	for _, t := range callerTargets {
		allowed[t.ID] = true
	}

	for _, id := range scopes {
		if !allowed[id] {
			return http.StatusForbidden, "forbidden", "requested scope not in caller's permissions"
		}
	}

	return 0, "", ""
}

// issueToken is the single token-minting path for both the self-service and
// the per-user endpoints. The caller has already established that the
// principal may manage tokens for ownerID. Minting is refused for
// token-authenticated principals: a leaked token must not be able to spawn
// longer-lived or differently-scoped replacements for itself.
func issueToken(w http.ResponseWriter, r *http.Request, db database.Database, log *logger.Logger, evaluator *auth.Evaluator, ownerID string) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	if p.IsToken() {
		writeError(w, http.StatusForbidden, "forbidden", "API tokens cannot create tokens")
		return
	}

	ctx := r.Context()

	var body tokenCreateBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := body.validate(time.Now()); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	isAdmin, err := principalIsAdmin(ctx, p, evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return
	}

	if status, code, msg := authorizeTokenScopes(ctx, db, p, isAdmin, body.Scopes); status != 0 {
		writeError(w, status, code, msg)
		return
	}

	tokenString, record, err := auth.CreateToken(ctx, db, body.Kind, body.Name, ownerID, body.Scopes, body.ExpiresAt)
	if err != nil {
		log.Errorw("create token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "token creation failed")

		return
	}

	writeJSON(w, http.StatusCreated, responses.TokenCreateResponse{
		TokenResponse: tokenToResponse(record),
		Token:         tokenString,
	})
}

func tokenToResponse(t *models.Token) responses.TokenResponse {
	return responses.TokenResponse{
		ID:         t.ID,
		Name:       t.Name,
		Kind:       t.Kind,
		Scopes:     t.Scopes,
		ExpiresAt:  formatTimePtr(t.ExpiresAt),
		LastUsedAt: formatTimePtr(t.LastUsedAt),
		CreatedAt:  formatTime(t.CreatedAt),
	}
}

func nonEmptyString(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
