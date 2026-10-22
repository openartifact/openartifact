// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Project-derived access.
//
// A user who holds a role in project P holds the data actions that role allows
// (projectRoleActions) on every repository whose project_id is P, independent of
// the repository's name or path. The role comes from, per project, the highest
// of:
//
//	the project's recorded owner      -> owner
//	a membership row                  -> its role
//	a group the user belongs to       -> the role the group holds in the project
//
// It never includes "manage". The built-in system project (models.SystemProjectID)
// is the "unassigned" project and grants nothing. The result is unioned with the
// permission targets (there are no deny rules). Anonymous principals and tokens
// that carry scopes get no project grant: scopes are permission-target IDs and
// only narrow the owner's targets.

// usesProjectGrants reports whether project-derived access applies to the principal.
func usesProjectGrants(principal Principal) bool {
	return principal.UserID != nil && !principal.IsAnonymous && !principal.IsLegacy && len(principal.TokenScopes) == 0
}

// projectRoles loads the principal's effective role per project ID. It returns
// nil when no project grant applies.
func (e *Evaluator) projectRoles(ctx context.Context, principal Principal) (map[string]string, error) {
	if !usesProjectGrants(principal) {
		return nil, nil //nolint:nilnil // nil map: no project grants
	}

	grants, err := e.db.ListProjectGrantsByUser(ctx, *principal.UserID)
	if err != nil {
		return nil, err //nolint:wrapcheck // database error, logged by callers
	}

	roles := make(map[string]string, len(grants))

	for _, g := range grants {
		if g.ProjectID == models.SystemProjectID || models.ProjectRoleRank(g.Role) == 0 {
			continue
		}

		// A service account belongs to one project only.
		if principal.ServiceProjectID != "" && g.ProjectID != principal.ServiceProjectID {
			continue
		}

		roles[g.ProjectID] = maxProjectRole(roles[g.ProjectID], g.Role)
	}

	return roles, nil
}

// projectAllows reports whether a project role of the principal allows action
// on the named repository.
func (e *Evaluator) projectAllows(ctx context.Context, principal Principal, repoName, action string) (bool, error) {
	if repoName == "" {
		return false, nil
	}

	switch action {
	case "read", "write", "delete":
	default:
		return false, nil
	}

	roles, err := e.projectRoles(ctx, principal)
	if err != nil || len(roles) == 0 {
		return false, err
	}

	repo, err := e.db.GetRepositoryByName(ctx, repoName)
	if err != nil || repo == nil || repo.ProjectID == nil {
		return false, err //nolint:wrapcheck // database error, logged by callers
	}

	role, ok := roles[*repo.ProjectID]

	return ok && projectRoleAllows(role, action), nil
}
