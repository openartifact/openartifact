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

	defer func() { _ = rows.Close() }()

	var out []*models.PermissionAssignment

	for rows.Next() {
		a := &models.PermissionAssignment{}
		if err := rows.Scan(&a.ID, &a.PermissionTargetID, &a.PrincipalType, &a.PrincipalID, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan permission assignment: %w", err)
		}

		out = append(out, a)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list permission assignments: %w", err)
	}

	return out, nil
}

// ListUserGroupMemberships returns every user-to-group membership, including
// the ones an SSO login manages.
func (p *PostgresDB) ListUserGroupMemberships(ctx context.Context) ([]*models.UserGroup, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT user_id, group_id, is_sso_managed FROM auth_user_groups ORDER BY group_id, user_id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list user group memberships: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []*models.UserGroup

	for rows.Next() {
		m := &models.UserGroup{}
		if err := rows.Scan(&m.UserID, &m.GroupID, &m.SSOManaged); err != nil {
			return nil, fmt.Errorf("failed to scan user group membership: %w", err)
		}

		out = append(out, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list user group memberships: %w", err)
	}

	return out, nil
}

// ListStorageQuotas returns every repository's quota row.
func (p *PostgresDB) ListStorageQuotas(ctx context.Context) ([]*models.StorageQuota, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT repository_id, max_size_bytes, max_artifact_count, warn_threshold_percent
		   FROM storage_quotas ORDER BY repository_id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list storage quotas: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []*models.StorageQuota

	for rows.Next() {
		q := &models.StorageQuota{}
		if err := rows.Scan(&q.RepositoryID, &q.MaxSizeBytes, &q.MaxArtifactCount, &q.WarnThresholdPercent); err != nil {
			return nil, fmt.Errorf("failed to scan storage quota: %w", err)
		}

		out = append(out, q)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list storage quotas: %w", err)
	}

	return out, nil
}
