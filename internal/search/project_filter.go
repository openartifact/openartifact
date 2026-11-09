// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// validProjectRole reports whether role is a project role project_role accepts.
func validProjectRole(role string) bool {
	return models.ProjectRoleRank(role) > 0
}

// narrowToProjects returns the repositories of readable that the project filters
// keep. It never adds a repository: a repository stays only when the caller can
// already read it, its project matches filter.Projects (name or slug) when that is
// set, and the caller's effective role in that project is one of
// filter.ProjectRoles when that is set.
//
// The effective role is the one the projects API reports as my_role: the highest of
// recorded ownership, membership and group grants (auth.Evaluator.ProjectRoles), and
// owner in every project for a global admin. So project_role=viewer finds nothing for
// an admin, deliberately. A caller without a role in a project keeps none of its
// repositories, even ones readable through a permission target or anonymous access,
// which makes a project that does not exist and one the caller has no role in give
// the same empty answer. The system project grants nothing and never matches.
//
// readable may be shared with other requests (see resolveReadableRepos), so the
// result is a new map.
func (h *Handler) narrowToProjects(
	ctx context.Context,
	principal *auth.Principal,
	readable map[uuid.UUID]struct{},
	filter SearchFilter,
) (map[uuid.UUID]struct{}, error) {
	kept := map[uuid.UUID]struct{}{}
	if principal == nil || len(readable) == 0 {
		return kept, nil
	}

	_, admin, err := h.evaluator.ProjectRole(ctx, *principal, "")
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	roles, err := h.evaluator.ProjectRoles(ctx, *principal)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	if !admin && len(roles) == 0 {
		return kept, nil
	}

	rows, err := h.db.QueryContext(ctx,
		`SELECT r.id, r.project_id, p.name, p.slug FROM repositories r
		 JOIN projects p ON p.id = r.project_id
		 WHERE r.deleted_at IS NULL AND r.project_id <> $1`, models.SystemProjectID)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var rawID, projectID, name, slug string
		if err := rows.Scan(&rawID, &projectID, &name, &slug); err != nil {
			return nil, err //nolint:wrapcheck // logged by the caller
		}

		id, err := uuid.Parse(rawID)
		if err != nil {
			continue
		}

		if _, ok := readable[id]; !ok {
			continue
		}

		role := roles[projectID]
		if admin {
			role = models.ProjectRoleOwner
		}

		if keepForProject(filter, role, name, slug) {
			kept[id] = struct{}{}
		}
	}

	return kept, rows.Err() //nolint:wrapcheck // logged by the caller
}

// keepForProject applies the project filters to one repository's project, given the
// caller's effective role there (empty for none).
func keepForProject(filter SearchFilter, role, name, slug string) bool {
	if role == "" {
		return false
	}

	if len(filter.Projects) > 0 && !slices.Contains(filter.Projects, name) && !slices.Contains(filter.Projects, slug) {
		return false
	}

	return len(filter.ProjectRoles) == 0 || slices.Contains(filter.ProjectRoles, role)
}
