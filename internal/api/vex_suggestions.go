// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// VEXSuggestionStore is the part of the database VEXSuggestionHandler needs.
// Every call is scoped by the repository ID of the URL.
type VEXSuggestionStore interface {
	repoAccessStore
	ListVEXSuggestions(ctx context.Context, repoID, state string) ([]*models.VEXSuggestion, error)
	SetVEXSuggestionState(ctx context.Context, repoID, id, state, decidedBy string) error
	GetVEXSuggestion(ctx context.Context, repoID, id string) (*models.VEXSuggestion, error)
	AcceptVEXSuggestion(ctx context.Context, repoID, id, decidedBy string) (*models.VEXStatement, error)
}

// VEXSuggestionHandler lets a repository admin review the VEX decisions of the
// federated peers.
type VEXSuggestionHandler struct {
	repoAccess

	db VEXSuggestionStore
}

// NewVEXSuggestionHandler creates the handler.
func NewVEXSuggestionHandler(db VEXSuggestionStore, log *logger.Logger, evaluator *auth.Evaluator) *VEXSuggestionHandler {
	return &VEXSuggestionHandler{repoAccess: newRepoAccess(db, log, evaluator), db: db}
}

// RegisterAuthed mounts the routes on the authenticated API router.
func (h *VEXSuggestionHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/vex/suggestions", h.handleList)
	r.Post("/api/v1/repositories/{name}/vex/suggestions/{id}/accept", h.handleAccept)
	r.Post("/api/v1/repositories/{name}/vex/suggestions/{id}/dismiss", h.handleDismiss)
}

func vexSuggestionResponse(s *models.VEXSuggestion) responses.VEXSuggestionResponse {
	return responses.VEXSuggestionResponse{
		ID:              s.ID,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		PeerID:          s.PeerID,
		PeerName:        s.PeerName,
		PeerBaseURL:     s.PeerBaseURL,
		PeerUpdatedAt:   formatTimePtr(s.PeerUpdatedAt),
		State:           s.State,
		DecidedBy:       s.DecidedBy,
		DecidedAt:       formatTimePtr(s.DecidedAt),
		LocalStatus:     s.LocalStatus,
		LocalSourceKind: s.LocalSourceKind,
		CreatedAt:       formatTime(s.CreatedAt),
		UpdatedAt:       formatTime(s.UpdatedAt),
	}
}

func (h *VEXSuggestionHandler) handleList(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "list vex suggestions")
	if !ok {
		return
	}

	state := r.URL.Query().Get("state")

	switch state {
	case "":
		state = models.VEXSuggestionPending
	case "all":
		state = ""
	case models.VEXSuggestionPending, models.VEXSuggestionAccepted, models.VEXSuggestionDismissed:
	default:
		writeError(w, http.StatusBadRequest, "validation_failed", "state must be one of: pending, accepted, dismissed, all")
		return
	}

	items, err := h.db.ListVEXSuggestions(r.Context(), repo.ID, state)
	if err != nil {
		h.dbError(w, "list vex suggestions: db error", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"suggestions": mapList(items, vexSuggestionResponse)})
}

// suggestionID returns the {id} of the URL; false (404 written) when it is no UUID.
func suggestionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := urlparams.Vars(r)["id"]
	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "VEX suggestion not found")
		return "", false
	}

	return id, true
}

func principalName(r *http.Request) string {
	if p := currentPrincipal(r); p != nil {
		return p.Username
	}

	return ""
}

// handleAccept records the peer's decision as the repository's VEX statement,
// replacing whatever was there, and answers the statement.
func (h *VEXSuggestionHandler) handleAccept(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "accept vex suggestion")
	if !ok {
		return
	}

	id, ok := suggestionID(w, r)
	if !ok {
		return
	}

	stmt, err := h.db.AcceptVEXSuggestion(r.Context(), repo.ID, id, principalName(r))
	if errors.Is(err, database.ErrVEXSuggestionNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "VEX suggestion not found")
		return
	}

	if err != nil {
		h.dbError(w, "accept vex suggestion: db error", err)
		return
	}

	auditRepository(r, repo, audit.ActionVEXSuggestionAccepted, "vex_suggestion", id,
		"VEX decision of peer "+stmt.SourceName+" on "+stmt.VulnID+" for "+stmt.ArtifactID+" accepted: "+stmt.Status)
	writeJSON(w, http.StatusOK, vexResponse(stmt))
}

func (h *VEXSuggestionHandler) handleDismiss(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "dismiss vex suggestion")
	if !ok {
		return
	}

	id, ok := suggestionID(w, r)
	if !ok {
		return
	}

	err := h.db.SetVEXSuggestionState(r.Context(), repo.ID, id, models.VEXSuggestionDismissed, principalName(r))
	if errors.Is(err, database.ErrVEXSuggestionNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "VEX suggestion not found")
		return
	}

	if err != nil {
		h.dbError(w, "dismiss vex suggestion: db error", err)
		return
	}

	s, err := h.db.GetVEXSuggestion(r.Context(), repo.ID, id)
	if err != nil {
		h.dbError(w, "dismiss vex suggestion: db error", err)
		return
	}

	auditRepository(r, repo, audit.ActionVEXSuggestionDismissed, "vex_suggestion", id,
		"VEX decision of peer "+s.PeerName+" on "+s.VulnID+" for "+s.ArtifactID+" dismissed")
	writeJSON(w, http.StatusOK, vexSuggestionResponse(s))
}
