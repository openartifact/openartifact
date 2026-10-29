// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/dtrack"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// handleHealth aggregates scanner readiness, the scan queue, the integrations
// and the scan coverage for the security pages. Administrative.
func (h *SecurityHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	ctx := r.Context()

	stats, err := h.db.SecurityHealthStats(ctx)
	if err != nil {
		h.log.Errorw("security health: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	dt, err := dtrack.LoadGlobal(ctx, h.db)
	if err != nil {
		h.log.Errorw("security health: dependency-track settings", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	push, err := h.db.LatestFinishedDependencyTrackPush(ctx)
	if err != nil {
		h.log.Errorw("security health: dependency-track push", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	resp := responses.SecurityHealthResponse{
		Scanners:              []responses.SecurityHealthScannerResponse{},
		LastScanAt:            formatTimePtr(stats.LastScanAt),
		ScansPending:          stats.ScansPending,
		ScansFailed24h:        stats.ScansFailed24h,
		DependencyTrack:       responses.SecurityHealthDependencyTrackResponse{Enabled: dt.Enabled},
		AI:                    responses.SecurityHealthAIResponse{Enabled: h.aiEnabled != nil && h.aiEnabled(ctx)},
		UnscannedRepositories: stats.UnscannedRepositories,
	}

	if push != nil {
		resp.DependencyTrack.LastPushOK = &push.OK
		resp.DependencyTrack.LastPushAt = formatTime(push.At)
	}

	if h.scanners != nil {
		for _, s := range h.scanners.Status() {
			resp.Scanners = append(resp.Scanners, responses.SecurityHealthScannerResponse{
				Name: s.Name, Ready: s.Ready, Message: s.Message, DocsURL: s.DocsURL,
			})
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleRepoSummary returns the counts-only security state of a repository,
// narrowed to a package (?package_name) and version (?version) when given. It
// needs read access to the repository and exposes no ids, titles or other
// finding details, so readers and tokens that cannot list findings can show it.
func (h *SecurityHandler) handleRepoSummary(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	if ok, err := h.evaluator.Evaluate(ctx, *p, name, "", "read"); err != nil || !ok {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return
	}

	q := r.URL.Query()
	pkg, version := q.Get("package_name"), q.Get("version")

	if len(pkg) > maxNameLen || len(version) > maxNameLen {
		writeError(w, http.StatusBadRequest, "validation_failed", "package_name or version is too long")
		return
	}

	if version != "" && pkg == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "version needs package_name")
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("repository security summary: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	s, err := h.db.RepoSecuritySummary(ctx, repo.ID, pkg, version)
	if err != nil {
		h.log.Errorw("repository security summary: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, responses.RepoSecuritySummaryResponse{
		ScannedAt: formatTimePtr(s.ScannedAt),
		Vuln: responses.SecurityFindingCounts{
			Critical: s.VulnCritical, High: s.VulnHigh, Medium: s.VulnMedium, Low: s.VulnLow,
		},
		License:     responses.RepoSecurityLicenseResponse{Violations: s.LicenseViolations},
		Secret:      responses.RepoSecuritySecretResponse{Count: s.SecretCount},
		AI:          responses.RepoSecurityAIResponse{QuarantineRecommended: s.AIQuarantineRecommended, ReviewRequired: s.AIReviewRequired},
		Quarantined: s.Quarantined > 0,
	})
}
