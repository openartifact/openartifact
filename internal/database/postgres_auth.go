// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
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

func (p *PostgresDB) CreateGroup(ctx context.Context, group *models.Group) (*models.Group, error) {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO auth_groups (name, description) VALUES ($1, $2) RETURNING id, created_at, updated_at`,
		group.Name, group.Description,
	).Scan(&group.ID, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create group: %w", err)
	}

	return group, nil
}

func (p *PostgresDB) GetGroupByID(ctx context.Context, id string) (*models.Group, error) {
	return p.getGroup(ctx, "id", id)
}

// groupColumns is the column list scanGroup reads; queries select it FROM auth_groups g.
const groupColumns = `g.id, g.name, g.description, g.created_at, g.updated_at`

// scanGroup reads one row selected with groupColumns.
func scanGroup(row rowScanner) (*models.Group, error) {
	g := &models.Group{}

	var description sql.NullString

	if err := row.Scan(&g.ID, &g.Name, &description, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	g.Description = nullStringPtr(description)

	return g, nil
}

// getGroup reads one auth group by its id or name column.
func (p *PostgresDB) getGroup(ctx context.Context, column, value string) (*models.Group, error) {
	g, err := scanGroup(p.db.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM auth_groups g WHERE g.`+column+` = $1`, value))

	return orNotFound(g, err, "get group by "+column)
}

func (p *PostgresDB) UpdateGroup(ctx context.Context, group *models.Group) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx,
		`UPDATE auth_groups SET name=$2, description=$3 WHERE id=$1`,
		group.ID, group.Name, group.Description,
	)
	if err != nil {
		return fmt.Errorf("failed to update group: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteGroup(ctx context.Context, id string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	// permission_assignments.principal_id is polymorphic (no FK), so a group's
	// assignments must be removed explicitly in the same transaction.
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin delete group tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM permission_assignments WHERE principal_type = 'group' AND principal_id = $1`, id,
	); err != nil {
		return fmt.Errorf("failed to delete group permission assignments: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_groups WHERE id=$1`, id); err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}

	if err := revokeExcessProjectTokens(ctx, tx, ""); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit delete group: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListGroups(ctx context.Context) ([]*models.Group, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+groupColumns+` FROM auth_groups g ORDER BY g.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}

	return collectRows(rows, "failed to scan group", "failed to iterate groups", scanGroup)
}

func (p *PostgresDB) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx,
		`INSERT INTO auth_user_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		userID, groupID,
	)
	if err != nil {
		return fmt.Errorf("failed to add user to group: %w", err)
	}

	return nil
}

func (p *PostgresDB) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin remove user from group: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM auth_user_groups WHERE user_id=$1 AND group_id=$2`,
		userID, groupID,
	); err != nil {
		return fmt.Errorf("failed to remove user from group: %w", err)
	}

	// Project roles granted through the group go with the membership.
	if err := revokeExcessProjectTokens(ctx, tx, ""); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit remove user from group: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListGroupsByUserUncached(ctx context.Context, userID string) ([]*models.Group, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+groupColumns+`
		FROM auth_groups g
		JOIN auth_user_groups ug ON ug.group_id = g.id
		WHERE ug.user_id = $1
		ORDER BY g.name ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups by user: %w", err)
	}

	return collectRows(rows, "failed to scan group", "failed to iterate groups", scanGroup)
}

func (p *PostgresDB) ListUsersByGroup(ctx context.Context, groupID string) ([]*models.User, error) {
	rows, err := p.db.QueryContext(
		ctx,
		`SELECT `+userColumns+`
		FROM auth_users u
		JOIN auth_user_groups ug ON ug.user_id = u.id
		WHERE ug.group_id = $1
		ORDER BY u.username ASC`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list users by group: %w", err)
	}

	return collectRows(rows, "failed to scan user", "failed to iterate users", scanUser)
}

func (p *PostgresDB) CreateToken(ctx context.Context, token *models.Token) (*models.Token, error) {
	scopesJSON, err := json.Marshal(token.Scopes)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal token scopes: %w", err)
	}

	err = p.db.QueryRowContext(ctx,
		`INSERT INTO auth_tokens (user_id, name, kind, token_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		RETURNING id, created_at`,
		token.UserID, token.Name, token.Kind, token.TokenHash, scopesJSON, token.ExpiresAt,
	).Scan(&token.ID, &token.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create token: %w", err)
	}

	return token, nil
}

// getTokenWhere returns the single token matching the WHERE clause, or nil when there is none.
func (p *PostgresDB) getTokenWhere(ctx context.Context, what, where string, arg any) (*models.Token, error) {
	t, err := scanToken(p.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE `+where, arg))

	return orNotFound(t, err, "get token by "+what)
}

func (p *PostgresDB) GetTokenByHashUncached(ctx context.Context, hash string) (*models.Token, error) {
	return p.getTokenWhere(ctx, "hash", "t.token_hash = $1", hash)
}

func (p *PostgresDB) GetTokenByID(ctx context.Context, tokenID string) (*models.Token, error) {
	return p.getTokenWhere(ctx, "id", "t.id = $1", tokenID)
}

func (p *PostgresDB) RevokeToken(ctx context.Context, tokenID string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx,
		`WITH r AS (UPDATE auth_tokens SET revoked_at=now() WHERE id=$1 RETURNING user_id)
		UPDATE auth_users SET disabled = true WHERE kind = 'service' AND id IN (SELECT user_id FROM r)`,
		tokenID,
	)
	if err != nil {
		return fmt.Errorf("failed to revoke token: %w", err)
	}

	return nil
}

func (p *PostgresDB) UpdateTokenLastUsed(ctx context.Context, tokenID string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE auth_tokens SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now() - interval '60 seconds')`,
		tokenID,
	)
	if err != nil {
		return fmt.Errorf("failed to update token last used: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListTokensByUser(ctx context.Context, userID string) ([]*models.Token, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE t.user_id=$1 ORDER BY t.created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list tokens by user: %w", err)
	}

	return collectRows(rows, "failed to scan token", "failed to iterate tokens", scanToken)
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
