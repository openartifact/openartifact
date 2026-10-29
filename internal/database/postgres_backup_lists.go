// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Bulk readers for the backup engine: authorization and quota state that has
// no per-parent list method.

// ListPermissionAssignments returns every permission assignment (user and
// group principals alike).
func (p *PostgresDB) ListPermissionAssignments(ctx context.Context) ([]*models.PermissionAssignment, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, permission_target_id, principal_type, principal_id, created_at
		   FROM permission_assignments ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list permission assignments: %w", err)
	}

	return collectRows(
		rows,
		"failed to scan permission assignment",
		"failed to list permission assignments",
		func(row rowScanner) (*models.PermissionAssignment, error) {
			v := &models.PermissionAssignment{}

			return v, row.Scan(
				&v.ID,
				&v.PermissionTargetID,
				&v.PrincipalType,
				&v.PrincipalID,
				&v.CreatedAt,
			) //nolint:wrapcheck // collectRows wraps
		},
	)
}

// ListUserGroupMemberships returns every user-to-group membership, including
// the ones an SSO login manages.
func (p *PostgresDB) ListUserGroupMemberships(ctx context.Context) ([]*models.UserGroup, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT user_id, group_id, is_sso_managed FROM auth_user_groups ORDER BY group_id, user_id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list user group memberships: %w", err)
	}

	return collectRows(
		rows,
		"failed to scan user group membership",
		"failed to list user group memberships",
		func(row rowScanner) (*models.UserGroup, error) {
			v := &models.UserGroup{}

			return v, row.Scan(&v.UserID, &v.GroupID, &v.SSOManaged) //nolint:wrapcheck // collectRows wraps
		},
	)
}
