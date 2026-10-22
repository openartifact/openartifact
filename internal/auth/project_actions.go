// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ProjectAction is a control-plane action on a project (as opposed to the data
// actions read/write/delete on its repositories, see project_roles.go).
type ProjectAction string

// Project control-plane actions. Each needs a minimum project role; a global
// admin (the "manage" permission) is an implicit owner of every project.
const (
	// ProjectActionRead: see the project, its repositories, members and groups (viewer).
	ProjectActionRead ProjectAction = "project.read"
	// ProjectActionMembersManage: add, change and remove members and groups
	// below the maintainer role (maintainer). Granting or changing maintainer
	// and owner roles takes an owner; the handler enforces that per target.
	ProjectActionMembersManage ProjectAction = "members.manage"
	// ProjectActionTokens: list, mint and revoke project tokens (viewer). A
	// viewer only sees and revokes the tokens it minted itself; maintainers
	// see and revoke all of them (the handler enforces that per token).
	ProjectActionTokens ProjectAction = "tokens.create"
	// ProjectActionUpdate: edit display name and description (owner).
	ProjectActionUpdate ProjectAction = "project.update"
	// ProjectActionDelete: delete the project (owner).
	ProjectActionDelete ProjectAction = "project.delete"
	// ProjectActionRepoCreate: create a repository in the project (maintainer).
	ProjectActionRepoCreate ProjectAction = "repo.create"
	// ProjectActionRepoAdmin: update, delete and run the cleanup, quota and trash
	// of a repository of the project (maintainer); see EvaluateRepoAdmin.
	ProjectActionRepoAdmin ProjectAction = "repo.admin"
	// ProjectActionRepoTransfer: move a repository out of the project (owner;
	// the handler also needs ownership of the target project).
	ProjectActionRepoTransfer ProjectAction = "repo.transfer"
)

// projectActionMinRole is the single table of the lowest role allowed to
// perform each project action.
//
//nolint:gochecknoglobals // immutable lookup table
var projectActionMinRole = map[ProjectAction]string{
	ProjectActionRead:          models.ProjectRoleViewer,
	ProjectActionMembersManage: models.ProjectRoleMaintainer,
	ProjectActionTokens:        models.ProjectRoleViewer,
	ProjectActionUpdate:        models.ProjectRoleOwner,
	ProjectActionDelete:        models.ProjectRoleOwner,
	ProjectActionRepoCreate:    models.ProjectRoleMaintainer,
	ProjectActionRepoAdmin:     models.ProjectRoleMaintainer,
	ProjectActionRepoTransfer:  models.ProjectRoleOwner,
}

// ProjectDecision is the outcome of a project control-plane check.
type ProjectDecision int

const (
	// ProjectAllow: the action is permitted.
	ProjectAllow ProjectDecision = iota
	// ProjectForbidden: the caller sees the project (holds a role) but the role
	// is too low for the action (HTTP 403).
	ProjectForbidden
	// ProjectHidden: the caller has no role in the project, or the project does
	// not exist: both look the same (HTTP 404), so existence does not leak.
	ProjectHidden
)

// ProjectRoleAllows reports whether role (or admin, an implicit owner) may
// perform action. An unknown action is never allowed.
func ProjectRoleAllows(role string, admin bool, action ProjectAction) bool {
	minRole, ok := projectActionMinRole[action]
	if !ok {
		return false
	}

	if admin {
		return true
	}

	return models.ProjectRoleRank(role) >= models.ProjectRoleRank(minRole)
}

// controlPlaneBlocked reports whether the principal can never use the project
// control plane: anonymous callers and deploy tokens (repository credentials).
func controlPlaneBlocked(principal Principal) bool {
	return principal.IsAnonymous || principal.TokenKind == TokenKindDeploy
}

// ProjectRoles returns the principal's role per project ID (the highest of its
// recorded ownership, membership and group grants), without the implicit
// ownership of global admins. Anonymous callers, deploy tokens, scoped tokens
// and the system project yield nothing.
func (e *Evaluator) ProjectRoles(ctx context.Context, principal Principal) (map[string]string, error) {
	if controlPlaneBlocked(principal) {
		return nil, nil //nolint:nilnil // nil map: no roles
	}

	return e.projectRoles(ctx, principal)
}

// ProjectRole returns the principal's role in the project (empty for a
// non-member) and whether the principal is a global admin. The role never
// includes the admin's implicit ownership: callers combine both through
// ProjectRoleAllows. Anonymous callers, deploy tokens, scoped tokens and the
// system project (which grants nothing) have no role.
func (e *Evaluator) ProjectRole(ctx context.Context, principal Principal, projectID string) (string, bool, error) {
	if controlPlaneBlocked(principal) {
		return "", false, nil
	}

	admin, err := e.Evaluate(ctx, principal, "", "", "manage")
	if err != nil {
		return "", false, err
	}

	roles, err := e.ProjectRoles(ctx, principal)
	if err != nil {
		return "", false, err
	}

	return roles[projectID], admin, nil
}

// DecideProject answers a project control-plane check for the project with the
// given name: a global admin always passes (a missing project is then the
// handler's 404); anybody else needs a role that is high enough, and a caller
// without any role sees ProjectHidden whether or not the project exists.
func (e *Evaluator) DecideProject(ctx context.Context, principal Principal, projectName string, action ProjectAction) (ProjectDecision, error) {
	if controlPlaneBlocked(principal) {
		return ProjectHidden, nil
	}

	admin, err := e.Evaluate(ctx, principal, "", "", "manage")
	if err != nil {
		return ProjectHidden, err
	}

	if admin {
		return ProjectDecisionFor("", true, action), nil
	}

	project, err := e.db.GetProjectByName(ctx, projectName)
	if err != nil || project == nil {
		return ProjectHidden, err //nolint:wrapcheck // database error, logged by callers
	}

	roles, err := e.projectRoles(ctx, principal)
	if err != nil {
		return ProjectHidden, err
	}

	return ProjectDecisionFor(roles[project.ID], false, action), nil
}

// ProjectDecisionFor maps a role to the outcome for action: allowed, forbidden
// (a role that is too low) or hidden (no role at all).
func ProjectDecisionFor(role string, admin bool, action ProjectAction) ProjectDecision {
	switch {
	case ProjectRoleAllows(role, admin, action):
		return ProjectAllow
	case models.ProjectRoleRank(role) > 0:
		return ProjectForbidden
	default:
		return ProjectHidden
	}
}

// EvaluateProject reports whether the principal may perform action on the
// named project. A global admin may; so may a member whose role is high enough.
// An unknown project is never allowed (except to an admin, whose handler 404s).
func (e *Evaluator) EvaluateProject(ctx context.Context, principal Principal, projectName string, action ProjectAction) (bool, error) {
	decision, err := e.DecideProject(ctx, principal, projectName, action)

	return decision == ProjectAllow, err
}

// EvaluateRepoAdmin reports whether the principal may administer the named
// repository: configure or delete it, manage its cleanup policies and quota,
// empty its trash. A global admin may, and so may a maintainer or owner of the
// repository's project. A repository-scoped "manage" permission target does
// not qualify (it only covers what it always did), and neither does a project
// role pass Evaluate(..., "manage"). Anonymous callers, deploy tokens, scoped
// tokens and repositories of the system project never qualify except for a
// global admin; an unknown repository is never administrable.
func (e *Evaluator) EvaluateRepoAdmin(ctx context.Context, principal Principal, repoName string) (bool, error) {
	if controlPlaneBlocked(principal) {
		return false, nil
	}

	admin, err := e.Evaluate(ctx, principal, "", "", "manage")
	if err != nil || admin {
		return admin, err
	}

	roles, err := e.projectRoles(ctx, principal)
	if err != nil || len(roles) == 0 {
		return false, err
	}

	repo, err := e.db.GetRepositoryByName(ctx, repoName)
	if err != nil || repo == nil || repo.ProjectID == nil {
		return false, err //nolint:wrapcheck // database error, logged by callers
	}

	return ProjectRoleAllows(roles[*repo.ProjectID], false, ProjectActionRepoAdmin), nil
}
