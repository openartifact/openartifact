// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// maxProjectTokens caps the active tokens of one project.
const maxProjectTokens = 100

const projectTokenRoleMessage = "role must be viewer or developer" //nolint:gosec // validation message, not a credential

// projectTokenBody is the body of POST /api/v1/projects/{project}/tokens.
type projectTokenBody struct {
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// validate checks the request shape against the project's policy and returns a
// user-facing message, or "" when the body is acceptable. It normalises Name.
func (b *projectTokenBody) validate(project *models.Project, now time.Time) string {
	b.Name = strings.TrimSpace(b.Name)

	switch {
	case b.Name == "":
		return "name is required"
	case len(b.Name) > maxTokenNameLength:
		return "name must be at most 100 characters"
	case b.Role != models.ProjectRoleViewer && b.Role != models.ProjectRoleDeveloper:
		return projectTokenRoleMessage
	}

	if b.ExpiresAt == nil {
		if project.TokenMaxTTLDays > 0 {
			return "expires_at is required"
		}

		return ""
	}

	if !b.ExpiresAt.After(now) {
		return "expires_at must be in the future"
	}

	if project.TokenMaxTTLDays > 0 && b.ExpiresAt.After(now.AddDate(0, 0, project.TokenMaxTTLDays)) {
		return "expires_at exceeds the project's token lifetime limit"
	}

	return ""
}

// isMaintainer reports whether the caller may see and revoke every token of the
// project (maintainer and up, global admins included).
func (a *projectAccess) isMaintainer() bool {
	return a.rank() >= models.ProjectRoleRank(models.ProjectRoleMaintainer)
}

func (h *ProjectsHandler) handleListTokens(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionTokens)
	if !ok {
		return
	}

	var createdBy *string
	if !access.isMaintainer() {
		createdBy = access.principal.UserID
		if createdBy == nil {
			writeJSON(w, http.StatusOK, map[string]any{"tokens": []responses.ProjectTokenResponse{}})
			return
		}
	}

	tokens, err := h.db.ListProjectTokens(r.Context(), access.project.ID, createdBy)
	if err != nil {
		h.dbError(w, "list project tokens", err)
		return
	}

	result := make([]responses.ProjectTokenResponse, 0, len(tokens))
	for _, t := range tokens {
		result = append(result, projectTokenToResponse(t))
	}

	writeJSON(w, http.StatusOK, map[string]any{"tokens": result})
}

func (h *ProjectsHandler) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionTokens)
	if !ok {
		return
	}

	p := access.principal

	// Only an interactive session may mint: a leaked token must not spawn
	// replacements, and project tokens are bound to the human who minted them.
	if p.IsToken() || p.Credential != auth.CredentialSession || p.UserID == nil {
		writeError(w, http.StatusForbidden, "forbidden", "project tokens can only be created from a session")
		return
	}

	var body projectTokenBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if msg := body.validate(access.project, time.Now()); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if models.ProjectRoleRank(body.Role) > access.rank() {
		writeError(w, http.StatusForbidden, "forbidden", "token role exceeds your own role")
		return
	}

	secret, record, err := auth.CreateProjectToken(r.Context(), h.db, database.ProjectTokenSpec{
		ProjectID:   access.project.ID,
		ProjectSlug: access.project.Slug[:min(len(access.project.Slug), 40)],
		Name:        body.Name,
		Role:        body.Role,
		CreatedBy:   *p.UserID,
		ExpiresAt:   body.ExpiresAt,
		MaxActive:   maxProjectTokens,
	})

	switch {
	case errors.Is(err, database.ErrProjectTokenLimit):
		writeError(w, http.StatusConflict, "conflict", "the project has reached its limit of active tokens")
		return
	case errors.Is(err, database.ErrProjectTokenName):
		writeError(w, http.StatusConflict, "conflict", "a token with this name already exists in the project")
		return
	case errors.Is(err, database.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	case err != nil:
		h.dbError(w, "create project token", err)
		return
	}

	record.ProjectName = access.project.Name
	record.CreatedByName = p.Username

	h.emit(r, projectEvent{
		action: audit.ActionProjectTokenCreated, projectID: &access.project.ID, resourceType: "project_token", resourceID: record.ID,
		message: "token " + record.Name + " created in project " + access.project.Name + " (role " + body.Role + ", expires " + expiryLabel(record.ExpiresAt) + ")",
	})

	writeJSON(w, http.StatusCreated, responses.ProjectTokenCreateResponse{
		ProjectTokenResponse: projectTokenToResponse(record),
		Token:                secret,
	})
}

func (h *ProjectsHandler) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionTokens)
	if !ok {
		return
	}

	tokenID := urlparams.Vars(r)["token_id"]
	if !isUUIDString(tokenID) {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}

	token, err := h.db.GetTokenByID(r.Context(), tokenID)
	if err != nil {
		h.dbError(w, "get project token", err)
		return
	}

	// The token must belong to the project in the URL.
	if token == nil || token.ProjectID == nil || *token.ProjectID != access.project.ID || token.RevokedAt != nil {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}

	own := token.CreatedBy != nil && access.principal.UserID != nil && *token.CreatedBy == *access.principal.UserID
	if !own && !access.isMaintainer() {
		writeError(w, http.StatusForbidden, "forbidden", "only a maintainer can revoke tokens created by others")
		return
	}

	revoked, err := h.db.RevokeProjectToken(r.Context(), access.project.ID, tokenID)
	if err != nil {
		h.dbError(w, "revoke project token", err)
		return
	}

	if !revoked {
		writeError(w, http.StatusNotFound, "not_found", "token not found")
		return
	}

	h.emit(r, projectEvent{
		action: audit.ActionProjectTokenRevoked, projectID: &access.project.ID, resourceType: "project_token", resourceID: tokenID,
		message: "token " + token.Name + " revoked in project " + access.project.Name,
	})

	writeNoContent(w)
}

func expiryLabel(t *time.Time) string {
	if t == nil {
		return "never"
	}

	return t.UTC().Format(time.RFC3339)
}

func projectTokenToResponse(t *models.Token) responses.ProjectTokenResponse {
	resp := responses.ProjectTokenResponse{
		ID:                t.ID,
		Name:              t.Name,
		Project:           t.ProjectName,
		ExpiresAt:         formatTimePtr(t.ExpiresAt),
		LastUsedAt:        formatTimePtr(t.LastUsedAt),
		CreatedAt:         formatTime(t.CreatedAt),
		CreatedByUsername: t.CreatedByName,
	}

	if t.ProjectRole != nil {
		resp.Role = *t.ProjectRole
	}

	if t.CreatedBy != nil {
		resp.CreatedBy = *t.CreatedBy
	}

	return resp
}
