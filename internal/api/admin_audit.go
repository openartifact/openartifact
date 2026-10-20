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
}

func (h *AdminAuditHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	page := queryIntParam(r, "page", 1, 1, 10000)
	perPage := queryIntParam(r, "per_page", 25, 1, 100)
	actor := r.URL.Query().Get("actor")
	action := r.URL.Query().Get("action")

	logs, total, err := h.db.ListAuditLogs(r.Context(), page, perPage, actor, action)
	if err != nil {
		h.log.Errorw("admin audit list error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	data := make([]any, 0, len(logs))
	for _, log := range logs {
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

	writeJSON(w, http.StatusOK, map[string]any{
		"data":     data,
		"page":     page,
		"per_page": perPage,
		"total":    total,
	})
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
