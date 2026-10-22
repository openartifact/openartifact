// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// checkProjectRepoPolicy applies the project's self-service policy to a new (or
// transferred) repository: allowed format and type, name prefix and public
// access. It returns a zero status when the repository complies. The repository
// cap is checked in the database transaction (ErrProjectRepoLimit).
func checkProjectRepoPolicy(project *models.Project, name, format, repoType string, public bool) (int, string, string) {
	if len(project.AllowedFormats) > 0 && !slices.Contains(project.AllowedFormats, format) {
		return http.StatusBadRequest, "validation_failed", "format " + format + " is not allowed in this project; allowed: " + strings.Join(project.AllowedFormats, ", ")
	}

	if !slices.Contains(project.AllowedRepoTypes, repoType) {
		return http.StatusBadRequest, "validation_failed", "repo_type " + repoType + " is not allowed in this project; allowed: " + strings.Join(project.AllowedRepoTypes, ", ")
	}

	if !strings.HasPrefix(name, project.RepoNamePrefix) {
		return http.StatusBadRequest, "validation_failed", "name must start with " + project.RepoNamePrefix
	}

	if public && !project.AllowPublicRepos {
		return http.StatusForbidden, "forbidden", "this project does not allow public repositories"
	}

	return 0, "", ""
}

// delegatedActor returns p when it is a delegated administrator (not a global
// admin), whose requests the project policy and field guards apply to; nil for
// a global admin.
func (h *RepositoriesHandler) delegatedActor(ctx context.Context, p *auth.Principal) (*auth.Principal, error) {
	admin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || admin {
		return nil, err
	}

	return p, nil
}

// delegatedUpdateViolation returns the reason a project maintainer may not send
// an update, or "" when it is allowed: moving a repository (project), blocking
// it and the presign override stay global-admin controls, and public access
// needs the project policy to allow it.
func (h *RepositoriesHandler) delegatedUpdateViolation(ctx context.Context, repo *models.Repository, blocked, presign, project bool, public *bool) string {
	switch {
	case project:
		return "only an administrator can move a repository to another project; use the project transfer route"
	case blocked:
		return "only an administrator can block a repository"
	case presign:
		return "only an administrator can change presign_downloads"
	}

	if public == nil || !*public || repo.Public || repo.ProjectID == nil {
		return ""
	}

	owner, err := h.db.GetProject(ctx, *repo.ProjectID)
	if err != nil {
		h.log.Errorw("update repository: project lookup failed", "error", err)
		return "project policy could not be checked"
	}

	if owner == nil || !owner.AllowPublicRepos {
		return "this project does not allow public repositories"
	}

	return ""
}

// projectDecision gates a project-scoped repository route: 404 for a caller
// without a role in the project (and for an unknown project), 403 for a role
// that is too low. It writes the response and returns false unless allowed.
func (h *RepositoriesHandler) projectDecision(w http.ResponseWriter, r *http.Request, projectName string, action auth.ProjectAction) bool {
	p := currentPrincipal(r)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return false
	}

	decision, err := h.evaluator.DecideProject(r.Context(), *p, projectName, action)
	if err != nil {
		h.log.Errorw("project repository route: auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return false
	}

	switch decision {
	case auth.ProjectAllow:
		return true
	case auth.ProjectForbidden:
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
	case auth.ProjectHidden:
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	}

	return false
}

// handleCreateInProject creates a repository in {project} for a maintainer or
// owner (or a global admin), under the project's policy.
//
//	POST /api/v1/projects/{project}/repositories
func (h *RepositoriesHandler) handleCreateInProject(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["project"]
	ctx := r.Context()

	if !h.projectDecision(w, r, name, auth.ProjectActionRepoCreate) {
		return
	}

	project := h.resolveProject(ctx, w, name)
	if project == nil {
		return
	}

	actor, err := h.delegatedActor(ctx, currentPrincipal(r))
	if err != nil {
		h.log.Errorw("create repository: auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return
	}

	var body createRepositoryBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	body.Project = ""

	h.createRepository(w, r, &body, project, actor)
}

// handleTransfer moves a repository of {project} to another project. It takes
// ownership of both projects (a global admin holds both implicitly); the target
// project's policy applies as it does to a new repository.
//
//	POST /api/v1/projects/{project}/repositories/{name}/transfer  {"to_project": "…"}
func (h *RepositoriesHandler) handleTransfer(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	ctx := r.Context()

	if !h.projectDecision(w, r, vars["project"], auth.ProjectActionRepoTransfer) {
		return
	}

	var body struct {
		ToProject string `json:"to_project"`
	}
	if err := decodeJSON(r, &body); err != nil || body.ToProject == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "to_project is required")
		return
	}

	if !h.projectDecision(w, r, body.ToProject, auth.ProjectActionRepoTransfer) {
		return
	}

	from := h.resolveProject(ctx, w, vars["project"])
	if from == nil {
		return
	}

	to := h.resolveProject(ctx, w, body.ToProject)
	if to == nil {
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, vars["name"])
	if err != nil {
		h.log.Errorw("transfer repository: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// The repository must belong to the project in the URL.
	if repo == nil || repo.ProjectID == nil || *repo.ProjectID != from.ID {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if to.ID == from.ID {
		writeError(w, http.StatusBadRequest, "validation_failed", "the repository already belongs to that project")
		return
	}

	actor, err := h.delegatedActor(ctx, currentPrincipal(r))
	if err != nil {
		h.log.Errorw("transfer repository: auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return
	}

	maxRepos := 0

	if actor != nil {
		if status, code, msg := checkProjectRepoPolicy(to, repo.Name, repo.Format, repo.RepoType, repo.Public); status != 0 {
			writeError(w, status, code, msg)
			return
		}

		maxRepos = to.MaxRepositories
	}

	if err := h.db.MoveRepositoryToProject(ctx, repo.ID, to.ID, maxRepos); err != nil {
		switch {
		case errors.Is(err, database.ErrProjectRepoLimit):
			writeError(w, http.StatusConflict, "project_repository_limit", "the target project has reached its repository limit")
		case errors.Is(err, database.ErrProjectQuotaExceeded):
			writeError(w, http.StatusConflict, "project_quota_exceeded", "the target project's storage quota cannot take this repository's usage")
		case errors.Is(err, database.ErrRepositoryNotFound):
			writeError(w, http.StatusNotFound, "not_found", "repository not found")
		default:
			h.log.Errorw("transfer repository: db error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		}

		return
	}

	h.emitProjectRepoEvent(r, "project.repository_transferred", to.ID, &repo.ID,
		"repository "+repo.Name+" transferred from project "+from.Name+" to "+to.Name)

	repo.ProjectID = &to.ID

	writeJSON(w, http.StatusOK, h.repositoryResponse(ctx, repo, map[string]string{}))
}

// emitProjectRepoEvent records a project.repository_* audit event carrying the
// project and, unless it is gone (repoID nil), the repository.
func (h *RepositoriesHandler) emitProjectRepoEvent(r *http.Request, action, projectID string, repoID *string, message string) {
	l := audit.Default()
	if l == nil {
		return
	}

	resourceType := "repository"
	out := audit.AuditEvent{
		Action:       action,
		ActorType:    "system",
		ProjectID:    &projectID,
		RepositoryID: repoID,
		ResourceType: &resourceType,
		ResourceID:   repoID,
		Details:      message,
	}

	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		out.ActorType = "user"
		out.Actor = p.Username
		out.ActorID = p.UserID

		if p.IsToken() {
			out.ActorType = "token"
		}
	}

	l.Log(r.Context(), out)
}
