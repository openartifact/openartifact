// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// userColumns is the column list scanUser reads; queries select it FROM auth_users u.
const userColumns = `u.id, u.username, u.email, u.password_hash, u.full_name, u.disabled, u.must_change_password, u.last_login_at,
	u.created_at, u.updated_at, u.external_id, u.provider_slug, u.sso_managed_groups, u.kind, u.project_id`

// scanUser reads one row selected with userColumns.
func scanUser(row rowScanner) (*models.User, error) {
	u := &models.User{}

	var (
		email, passwordHash, fullName, externalID, providerSlug, userProject sql.NullString
		lastLoginAt                                                          sql.NullTime
	)

	if err := row.Scan(&u.ID, &u.Username, &email, &passwordHash, &fullName, &u.Disabled, &u.MustChangePassword, &lastLoginAt,
		&u.CreatedAt, &u.UpdatedAt, &externalID, &providerSlug, &u.SSOManagedGroups, &u.Kind, &userProject); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	u.Email = nullStringPtr(email)
	u.PasswordHash = nullStringPtr(passwordHash)
	u.FullName = nullStringPtr(fullName)
	u.ExternalID = nullStringPtr(externalID)
	u.ProviderSlug = nullStringPtr(providerSlug)
	u.ProjectID = nullStringPtr(userProject)

	if lastLoginAt.Valid {
		u.LastLoginAt = &lastLoginAt.Time
	}

	return u, nil
}

func (p *PostgresDB) CreatePermissionTarget(ctx context.Context, target *models.PermissionTarget) (*models.PermissionTarget, error) {
	row, err := p.q.CreatePermissionTarget(ctx, dbsqlc.CreatePermissionTargetParams{
		Name:             target.Name,
		RepositoryFilter: target.RepositoryFilter,
		PathPattern:      target.PathPattern,
		Actions:          target.Actions,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create permission target: %w", err)
	}

	target.ID = row.ID
	target.CreatedAt = row.CreatedAt
	target.UpdatedAt = row.UpdatedAt

	return target, nil
}

func permissionTargetFromRow(row dbsqlc.GetPermissionTargetByIDRow) *models.PermissionTarget {
	return &models.PermissionTarget{
		ID:               row.ID,
		Name:             row.Name,
		RepositoryFilter: row.RepositoryFilter,
		PathPattern:      row.PathPattern,
		Actions:          row.Actions,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func (p *PostgresDB) GetPermissionTargetByID(ctx context.Context, id string) (*models.PermissionTarget, error) {
	row, err := p.q.GetPermissionTargetByID(ctx, id)

	return mapRow(row, err, "get permission target by id", permissionTargetFromRow)
}

func (p *PostgresDB) GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error) {
	row, err := p.q.GetPermissionTargetByName(ctx, name)

	return mapRow(dbsqlc.GetPermissionTargetByIDRow(row), err, "get permission target by name", permissionTargetFromRow)
}

func (p *PostgresDB) UpdatePermissionTarget(ctx context.Context, target *models.PermissionTarget) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx,
		`UPDATE permission_targets SET name=$2, repository_filter=$3, path_pattern=$4, actions=$5 WHERE id=$1`,
		target.ID, target.Name, target.RepositoryFilter, target.PathPattern, target.Actions,
	)
	if err != nil {
		return fmt.Errorf("failed to update permission target: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeletePermissionTarget(ctx context.Context, id string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	err := p.q.DeletePermissionTarget(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete permission target: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListPermissionTargets(ctx context.Context) ([]*models.PermissionTarget, error) {
	rows, err := p.q.ListPermissionTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list permission targets: %w", err)
	}

	var targets []*models.PermissionTarget
	for _, row := range rows {
		targets = append(targets, &models.PermissionTarget{
			ID:               row.ID,
			Name:             row.Name,
			RepositoryFilter: row.RepositoryFilter,
			PathPattern:      row.PathPattern,
			Actions:          row.Actions,
			CreatedAt:        row.CreatedAt,
			UpdatedAt:        row.UpdatedAt,
		})
	}

	return targets, nil
}

func (p *PostgresDB) CreatePermissionAssignment(ctx context.Context, assignment *models.PermissionAssignment) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	err := p.q.CreatePermissionAssignment(ctx, dbsqlc.CreatePermissionAssignmentParams{
		PermissionTargetID: assignment.PermissionTargetID,
		PrincipalType:      assignment.PrincipalType,
		PrincipalID:        assignment.PrincipalID,
	})
	if err != nil {
		return fmt.Errorf("failed to create permission assignment: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeletePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	err := p.q.DeletePermissionAssignment(ctx, dbsqlc.DeletePermissionAssignmentParams{
		PermissionTargetID: targetID,
		PrincipalType:      principalType,
		PrincipalID:        principalID,
	})
	if err != nil {
		return fmt.Errorf("failed to delete permission assignment: %w", err)
	}

	return nil
}

func (p *PostgresDB) GetPermissionTargetsForPrincipalUncached(
	ctx context.Context,
	userID string,
	groupIDs []string,
) ([]*models.PermissionTarget, error) {
	rows, err := p.q.GetPermissionTargetsForPrincipal(ctx, dbsqlc.GetPermissionTargetsForPrincipalParams{
		PrincipalID: userID,
		GroupIds:    groupIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get permission targets for principal: %w", err)
	}

	var targets []*models.PermissionTarget
	for _, row := range rows {
		targets = append(targets, &models.PermissionTarget{
			ID:               row.ID,
			Name:             row.Name,
			RepositoryFilter: row.RepositoryFilter,
			PathPattern:      row.PathPattern,
			Actions:          row.Actions,
			CreatedAt:        row.CreatedAt,
			UpdatedAt:        row.UpdatedAt,
		})
	}

	return targets, nil
}
