// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// RepositoryActivityHandler serves the audit feed of one repository.
type RepositoryActivityHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewRepositoryActivityHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *RepositoryActivityHandler {
	return &RepositoryActivityHandler{db: db, log: log, evaluator: evaluator}
}

func (h *RepositoryActivityHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/activity", h.handleList)
}

// handleList returns the audit events recorded against one repository, newest
// first, with actor and subject names resolved. package_name and version keep
// only the events that record that package version (deletions, restores and
// purges do; events without a package never match). Keyset paging only
// (`cursor`, `next_cursor`, `has_more`): no row is counted. Client addresses
// are left out. Global admins and the repository's project maintainers only.
//
//	GET /api/v1/repositories/{name}/activity?package_name=&version=&actor=&action=&cursor=&limit=
func (h *RepositoryActivityHandler) handleList(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]

	if !requireRepoAdmin(w, r, h.evaluator, name) {
		return
	}

	repo, err := h.db.GetRepositoryByName(r.Context(), name)
	if err != nil {
		h.log.Errorw("repository activity: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	q := r.URL.Query()
	limit := queryIntParam(r, "limit", activityDefaultPerPage, 1, activityMaxPerPage)

	params := models.AuditListParams{
		RepositoryID:   repo.ID,
		PackageName:    q.Get("package_name"),
		PackageVersion: q.Get("version"),
		Actor:          q.Get("actor"),
		Action:         q.Get("action"),
		Limit:          limit,
		NoTotal:        true,
	}

	if raw := q.Get("cursor"); raw != "" {
		cursor, err := decodeAuditCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return
		}

		params.After = cursor
	}

	page, err := h.db.ListAuditLogs(r.Context(), params)
	if err != nil {
		h.log.Errorw("repository activity: list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, activityPage(r.Context(), h.db, h.log, page, limit))
}
