// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"math"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

var (
	projectNameRe   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
	projectPrefixRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{0,100}$`)
)

const (
	projectRoleMessage      = "role must be owner, maintainer, developer, or viewer"
	projectGroupRoleMessage = "role must be maintainer, developer, or viewer"
)

// ProjectsHandler serves /api/v1/projects: projects, their members and the
// groups attached to them. The RBAC middleware is the first gate (project
// scope, 404 for non-members, 403 below the required role); the handler
// re-checks the same decision from the evaluator, so a route reached any other
// way is still protected, and adds the escalation guards the middleware cannot
// know (which role is granted to whom).
type ProjectsHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	settings  *auth.ProjectSettings
}

// NewProjectsHandler returns the projects handler. evaluator decides roles and
// who is a global admin (implicit owner of every project); settings holds the
// projects.self_create switch.
func NewProjectsHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, settings *auth.ProjectSettings) *ProjectsHandler {
	return &ProjectsHandler{db: db, log: log, evaluator: evaluator, settings: settings}
}

func (h *ProjectsHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/projects", h.handleCreate)
	r.Get("/api/v1/projects", h.handleList)
	r.Get("/api/v1/projects/{project}", h.handleGet)
	r.Patch("/api/v1/projects/{project}", h.handleUpdate)
	r.Delete("/api/v1/projects/{project}", h.handleDelete)
	r.Get("/api/v1/projects/{project}/repositories", h.handleListRepositories)
	r.Get("/api/v1/projects/{project}/members", h.handleListMembers)
	r.Post("/api/v1/projects/{project}/members", h.handleAddMember)
	r.Patch("/api/v1/projects/{project}/members/{user_id}", h.handleUpdateMember)
	r.Delete("/api/v1/projects/{project}/members/{user_id}", h.handleRemoveMember)
	r.Get("/api/v1/projects/{project}/groups", h.handleListGroups)
	r.Put("/api/v1/projects/{project}/groups/{group}", h.handleSetGroup)
	r.Delete("/api/v1/projects/{project}/groups/{group}", h.handleRemoveGroup)
	r.Get("/api/v1/projects/{project}/tokens", h.handleListTokens)
	r.Post("/api/v1/projects/{project}/tokens", h.handleCreateToken)
	r.Delete("/api/v1/projects/{project}/tokens/{token_id}", h.handleRevokeToken)
	r.Get("/api/v1/projects/{project}/usage", h.handleGetUsage)
	r.Get("/api/v1/projects/{project}/activity", h.handleListActivity)
	r.Get("/api/v1/projects/{project}/activity/export", h.handleExportActivity)
	r.Get("/api/v1/projects/{project}/quota", h.handleGetQuota)
	r.Put("/api/v1/projects/{project}/quota", h.handlePutQuota)
	r.Get("/api/v1/projects/{project}/cleanup-policies", h.handleListCleanupPolicies)
	r.Post("/api/v1/projects/{project}/cleanup-policies", h.handleCreateCleanupPolicy)
	r.Put("/api/v1/projects/{project}/cleanup-policies/{id}", h.handleUpdateCleanupPolicy)
	r.Delete("/api/v1/projects/{project}/cleanup-policies/{id}", h.handleDeleteCleanupPolicy)
	r.Get("/api/v1/admin/projects/settings", h.handleGetSettings)
	r.Put("/api/v1/admin/projects/settings", h.handlePutSettings)
}

func validProjectRole(role string) bool {
	return models.ProjectRoleRank(role) > 0
}

func validProjectGroupRole(role string) bool {
	return validProjectRole(role) && role != models.ProjectRoleOwner
}

// projectPolicy is the policy part of the create/update bodies. Pointer fields
// leave the stored value alone when omitted; allowed_formats [] means every
// format (stored NULL).
type projectPolicy struct {
	AllowedFormats   *[]string `json:"allowed_formats"`
	AllowedRepoTypes *[]string `json:"allowed_repo_types"`
	RepoNamePrefix   *string   `json:"repo_name_prefix"`
	AllowPublicRepos *bool     `json:"allow_public_repos"`
	MaxRepositories  *int      `json:"max_repositories"`
	TokenMaxTTLDays  *int      `json:"token_max_ttl_days"`
}

func (pp *projectPolicy) set() bool {
	return pp.AllowedFormats != nil || pp.AllowedRepoTypes != nil || pp.RepoNamePrefix != nil ||
		pp.AllowPublicRepos != nil || pp.MaxRepositories != nil || pp.TokenMaxTTLDays != nil
}

// validate returns the validation message for the first invalid field, or "".
func (pp *projectPolicy) validate() string {
	if pp.AllowedFormats != nil {
		for _, f := range *pp.AllowedFormats {
			if !validFormats[f] {
				return "allowed_formats contains an unknown format"
			}
		}
	}

	if pp.AllowedRepoTypes != nil {
		if len(*pp.AllowedRepoTypes) == 0 {
			return "allowed_repo_types must not be empty"
		}

		for _, t := range *pp.AllowedRepoTypes {
			if t != models.ProjectRepoTypeHosted && t != models.ProjectRepoTypeProxy && t != models.ProjectRepoTypeGroup {
				return "allowed_repo_types must contain hosted, proxy, or group"
			}
		}
	}

	if pp.RepoNamePrefix != nil && !projectPrefixRe.MatchString(*pp.RepoNamePrefix) {
		return "repo_name_prefix must be at most 100 alphanumeric characters, hyphens, or underscores"
	}

	for _, n := range []*int{pp.MaxRepositories, pp.TokenMaxTTLDays} {
		if n != nil && (*n < 0 || *n > math.MaxInt32) {
			return "max_repositories and token_max_ttl_days must be between 0 and 2147483647"
		}
	}

	return ""
}

// apply copies the set fields onto project, de-duplicating the lists.
func (pp *projectPolicy) apply(project *models.Project) {
	if pp.AllowedFormats != nil {
		project.AllowedFormats = nil

		if len(*pp.AllowedFormats) > 0 {
			project.AllowedFormats = dedupe(*pp.AllowedFormats)
		}
	}

	if pp.AllowedRepoTypes != nil {
		project.AllowedRepoTypes = dedupe(*pp.AllowedRepoTypes)
	}

	if pp.RepoNamePrefix != nil {
		project.RepoNamePrefix = *pp.RepoNamePrefix
	}

	if pp.AllowPublicRepos != nil {
		project.AllowPublicRepos = *pp.AllowPublicRepos
	}

	if pp.MaxRepositories != nil {
		project.MaxRepositories = *pp.MaxRepositories
	}

	if pp.TokenMaxTTLDays != nil {
		project.TokenMaxTTLDays = *pp.TokenMaxTTLDays
	}
}

// policyChanges names the policy fields whose value differs between two
// states of a project.
func policyChanges(before, after *models.Project) []string {
	var changed []string

	add := func(differs bool, name string) {
		if differs {
			changed = append(changed, name)
		}
	}

	add(!slices.Equal(before.AllowedFormats, after.AllowedFormats), "allowed_formats")
	add(!slices.Equal(before.AllowedRepoTypes, after.AllowedRepoTypes), "allowed_repo_types")
	add(before.RepoNamePrefix != after.RepoNamePrefix, "repo_name_prefix")
	add(before.AllowPublicRepos != after.AllowPublicRepos, "allow_public_repos")
	add(before.MaxRepositories != after.MaxRepositories, "max_repositories")
	add(before.TokenMaxTTLDays != after.TokenMaxTTLDays, "token_max_ttl_days")

	return changed
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))

	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}

	return out
}

// projectAccess describes what the calling principal may do in a project.
type projectAccess struct {
	project   *models.Project
	principal *auth.Principal
	// admin: global admin, an implicit owner of every project.
	admin bool
	// role is the caller's own role ("" for a non-member).
	role string
}

// effectiveRole is the role the caller acts with: owner for a global admin.
func (a *projectAccess) effectiveRole() string {
	if a.admin {
		return models.ProjectRoleOwner
	}

	return a.role
}

func (a *projectAccess) rank() int { return models.ProjectRoleRank(a.effectiveRole()) }

func (a *projectAccess) isOwner() bool {
	return a.rank() >= models.ProjectRoleRank(models.ProjectRoleOwner)
}

// canGrant reports whether the caller may give, change or take away role:
// owners any role, maintainers only roles below maintainer.
func (a *projectAccess) canGrant(role string) bool {
	return a.isOwner() ||
		(a.rank() >= models.ProjectRoleRank(models.ProjectRoleMaintainer) &&
			models.ProjectRoleRank(role) < models.ProjectRoleRank(models.ProjectRoleMaintainer))
}

// userID is the caller's user ID, nil for the legacy admin key.
func (a *projectAccess) userID() *string { return a.principal.UserID }

// caller returns the authenticated principal; it writes 401 and returns false
// for anonymous requests.
func (h *ProjectsHandler) caller(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, false
	}

	return p, true
}

func (h *ProjectsHandler) isAdmin(r *http.Request, p *auth.Principal) bool {
	if h.evaluator == nil {
		return false
	}

	ok, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil {
		h.log.Errorw("project admin check failed", "error", err)
		return false
	}

	return ok
}

func (h *ProjectsHandler) dbError(w http.ResponseWriter, msg string, err error) {
	h.log.Errorw(msg+": db error", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "database error")
}

// loadAccess authenticates the caller, loads the project named in the URL and
// applies the project decision for action: not found and non-member callers
// get 404, members below the required role 403. It writes the error response
// and returns false on failure.
func (h *ProjectsHandler) loadAccess(w http.ResponseWriter, r *http.Request, action auth.ProjectAction) (*projectAccess, bool) {
	p, ok := h.caller(w, r)
	if !ok {
		return nil, false
	}

	project, err := h.db.GetProjectByName(r.Context(), urlparams.Vars(r)["project"])
	if err != nil {
		h.dbError(w, "get project", err)
		return nil, false
	}

	if project == nil {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return nil, false
	}

	role, admin, err := h.evaluator.ProjectRole(r.Context(), *p, project.ID)
	if err != nil {
		h.dbError(w, "resolve project role", err)
		return nil, false
	}

	switch auth.ProjectDecisionFor(role, admin, action) {
	case auth.ProjectAllow:
		return &projectAccess{project: project, principal: p, admin: admin, role: role}, true
	case auth.ProjectForbidden:
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
	default:
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	}

	return nil, false
}

// canCreate reports whether the principal may create a project: a global
// admin always, any human user while projects.self_create is on.
func (h *ProjectsHandler) canCreate(r *http.Request, p *auth.Principal, admin bool) (bool, error) {
	if admin {
		return true, nil
	}

	if p.UserID == nil || p.IsLegacy || len(p.TokenScopes) > 0 || p.TokenKind == auth.TokenKindDeploy {
		return false, nil
	}

	return h.settings.SelfCreate(r.Context())
}

// Defaults of a new project's policy (the migration gives existing projects the
// same values).
const (
	defaultProjectMaxRepositories = 50
	defaultProjectTokenMaxTTLDays = 365
)

type createProjectRequest struct {
	projectPolicy

	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// Owner is a username; only a global admin may name an owner other than
	// themselves.
	Owner string `json:"owner"`
}

func (req *createProjectRequest) validate() string {
	if !projectNameRe.MatchString(req.Name) {
		return "project name must be 1-100 alphanumeric characters, hyphens, or underscores"
	}

	slug := models.ProjectSlug(req.Name)
	if slug == "" || len(slug) > models.MaxProjectSlugLen {
		return "project name must yield a URL slug of 1-64 lowercase letters, digits and hyphens"
	}

	return req.projectPolicy.validate()
}

// resolveOwner returns the new project's owner: the caller, or for an admin
// the user named in the request. It writes the error response on failure.
func (h *ProjectsHandler) resolveOwner(w http.ResponseWriter, r *http.Request, p *auth.Principal, req *createProjectRequest) (*models.User, bool) {
	var (
		user *models.User
		err  error
	)

	switch {
	case req.Owner != "":
		user, err = h.db.GetUserByUsername(r.Context(), req.Owner)
	case p.UserID != nil:
		user, err = h.db.GetUserByID(r.Context(), *p.UserID)
	default:
		writeError(w, http.StatusBadRequest, "validation_failed", "owner is required")
		return nil, false
	}

	if err != nil {
		h.dbError(w, "get project owner", err)
		return nil, false
	}

	if user == nil || user.IsService() {
		writeError(w, http.StatusNotFound, "not_found", "owner user not found")
		return nil, false
	}

	return user, true
}

func (h *ProjectsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := h.caller(w, r)
	if !ok {
		return
	}

	admin := h.isAdmin(r, p)

	allowed, err := h.canCreate(r, p, admin)
	if err != nil {
		h.dbError(w, "read projects.self_create", err)
		return
	}

	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "creating projects is not allowed")
		return
	}

	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	// Policy decides what self-service users may do, and only admins may
	// create on behalf of someone else.
	if !admin && (req.set() || req.Owner != "") {
		writeError(w, http.StatusForbidden, "forbidden", "only a global admin can set the project policy or owner")
		return
	}

	if msg := req.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	owner, ok := h.resolveOwner(w, r, p, &req)
	if !ok {
		return
	}

	project := h.newProject(&req, owner.ID)

	// The unique constraints are authoritative: no check-then-insert race.
	if err := h.db.CreateProject(r.Context(), project); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "project name or slug already taken")
			return
		}

		h.dbError(w, "create project", err)

		return
	}

	if err := h.db.AddProjectMember(r.Context(), project.ID, owner.ID, models.ProjectRoleOwner); err != nil {
		h.log.Errorw("add project owner member: db error", "error", err)

		// Do not leave a project with no owner membership behind.
		if delErr := h.db.DeleteProject(r.Context(), project.ID); delErr != nil {
			h.log.Errorw("rollback project create failed", "project_id", project.ID, "error", delErr)
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	h.emit(r, projectEvent{
		action: audit.ActionProjectCreated, projectID: &project.ID, resourceID: project.ID,
		message: "project " + project.Name + " created, owner " + owner.Username,
	})

	writeJSON(w, http.StatusCreated, projectToResponse(project, models.ProjectRoleOwner, 0))
}

func (h *ProjectsHandler) newProject(req *createProjectRequest, ownerID string) *models.Project {
	slug := models.ProjectSlug(req.Name)
	now := time.Now()
	project := &models.Project{
		Name:             req.Name,
		DisplayName:      &req.DisplayName,
		Description:      &req.Description,
		OwnerID:          &ownerID,
		Slug:             slug,
		AllowedRepoTypes: []string{models.ProjectRepoTypeHosted},
		RepoNamePrefix:   slug + "-",
		MaxRepositories:  defaultProjectMaxRepositories,
		TokenMaxTTLDays:  defaultProjectTokenMaxTTLDays,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	req.apply(project)

	return project
}

func (h *ProjectsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	p, ok := h.caller(w, r)
	if !ok {
		return
	}

	admin := h.isAdmin(r, p)
	if r.URL.Query().Get("all") == "true" && !admin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	roles, err := h.evaluator.ProjectRoles(r.Context(), *p)
	if err != nil {
		h.dbError(w, "list project roles", err)
		return
	}

	projects, err := h.visibleProjects(r, p, roles)
	if err != nil {
		h.dbError(w, "list projects", err)
		return
	}

	ids := make([]string, len(projects))
	for i, project := range projects {
		ids[i] = project.ID
	}

	counts, err := h.db.CountRepositoriesByProject(r.Context(), ids)
	if err != nil {
		h.dbError(w, "count project repositories", err)
		return
	}

	result := make([]responses.ProjectResponse, 0, len(projects))

	for _, project := range projects {
		role := roles[project.ID]
		if admin {
			role = models.ProjectRoleOwner
		}

		result = append(result, projectToResponse(project, role, counts[project.ID]))
	}

	canCreate, err := h.canCreate(r, p, admin)
	if err != nil {
		h.dbError(w, "read projects.self_create", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"projects": result, "can_create": canCreate})
}

// visibleProjects returns the projects listed to the caller: with ?all=true
// (admins only, checked by the caller) every project, otherwise the ones the
// caller holds a role in.
func (h *ProjectsHandler) visibleProjects(r *http.Request, p *auth.Principal, roles map[string]string) ([]*models.Project, error) {
	if r.URL.Query().Get("all") == "true" {
		return h.db.ListAllProjects(r.Context()) //nolint:wrapcheck // logged by caller
	}

	if p.UserID == nil || len(roles) == 0 {
		return []*models.Project{}, nil
	}

	mine, err := h.db.ListProjects(r.Context(), *p.UserID)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by caller
	}

	// The role map is authoritative: it leaves out the system project and, for
	// scoped tokens, everything.
	visible := make([]*models.Project, 0, len(mine))

	for _, project := range mine {
		if roles[project.ID] != "" {
			visible = append(visible, project)
		}
	}

	return visible, nil
}

func (h *ProjectsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionRead)
	if !ok {
		return
	}

	counts, err := h.db.CountRepositoriesByProject(r.Context(), []string{access.project.ID})
	if err != nil {
		h.dbError(w, "count project repositories", err)
		return
	}

	writeJSON(w, http.StatusOK, projectToResponse(access.project, access.effectiveRole(), counts[access.project.ID]))
}

func (h *ProjectsHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionUpdate)
	if !ok {
		return
	}

	// Pointer fields: omitted fields keep their current value.
	var req struct {
		projectPolicy

		DisplayName *string `json:"display_name"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if msg := req.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	// Policy decides what self-service users may do: global admins only.
	if req.set() && !access.admin {
		writeError(w, http.StatusForbidden, "forbidden", "only a global admin can change the project policy")
		return
	}

	project := access.project
	before := *project

	if req.DisplayName != nil {
		project.DisplayName = req.DisplayName
	}

	if req.Description != nil {
		project.Description = req.Description
	}

	req.apply(project)

	project.UpdatedAt = time.Now()

	if err := h.db.UpdateProject(r.Context(), project); err != nil {
		h.dbError(w, "update project", err)
		return
	}

	h.emitProjectUpdate(r, &before, project)

	counts, err := h.db.CountRepositoriesByProject(r.Context(), []string{project.ID})
	if err != nil {
		h.dbError(w, "count project repositories", err)
		return
	}

	writeJSON(w, http.StatusOK, projectToResponse(project, access.effectiveRole(), counts[project.ID]))
}

func (h *ProjectsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadAccess(w, r, auth.ProjectActionDelete)
	if !ok {
		return
	}

	if access.project.ID == models.SystemProjectID {
		writeError(w, http.StatusConflict, "conflict", "the system project cannot be deleted")
		return
	}

	if err := h.db.DeleteProject(r.Context(), access.project.ID); err != nil {
		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "project still owns repositories (including repositories in the trash, which must be purged or restored first)")
			return
		}

		h.dbError(w, "delete project", err)

		return
	}

	// The audit row cannot reference the deleted project (project_id would
	// dangle), so the project ID travels as the resource ID.
	h.emit(r, projectEvent{
		action: audit.ActionProjectDeleted, resourceID: access.project.ID,
		message: "project " + access.project.Name + " deleted",
	})

	writeNoContent(w)
}

// Helper functions

func projectToResponse(project *models.Project, myRole string, repositories int) responses.ProjectResponse {
	return responses.ProjectResponse{
		ID:               project.ID,
		Name:             project.Name,
		DisplayName:      project.DisplayName,
		Description:      project.Description,
		OwnerID:          project.OwnerID,
		Slug:             project.Slug,
		AllowedFormats:   project.AllowedFormats,
		AllowedRepoTypes: project.AllowedRepoTypes,
		RepoNamePrefix:   project.RepoNamePrefix,
		AllowPublicRepos: project.AllowPublicRepos,
		MaxRepositories:  project.MaxRepositories,
		TokenMaxTTLDays:  project.TokenMaxTTLDays,
		CreatedAt:        formatTime(project.CreatedAt),
		UpdatedAt:        formatTime(project.UpdatedAt),
		MyRole:           myRole,
		RepositoryCount:  repositories,
		IsSystem:         project.ID == models.SystemProjectID,
	}
}
