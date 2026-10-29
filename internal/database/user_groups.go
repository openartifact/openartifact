// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

// UserGroupStore owns user groups and their memberships.
type UserGroupStore struct{ *core }

// ListGroupsByUser returns the groups of a user, from the auth cache when fresh.
func (p *UserGroupStore) ListGroupsByUser(ctx context.Context, userID string) ([]*models.Group, error) {
	return cachedList(ctx, &p.authCache, &p.authCache.groups, userID, cloneShallowSlice, p.ListGroupsByUserUncached)
}

func (p *UserGroupStore) CreateGroup(ctx context.Context, group *models.Group) (*models.Group, error) {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO auth_groups (name, description) VALUES ($1, $2) RETURNING id, created_at, updated_at`,
		group.Name, group.Description,
	).Scan(&group.ID, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create group: %w", err)
	}

	return group, nil
}

func (p *UserGroupStore) GetGroupByID(ctx context.Context, id string) (*models.Group, error) {
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
func (p *UserGroupStore) getGroup(ctx context.Context, column, value string) (*models.Group, error) {
	g, err := scanGroup(p.db.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM auth_groups g WHERE g.`+column+` = $1`, value))

	return orNotFound(g, err, "get group by "+column)
}

func (p *UserGroupStore) UpdateGroup(ctx context.Context, group *models.Group) error {
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

func (p *UserGroupStore) DeleteGroup(ctx context.Context, id string) error {
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

func (p *UserGroupStore) ListGroups(ctx context.Context) ([]*models.Group, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+groupColumns+` FROM auth_groups g ORDER BY g.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}

	return collectRows(rows, "failed to scan group", "failed to iterate groups", scanGroup)
}

func (p *UserGroupStore) AddUserToGroup(ctx context.Context, userID, groupID string) error {
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

func (p *UserGroupStore) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
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

func (p *UserGroupStore) ListGroupsByUserUncached(ctx context.Context, userID string) ([]*models.Group, error) {
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

func (p *UserGroupStore) ListUsersByGroup(ctx context.Context, groupID string) ([]*models.User, error) {
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

// ListUserGroupMemberships returns every user-to-group membership, including
// the ones an SSO login manages.
func (p *UserGroupStore) ListUserGroupMemberships(ctx context.Context) ([]*models.UserGroup, error) {
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

func (p *UserGroupStore) SyncSSOGroups(ctx context.Context, userID string, groupIDs []string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`DELETE FROM auth_user_groups WHERE user_id=$1 AND is_sso_managed=TRUE`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete sso-managed groups: %w", err)
	}

	for _, groupID := range groupIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO auth_user_groups (user_id, group_id, is_sso_managed) VALUES ($1, $2, TRUE) ON CONFLICT DO NOTHING`,
			userID, groupID,
		)
		if err != nil {
			return fmt.Errorf("failed to insert sso group membership: %w", err)
		}
	}

	if err := revokeExcessProjectTokens(ctx, tx, ""); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit sso group sync: %w", err)
	}

	return nil
}

func (p *UserGroupStore) GetGroupByName(ctx context.Context, name string) (*models.Group, error) {
	return p.getGroup(ctx, "name", name)
}

func (p *UserGroupStore) GetOrCreateGroup(ctx context.Context, name string) (*models.Group, error) {
	existing, err := p.GetGroupByName(ctx, name)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		return existing, nil
	}

	now := time.Now().UTC()
	desc := "Auto-created by SSO group sync"
	g := &models.Group{
		ID:          uuid.New().String(),
		Name:        name,
		Description: &desc,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	return p.CreateGroup(ctx, g)
}
