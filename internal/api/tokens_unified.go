// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"slices"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	unifiedTokensDefaultLimit = 25
	unifiedTokensMaxLimit     = 100
)

// handleUnifiedList lists the tokens the caller may manage in one place:
// personal, project and federation tokens. A global admin sees every token;
// anyone else sees their own personal tokens plus the project and federation
// tokens of the projects they maintain or own. Secrets are never returned and
// revoked tokens are not listed. An API token may list only as an admin: like
// the personal token endpoints, listing needs an interactive session.
//
//	GET /api/v1/tokens?type=personal|project|federation&project=&expires_before=&q=&page=&limit=
func (h *AdminTokensHandler) handleUnifiedList(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	params, ok := parseUnifiedTokenParams(w, r)
	if !ok {
		return
	}

	admin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return
	}

	if !admin && (p.UserID == nil || p.IsToken() || p.ServiceProjectID != "") {
		writeError(w, http.StatusForbidden, "forbidden", "listing tokens needs an interactive session")
		return
	}

	params.Admin = admin

	if !admin {
		params.ViewerID = *p.UserID

		if params.ProjectIDs, err = h.maintainedProjectIDs(r, *p.UserID); err != nil {
			h.log.Errorw("list tokens: project grants lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}
	}

	items, total, err := h.db.ListUnifiedTokens(r.Context(), params)
	if err != nil {
		h.log.Errorw("list tokens failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	now := time.Now()
	resp := responses.UnifiedTokenList{
		Tokens: make([]responses.UnifiedTokenResponse, 0, len(items)),
		Total:  total,
		Page:   params.Page,
		Limit:  params.Limit,
	}

	for _, t := range items {
		resp.Tokens = append(resp.Tokens, responses.UnifiedTokenResponse{
			ID: t.ID, Type: t.Type, Name: t.Name, Owner: t.Owner,
			Project: t.Project, Repository: t.Repository, Role: t.Role, Scopes: t.Scopes,
			CreatedAt:  formatTime(t.CreatedAt),
			ExpiresAt:  formatTimePtr(t.ExpiresAt),
			LastUsedAt: formatTimePtr(t.LastUsedAt),
			Expired:    t.ExpiresAt != nil && !t.ExpiresAt.After(now),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// maintainedProjectIDs returns the projects userID may manage tokens in:
// those where they are maintainer or owner (directly or through a group).
func (h *AdminTokensHandler) maintainedProjectIDs(r *http.Request, userID string) ([]string, error) {
	grants, err := h.db.ListProjectGrantsByUser(r.Context(), userID)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	ids := make([]string, 0, len(grants))

	for _, g := range grants {
		if models.ProjectRoleRank(g.Role) >= models.ProjectRoleRank(models.ProjectRoleMaintainer) {
			ids = append(ids, g.ProjectID)
		}
	}

	return ids, nil
}

// parseUnifiedTokenParams reads and validates the list query; it writes the
// 400 itself and returns false on a bad value.
func parseUnifiedTokenParams(w http.ResponseWriter, r *http.Request) (models.TokenListParams, bool) {
	q := r.URL.Query()
	params := models.TokenListParams{
		Query:   q.Get("q"),
		Project: q.Get("project"),
		Page:    queryIntParam(r, "page", 1, 1, 10000),
		Limit:   queryIntParam(r, "limit", unifiedTokensDefaultLimit, 1, unifiedTokensMaxLimit),
	}

	if t := q.Get("type"); t != "" {
		if !slices.Contains([]string{models.TokenListPersonal, models.TokenListProject, models.TokenListFederation}, t) {
			writeError(w, http.StatusBadRequest, "validation_failed", "type must be personal, project or federation")
			return params, false
		}

		params.Types = []string{t}
	}

	if raw := q.Get("expires_before"); raw != "" {
		before, err := parseTokenTime(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "expires_before must be an RFC 3339 timestamp or a YYYY-MM-DD date")
			return params, false
		}

		params.ExpiresBefore = &before
	}

	return params, true
}

// parseTokenTime accepts an RFC 3339 timestamp or a plain date (midnight UTC).
func parseTokenTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	return time.Parse(time.DateOnly, s)
}
