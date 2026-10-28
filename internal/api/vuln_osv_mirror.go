// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/osv"
)

// osvStateLister is the part of the database the status endpoint reads.
type osvStateLister interface {
	ListOSVMirrorStates(ctx context.Context) ([]models.OSVMirrorState, error)
}

// handleOSVMirrorStatus reports the local OSV mirror: its schedule, the import
// state of every configured ecosystem and the attribution of the data.
func (h *VulnHandler) handleOSVMirrorStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	cfg := h.osvMirror
	resp := responses.OSVMirrorStatusResponse{
		Enabled:         cfg.Enabled,
		SourceURL:       redactedURL(cfg.BaseURL),
		IntervalSeconds: int(cfg.Interval / time.Second),
		Ecosystems:      []responses.OSVMirrorEcosystemResponse{},
		Attribution:     responses.OSVAttributionResponse{Source: "OSV", License: "CC-BY-4.0", URL: "https://osv.dev"},
	}

	if !cfg.Enabled {
		writeJSON(w, http.StatusOK, resp)

		return
	}

	states := map[string]models.OSVMirrorState{}

	if store, ok := h.db.(osvStateLister); ok {
		list, err := store.ListOSVMirrorStates(r.Context())
		if err != nil {
			h.log.Errorw("osv mirror status: db error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		for _, st := range list {
			states[st.Ecosystem] = st
		}
	}

	for _, eco := range osvEcosystemsOrDefault(cfg.Ecosystems) {
		resp.Ecosystems = append(resp.Ecosystems, osvEcosystemToResponse(eco, states[eco]))
	}

	writeJSON(w, http.StatusOK, resp)
}

func osvEcosystemsOrDefault(configured []string) []string {
	if len(configured) == 0 {
		return osv.Ecosystems()
	}

	return configured
}

func osvEcosystemToResponse(eco string, st models.OSVMirrorState) responses.OSVMirrorEcosystemResponse {
	out := responses.OSVMirrorEcosystemResponse{
		Ecosystem:          eco,
		Populated:          st.HighWater != nil,
		Advisories:         st.Advisories,
		HighWater:          rfc3339Ptr(st.HighWater),
		LastCheckedAt:      rfc3339Ptr(st.LastCheckedAt),
		LastImportedAt:     rfc3339Ptr(st.LastImportedAt),
		LastImportSeconds:  st.LastImportSeconds,
		LastRematchSeconds: st.LastRematchSeconds,
		RematchPending:     st.RematchPending,
	}

	if st.LastError != "" {
		msg := st.LastError
		out.LastError = &msg
	}

	return out
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}

	s := t.UTC().Format(time.RFC3339)

	return &s
}

// redactedURL returns raw without any credentials.
func redactedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	u.User = nil

	return u.String()
}
