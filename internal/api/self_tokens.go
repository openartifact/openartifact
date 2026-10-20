// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type SelfTokensHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewSelfTokensHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *SelfTokensHandler {
	return &SelfTokensHandler{db: db, log: log, evaluator: evaluator}
}

func (h *SelfTokensHandler) Register(r chi.Router) {
	r.Get("/api/v1/auth/tokens", h.handleList)
	r.Post("/api/v1/auth/tokens", h.handleCreate)
	r.Delete("/api/v1/auth/tokens/{id}", h.handleRevoke)
}

func (h *SelfTokensHandler) handleList(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	tokens, err := h.db.ListTokensByUser(r.Context(), *p.UserID)
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

func (h *SelfTokensHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	issueToken(w, r, h.db, h.log, h.evaluator, *p.UserID)
}

func (h *SelfTokensHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	if p.IsToken() {
		writeError(w, http.StatusForbidden, "forbidden", "API tokens cannot revoke tokens")
		return
	}

	tokenID := urlparams.Vars(r)["id"]
	ctx := r.Context()

	token, err := h.db.GetTokenByID(ctx, tokenID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if token == nil {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}

	if token.UserID != *p.UserID {
		writeError(w, http.StatusForbidden, "forbidden", "token does not belong to current user")
		return
	}

	if err := h.db.RevokeToken(ctx, tokenID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	writeNoContent(w)
}
