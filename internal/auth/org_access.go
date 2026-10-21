// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Organization-derived access.
//
// A user who belongs to organization O (a membership row, or O's recorded
// owner) holds an access level on every repository whose org_id is O,
// independent of the repository's name or path:
//
//	owner / admin member  -> admin (read, write, delete)
//	plain member          -> the organization's member_role (read | write | admin)
//
// It never includes "manage": org roles do not make anybody a global admin.
// The built-in system organization (models.SystemOrgID) is the legacy default
// organization and grants nothing. The result is unioned with the permission
// targets (there are no deny rules). Anonymous principals and tokens that
// carry scopes get no organization grant: scopes are permission-target IDs
// and only narrow the owner's targets.

// orgLevelActions maps an access level to the actions it allows.
//
//nolint:gochecknoglobals // immutable lookup table
var orgLevelActions = map[string][]string{
	models.OrgAccessRead:  {"read"},
	models.OrgAccessWrite: {"read", "write"},
	models.OrgAccessAdmin: {"read", "write", "delete"},
}

// orgGrantLevel resolves the access level a membership gives, or "" when it
// gives none (system organization, unknown role/level).
func orgGrantLevel(g *models.OrgGrant) string {
	if g.OrgID == models.SystemOrgID {
		return ""
	}

	level := g.MemberRole

	if g.Role == "owner" || g.Role == "admin" {
		level = models.OrgAccessAdmin
	}

	if _, ok := orgLevelActions[level]; !ok {
		return ""
	}

	return level
}

func orgLevelAllows(level, action string) bool {
	return slices.Contains(orgLevelActions[level], action)
}

// usesOrgGrants reports whether org-derived access applies to the principal.
func usesOrgGrants(principal Principal) bool {
	return principal.UserID != nil && !principal.IsAnonymous && !principal.IsLegacy && len(principal.TokenScopes) == 0
}

// orgLevels loads the principal's organization access levels keyed by
// organization ID. It returns nil when no organization grant applies.
func (e *Evaluator) orgLevels(ctx context.Context, principal Principal) (map[string]string, error) {
	if !usesOrgGrants(principal) {
		return nil, nil //nolint:nilnil // nil map: no organization grants
	}

	grants, err := e.db.ListOrgGrantsByUser(ctx, *principal.UserID)
	if err != nil {
		return nil, err //nolint:wrapcheck // database error, logged by callers
	}

	levels := make(map[string]string, len(grants))

	for _, g := range grants {
		if level := orgGrantLevel(g); level != "" {
			levels[g.OrgID] = level
		}
	}

	return levels, nil
}

// orgAllows reports whether an organization membership of the principal
// allows action on the named repository.
func (e *Evaluator) orgAllows(ctx context.Context, principal Principal, repoName, action string) (bool, error) {
	if repoName == "" {
		return false, nil
	}

	switch action {
	case "read", "write", "delete":
	default:
		return false, nil
	}

	levels, err := e.orgLevels(ctx, principal)
	if err != nil || len(levels) == 0 {
		return false, err
	}

	repo, err := e.db.GetRepositoryByName(ctx, repoName)
	if err != nil || repo == nil || repo.OrgID == nil {
		return false, err //nolint:wrapcheck // database error, logged by callers
	}

	level, ok := levels[*repo.OrgID]

	return ok && orgLevelAllows(level, action), nil
}
