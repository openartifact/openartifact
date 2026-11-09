// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// validProjectRole reports whether role is a project role project_role accepts.
func validProjectRole(role string) bool {
	return models.ProjectRoleRank(role) > 0
}

// Errors of CheckProjectFilters.
var (
	ErrUnknownProjectRole    = errors.New("project_role must be viewer, developer, maintainer or owner")
	ErrTooManyProjectFilters = errors.New("too many project or project_role filters")
)

// CheckProjectFilters applies the rules GET /api/v1/search answers with
// 400 invalid_filter to the project filters, so the MCP search tool enforces the same
// ones: every project_role is a project role, and there are at most maxFilterValues
// project and project_role values.
func CheckProjectFilters(f SearchFilter) error {
	for _, role := range f.ProjectRoles {
		if !validProjectRole(role) {
			return ErrUnknownProjectRole
		}
	}

	if len(f.Projects) > maxFilterValues || len(f.ProjectRoles) > maxFilterValues {
		return ErrTooManyProjectFilters
	}

	return nil
}

// ProjectScope applies the project: and project_role: filters of a search for the
// caller of the request context. The HTTP search and the MCP search tool share it.
type ProjectScope struct {
	db        *sql.DB
	evaluator *auth.Evaluator
}

// NewProjectScope returns a ProjectScope that reads repositories from db and the
// caller's project roles through evaluator.
func NewProjectScope(db *sql.DB, evaluator *auth.Evaluator) *ProjectScope {
	return &ProjectScope{db: db, evaluator: evaluator}
}

// callerRoles is the caller's effective role per project ID, the one the projects
// API reports as my_role: recorded ownership, membership and group grants
// (auth.Evaluator.ProjectRoles, nothing for scoped tokens and the system project),
// and owner in every project for a global admin. Anonymous callers and deploy tokens
// have no project role at all, as on the projects API.
type callerRoles struct {
	admin bool
	roles map[string]string
}

func (c callerRoles) any() bool { return c.admin || len(c.roles) > 0 }

func (s *ProjectScope) callerRoles(ctx context.Context) (callerRoles, error) {
	p := middleware.PrincipalFromContext(ctx)
	if p == nil || p.IsAnonymous || p.TokenKind == auth.TokenKindDeploy {
		return callerRoles{}, nil
	}

	admin, err := s.evaluator.Evaluate(ctx, *p, "", "", "manage")
	if err != nil {
		return callerRoles{}, err //nolint:wrapcheck // logged by the caller
	}

	roles, err := s.evaluator.ProjectRoles(ctx, *p)
	if err != nil {
		return callerRoles{}, err //nolint:wrapcheck // logged by the caller
	}

	return callerRoles{admin: admin, roles: roles}, nil
}

// UsesProjects reports whether a project filter can match anything for the caller:
// whether it holds a role in some project. Only then is project:<project> worth
// suggesting.
func (s *ProjectScope) UsesProjects(ctx context.Context) (bool, error) {
	c, err := s.callerRoles(ctx)

	return c.any(), err
}

// NarrowToProjects returns the repositories of readable that the project filters
// keep, and whether the caller holds a project role (UsesProjects), which it has
// resolved anyway, so a too-broad answer need not resolve it again. It never adds a repository: a repository stays only when the caller can
// already read it, its project matches filter.Projects (name or slug) when that is
// set, and the caller's effective role there (see callerRoles) is one of
// filter.ProjectRoles when that is set.
//
// So project_role=viewer finds nothing for an admin, deliberately. A caller without
// a role in a project keeps none of its repositories, even ones readable through a
// permission target or anonymous access, which makes a project that does not exist
// and one the caller has no role in give the same empty answer. The system project
// grants nothing and never matches.
//
// readable may be shared with other requests (the HTTP handler caches it), so it is
// never modified; the result is a new map.
func (s *ProjectScope) NarrowToProjects(
	ctx context.Context,
	readable map[uuid.UUID]struct{},
	filter SearchFilter,
) (map[uuid.UUID]struct{}, bool, error) {
	kept := map[uuid.UUID]struct{}{}

	c, err := s.callerRoles(ctx)
	if err != nil {
		return nil, false, err
	}

	if len(readable) == 0 || !c.any() {
		return kept, c.any(), nil
	}

	query, args := projectReposQuery(c, filter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err //nolint:wrapcheck // logged by the caller
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var rawID, projectID, name, slug string
		if err := rows.Scan(&rawID, &projectID, &name, &slug); err != nil {
			return nil, false, err //nolint:wrapcheck // logged by the caller
		}

		id, err := uuid.Parse(rawID)
		if err != nil {
			continue
		}

		if _, ok := readable[id]; !ok {
			continue
		}

		role := c.roles[projectID]
		if c.admin {
			role = models.ProjectRoleOwner
		}

		if keepForProject(filter, role, name, slug) {
			kept[id] = struct{}{}
		}
	}

	return kept, true, rows.Err() //nolint:wrapcheck // logged by the caller
}

// projectReposQuery selects the live repositories outside the system project that
// can match: for a non-admin only those of the caller's projects, and only those of
// the named projects when filter.Projects is set.
func projectReposQuery(c callerRoles, filter SearchFilter) (string, []any) {
	var b strings.Builder

	b.WriteString(`SELECT r.id, r.project_id, p.name, p.slug FROM repositories r
		JOIN projects p ON p.id = r.project_id
		WHERE r.deleted_at IS NULL AND r.project_id <> $1`)

	args := []any{models.SystemProjectID}

	if !c.admin {
		ids := make([]string, 0, len(c.roles))
		for id := range c.roles {
			ids = append(ids, id)
		}

		// The executor passes UUID sets the same way (whereBuilder.addReadable).
		args = append(args, "{"+strings.Join(ids, ",")+"}")
		b.WriteString(" AND r.project_id = ANY($" + strconv.Itoa(len(args)) + "::uuid[])")
	}

	if len(filter.Projects) > 0 {
		args = append(args, filter.Projects)
		n := strconv.Itoa(len(args))
		b.WriteString(" AND (p.name = ANY($" + n + "::text[]) OR p.slug = ANY($" + n + "::text[]))")
	}

	return b.String(), args
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
