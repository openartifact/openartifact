// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type AdminAuditHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminAuditHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminAuditHandler {
	return &AdminAuditHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminAuditHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/audit", h.handleList)
	r.Get("/api/v1/admin/audit/export", h.handleExport)
}

// handleExport streams the whole audit log (or the slice selected by actor,
// action, since and until) as JSONL or CSV, newest first.
//
//	GET /api/v1/admin/audit/export?format=jsonl|csv
func (h *AdminAuditHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	e := &auditExport{
		db:  h.db,
		log: h.log,
		row: func(l *models.AuditLog) auditExportRow {
			return newAdminAuditExportRow(l)
		},
		columns:    adminAuditExportColumns(),
		filePrefix: "openartifact-audit",
	}

	var ok bool

	if e.format, ok = parseAuditExportQuery(w, r, &e.params); !ok {
		return
	}

	e.serve(w, r)
}

func (h *AdminAuditHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	page := queryIntParam(r, "page", 1, 1, 10000)
	perPage := queryIntParam(r, "per_page", 25, 1, 100)

	params := models.AuditListParams{
		Actor:  r.URL.Query().Get("actor"),
		Action: r.URL.Query().Get("action"),
		Limit:  perPage,
		Offset: (page - 1) * perPage,
	}

	// A cursor parameter switches to keyset paging: no offset scan and no
	// total. An empty value starts at the newest entry.
	keyset := r.URL.Query().Has("cursor")
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err := decodeAuditCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return
		}

		params.After = cursor
	}

	if keyset {
		params.Offset = 0
		params.NoTotal = true
	}

	result, err := h.db.ListAuditLogs(r.Context(), params)
	if err != nil {
		h.log.Errorw("admin audit list error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(result.Logs))
	for _, log := range result.Logs {
		details := parseDetailsJSON(log.DetailsJSON)
		data = append(data, map[string]any{
			"id":            log.ID,
			"timestamp":     log.CreatedAt,
			"action":        log.Action,
			"actor":         extractActor(details),
			"target":        log.ResourceID,
			"details":       details,
			"ip":            log.IPAddress,
			"repository_id": log.RepositoryID,
			"resource_type": log.ResourceType,
			"resource_id":   log.ResourceID,
		})
	}

	resp := map[string]any{
		"data":     data,
		"per_page": perPage,
		"has_more": result.Next != nil,
	}

	if result.Next != nil {
		resp["next_cursor"] = encodeAuditCursor(result.Next)
	}

	if !keyset {
		resp["page"] = page
		resp["total"] = result.Total
		resp["total_exact"] = result.TotalExact
	}

	writeJSON(w, http.StatusOK, resp)
}

// parseDetailsJSON safely parses a JSON string into a map, falling back to raw string.
func parseDetailsJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil
	}
	var details map[string]any
	if err := json.Unmarshal(raw, &details); err == nil {
		// If the message field is itself JSON, parse it too
		if msg, ok := details["message"].(string); ok && strings.HasPrefix(msg, "{") {
			var nested map[string]any
			if json.Unmarshal([]byte(msg), &nested) == nil {
				details["message"] = nested
			}
		}
		return details
	}
	return string(raw)
}

// extractActor pulls the human-readable actor name from parsed details JSON.
func extractActor(details any) string {
	m, ok := details.(map[string]any)
	if !ok {
		return ""
	}
	if a, ok := m["actor"].(string); ok && a != "" {
		return a
	}
	return ""
}
