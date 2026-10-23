// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	activityDefaultPerPage = 25
	activityMaxPerPage     = 100
)

// handleListActivity returns the audit events of the project and of the
// repositories it owns to any member, newest first (project.token_* events only to maintainers and above). Keyset paging only
// (`cursor`, `next_cursor`, `has_more`): no row is ever counted. Client
// addresses are left out.
//
//	GET /api/v1/projects/{project}/activity
func (h *ProjectsHandler) handleListActivity(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	perPage := queryIntParam(r, "per_page", activityDefaultPerPage, 1, activityMaxPerPage)

	params := models.AuditListParams{
		ProjectID: access.project.ID,
		Actor:     r.URL.Query().Get("actor"),
		Action:    r.URL.Query().Get("action"),
		Limit:     perPage,
		NoTotal:   true,
		// Token events name tokens and their roles: maintainers and above only.
		HideProjectTokenEvents: !access.isMaintainer(),
	}

	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err := decodeAuditCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return
		}

		params.After = cursor
	}

	page, err := h.db.ListAuditLogs(r.Context(), params)
	if err != nil {
		h.dbError(w, "list project activity", err)
		return
	}

	resp := responses.ProjectActivityPage{
		Data:    make([]responses.ProjectActivityEntry, 0, len(page.Logs)),
		PerPage: perPage,
		HasMore: page.Next != nil,
	}

	for _, l := range page.Logs {
		resp.Data = append(resp.Data, activityEntry(l))
	}

	if page.Next != nil {
		resp.NextCursor = encodeAuditCursor(page.Next)
	}

	writeJSON(w, http.StatusOK, resp)
}

func activityEntry(l *models.AuditLog) responses.ProjectActivityEntry {
	var details struct {
		Message string `json:"message"`
		Actor   string `json:"actor"`
	}

	// A row whose details are not the {"message","actor"} object (the generic
	// request rows carry other keys) just has neither.
	_ = json.Unmarshal(l.DetailsJSON, &details)

	return responses.ProjectActivityEntry{
		ID:           l.ID,
		Timestamp:    formatTime(l.CreatedAt),
		Action:       l.Action,
		Actor:        details.Actor,
		ActorType:    l.ActorType,
		Message:      details.Message,
		ResourceType: l.ResourceType,
		ResourceID:   l.ResourceID,
		RepositoryID: l.RepositoryID,
	}
}

// handleExportActivity streams the project's activity as JSONL or CSV to any
// member, newest first, with the same visibility rules as the listing:
// project.token_* events only for maintainers and above, no client addresses.
//
//	GET /api/v1/projects/{project}/activity/export?format=jsonl|csv
func (h *ProjectsHandler) handleExportActivity(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	e := &auditExport{
		db:  h.db,
		log: h.log,
		params: models.AuditListParams{
			ProjectID:              access.project.ID,
			HideProjectTokenEvents: !access.isMaintainer(),
		},
		columns: projectActivityExportColumns(),
		row: func(l *models.AuditLog) auditExportRow {
			return newProjectActivityExportRow(l)
		},
		filePrefix: "openartifact-" + access.project.Slug + "-activity",
	}

	if e.format, ok = parseAuditExportQuery(w, r, &e.params); !ok {
		return
	}

	e.serve(w, r)
}
