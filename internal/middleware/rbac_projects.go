// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// isProjectRoute reports whether the scope is a project control-plane route.
func (s routeScope) isProjectRoute() bool {
	return s.projectsRoot || s.projectAction != ""
}

// classifyProjectRoute maps /api/v1/projects/** to project actions. segs are
// the path segments after /api/v1/ (segs[0] == "projects"). Only the routes
// listed here are project-scoped; every other shape (unknown sub-paths,
// unexpected methods, escaped project names) returns ok=false so it falls to
// the global "manage" permission: deny by default.
func classifyProjectRoute(method string, segs []string) (routeScope, bool) {
	if len(segs) == 1 {
		if method == http.MethodGet || method == http.MethodPost {
			return routeScope{action: "manage", projectsRoot: true}, true
		}

		return routeScope{}, false
	}

	project := segs[1]
	if !isPlainName(project) {
		return routeScope{}, false
	}

	action, ok := projectActionFor(method, segs[2:])
	if !ok {
		return routeScope{}, false
	}

	return routeScope{action: "manage", project: project, projectAction: action}, true
}

// projectActionFor returns the action for method on the sub-path under
// /projects/{project} (rest is empty for the project itself).
func projectActionFor(method string, rest []string) (auth.ProjectAction, bool) {
	switch len(rest) {
	case 0:
		return projectItemAction(method)
	case 1:
		return projectCollectionAction(method, rest[0])
	case 2:
		return projectMemberAction(method, rest[0], rest[1])
	case 3:
		if method == http.MethodPost && rest[0] == "repositories" && isSingleSegment(rest[1]) && rest[2] == "transfer" {
			return auth.ProjectActionRepoTransfer, true
		}
	}

	return "", false
}

// projectItemAction is the action for the project itself.
func projectItemAction(method string) (auth.ProjectAction, bool) {
	switch method {
	case http.MethodGet:
		return auth.ProjectActionRead, true
	case http.MethodPatch:
		return auth.ProjectActionUpdate, true
	case http.MethodDelete:
		return auth.ProjectActionDelete, true
	}

	return "", false
}

// projectCollectionAction is the action for /projects/{project}/{collection}.
func projectCollectionAction(method, collection string) (auth.ProjectAction, bool) {
	switch {
	case method == http.MethodGet && (collection == "repositories" || collection == "members" || collection == "groups"):
		return auth.ProjectActionRead, true
	case method == http.MethodPost && collection == "members":
		return auth.ProjectActionMembersManage, true
	case method == http.MethodPost && collection == "repositories":
		return auth.ProjectActionRepoCreate, true
	}

	return "", false
}

// projectMemberAction is the action for /projects/{project}/{members|groups}/{id}.
func projectMemberAction(method, collection, id string) (auth.ProjectAction, bool) {
	if !isSingleSegment(id) {
		return "", false
	}

	switch {
	case collection == "members" && (method == http.MethodPatch || method == http.MethodDelete):
		return auth.ProjectActionMembersManage, true
	case collection == "groups" && (method == http.MethodPut || method == http.MethodDelete):
		return auth.ProjectActionMembersManage, true
	}

	return "", false
}

// authorizeProjectRoute gates a project control-plane request. It returns true
// when the request may proceed; otherwise the response has been written.
//
//	GET/POST /api/v1/projects   any authenticated non-deploy caller; POST also
//	                            needs admin or the projects.self_create setting
//	/api/v1/projects/{project}  a project role that allows the action, or admin;
//	                            a caller with no role gets 404 (like an unknown
//	                            project), a member with too low a role 403
func authorizeProjectRoute(w http.ResponseWriter, r *http.Request, cfg RBACConfig, evaluator *auth.Evaluator, log *logger.Logger, principal *auth.Principal, credential string, scope routeScope, db database.Database) bool {
	if principal.IsAnonymous || principal.TokenKind == auth.TokenKindDeploy {
		writeAccessDenied(w, r, cfg, db, log, principal, credential, scope)
		return false
	}

	if scope.projectsRoot {
		return authorizeProjectsRoot(w, r, cfg, evaluator, log, principal, credential, scope, db)
	}

	decision, err := evaluator.DecideProject(r.Context(), *principal, scope.project, scope.projectAction)
	if err != nil {
		log.Errorw("project auth evaluation error", "error", err, "path", r.URL.Path, "method", r.Method)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth_evaluation_failed"})

		return false
	}

	switch decision {
	case auth.ProjectAllow:
		return true
	case auth.ProjectHidden:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "code": "not_found"})
	case auth.ProjectForbidden:
		writeAccessDenied(w, r, cfg, db, log, principal, credential, scope)
	}

	return false
}

// authorizeProjectsRoot gates GET/POST /api/v1/projects.
func authorizeProjectsRoot(w http.ResponseWriter, r *http.Request, cfg RBACConfig, evaluator *auth.Evaluator, log *logger.Logger, principal *auth.Principal, credential string, scope routeScope, db database.Database) bool {
	if r.Method != http.MethodPost {
		return true // the handler returns the caller's projects (?all=true is admin-only there)
	}

	ctx := r.Context()

	admin, err := evaluator.Evaluate(ctx, *principal, "", "", "manage")
	if err != nil {
		log.Errorw("project create auth evaluation error", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth_evaluation_failed"})

		return false
	}

	if admin {
		return true
	}

	// Self-service creation: a human user account only. The legacy key has no
	// account, scoped tokens narrow the owner's rights and never widen them.
	if principal.UserID != nil && !principal.IsLegacy && len(principal.TokenScopes) == 0 {
		on, err := cfg.ProjectSettings.SelfCreate(ctx)
		if err != nil {
			log.Errorw("project self-create setting lookup failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth_evaluation_failed"})

			return false
		}

		if on {
			return true
		}
	}

	writeAccessDenied(w, r, cfg, db, log, principal, credential, scope)

	return false
}
