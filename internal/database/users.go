// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// UserStore owns user accounts, passwords and the bootstrap admin.
type UserStore struct{ *core }

// GetUserByID returns the user with the given ID, from the auth cache when fresh.
func (p *UserStore) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	return cachedPtr(ctx, &p.authCache, &p.authCache.users, id, cloneShallow, p.GetUserByIDUncached)
}

func (p *UserStore) CreateUser(ctx context.Context, user *models.User) (*models.User, error) {
	err := p.db.QueryRowContext(
		ctx,
		`INSERT INTO auth_users (username, email, password_hash, full_name, disabled, must_change_password, external_id,
			provider_slug, sso_managed_groups)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`,
		user.Username,
		user.Email,
		user.PasswordHash,
		user.FullName,
		user.Disabled,
		user.MustChangePassword,
		user.ExternalID,
		user.ProviderSlug,
		user.SSOManagedGroups,
	).Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

// getUserWhere returns the single user matching the WHERE clause, or nil when
// there is none. what names the lookup in the error message.
func (p *UserStore) getUserWhere(ctx context.Context, what, where string, args ...any) (*models.User, error) {
	u, err := scanUser(p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM auth_users u WHERE `+where, args...))

	return orNotFound(u, err, "get user by "+what)
}

func (p *UserStore) GetUserByIDUncached(ctx context.Context, id string) (*models.User, error) {
	return p.getUserWhere(ctx, "id", "u.id = $1", id)
}

func (p *UserStore) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	return p.getUserWhere(ctx, "username", "u.username = $1", username)
}

func (p *UserStore) UpdateUser(ctx context.Context, user *models.User) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(
		ctx,
		`UPDATE auth_users SET username=$2, email=$3, password_hash=$4, full_name=$5, disabled=$6, must_change_password=$7,
			last_login_at=$8, external_id=$9, provider_slug=$10, sso_managed_groups=$11,
		password_expires_at = CASE WHEN password_hash IS DISTINCT FROM $4 THEN NULL ELSE password_expires_at END
		WHERE id=$1`,
		user.ID,
		user.Username,
		user.Email,
		user.PasswordHash,
		user.FullName,
		user.Disabled,
		user.MustChangePassword,
		user.LastLoginAt,
		user.ExternalID,
		user.ProviderSlug,
		user.SSOManagedGroups,
	)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	// A disabled creator loses the project tokens it minted, in the same transaction.
	if user.Disabled {
		if err := revokeProjectTokensOf(ctx, tx, user.ID, ""); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	return nil
}

// UpdateUserLastLogin sets only last_login_at, so a login finishing from a
// stale snapshot cannot undo a concurrent disable or password reset.
func (p *UserStore) UpdateUserLastLogin(ctx context.Context, id string, at time.Time) error {
	if _, err := p.db.ExecContext(ctx, `UPDATE auth_users SET last_login_at=$2 WHERE id=$1`, id, at); err != nil {
		return fmt.Errorf("failed to update user last login: %w", err)
	}

	return nil
}

// UpdateUserSSOProfile sets only email (when non-nil), full_name (when
// non-nil) and last_login_at, so an SSO login finishing from a stale
// snapshot cannot undo a concurrent disable or password reset.
func (p *UserStore) UpdateUserSSOProfile(ctx context.Context, id string, email, fullName *string, lastLogin time.Time) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	if _, err := p.db.ExecContext(ctx,
		`UPDATE auth_users SET email=COALESCE($2, email), full_name=COALESCE($3, full_name), last_login_at=$4 WHERE id=$1`,
		id, email, fullName, lastLogin,
	); err != nil {
		return fmt.Errorf("failed to update user sso profile: %w", err)
	}

	return nil
}

// UpdateUserPassword sets only password_hash and must_change_password.
func (p *UserStore) UpdateUserPassword(ctx context.Context, id, passwordHash string, mustChange bool) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	if _, err := p.db.ExecContext(
		ctx,
		`UPDATE auth_users SET password_hash=$2, must_change_password=$3, password_expires_at=NULL WHERE id=$1`,
		id,
		passwordHash,
		mustChange,
	); err != nil {
		return fmt.Errorf("failed to update user password: %w", err)
	}

	return nil
}

func (p *UserStore) ListUsers(ctx context.Context, page, perPage int) ([]*models.User, int, error) {
	offset := (page - 1) * perPage

	var total int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE kind = 'human'`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count users: %w", err)
	}

	rows, err := p.db.QueryContext(
		ctx,
		`SELECT `+userColumns+`
		FROM auth_users u WHERE u.kind = 'human' ORDER BY u.created_at ASC, u.id LIMIT $1 OFFSET $2`,
		perPage,
		offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list users: %w", err)
	}

	users, err := collectRows(rows, "failed to scan user", "failed to iterate users", scanUser)
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (p *UserStore) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE kind = 'human'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count users: %w", err)
	}

	return count, nil
}

// bootstrapAdvisoryLockKey serialises first-boot admin creation across replicas.
const bootstrapAdvisoryLockKey = "openartifact:auth-bootstrap"

// adminDeleteAdvisoryLockKey serialises user deletion so the last-admin guard holds.
const adminDeleteAdvisoryLockKey = "openartifact:auth-user-delete"

// enabledAdminIDsSQL selects the enabled human users holding admin-all, either
// by a direct assignment or through a group.
const enabledAdminIDsSQL = `
	SELECT u.id FROM auth_users u
	WHERE NOT u.disabled AND u.kind = 'human' AND EXISTS (
		SELECT 1 FROM permission_assignments pa
		JOIN permission_targets pt ON pt.id = pa.permission_target_id
		WHERE pt.name = 'admin-all' AND (
			(pa.principal_type = 'user' AND pa.principal_id = u.id)
			OR (pa.principal_type = 'group' AND pa.principal_id IN
				(SELECT ug.group_id FROM auth_user_groups ug WHERE ug.user_id = u.id))))`

// DeleteUser hard-deletes a human user. Sessions, tokens, project
// memberships, group links and the SSO link (external_id/provider_slug live on
// the row) go with it; creator/owner references are set to NULL by their
// foreign keys. The direct permission assignments carry no foreign key and are
// removed explicitly. It returns false when the user does not exist and
// ErrLastAdmin when the user is the last enabled admin.
func (p *UserStore) DeleteUser(ctx context.Context, id string) (bool, error) {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin delete user tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	// Serialise with other deletes so two admins cannot each remove "the other" admin.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, adminDeleteAdvisoryLockKey); err != nil {
		return false, fmt.Errorf("delete user advisory lock: %w", err)
	}

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM auth_users WHERE id = $1 AND kind = 'human')`, id).
		Scan(&exists); err != nil {
		return false, fmt.Errorf("delete user lookup: %w", err)
	}

	if !exists {
		return false, nil
	}

	var isAdmin, others bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM (`+enabledAdminIDsSQL+`) a WHERE a.id = $1),
		        EXISTS (SELECT 1 FROM (`+enabledAdminIDsSQL+`) a WHERE a.id <> $1)`, id,
	).Scan(&isAdmin, &others); err != nil {
		return false, fmt.Errorf("delete user admin check: %w", err)
	}

	if isAdmin && !others {
		return false, ErrLastAdmin
	}

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM permission_assignments WHERE principal_type = 'user' AND principal_id = $1`,
		id,
	); err != nil {
		return false, fmt.Errorf("delete user assignments: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_users WHERE id = $1`, id); err != nil {
		return false, fmt.Errorf("delete user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit delete user: %w", err)
	}

	return true, nil
}

// CreateBootstrapAdmin creates the first admin user, binds it to the
// admin-all permission target and creates its optional access token in one
// transaction guarded by an advisory lock. It returns false (and creates
// nothing) when a user already exists, which is what a replica that lost the
// first-boot race observes.
func (p *UserStore) CreateBootstrapAdmin(ctx context.Context, spec BootstrapAdmin) (bool, error) {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin bootstrap tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, bootstrapAdvisoryLockKey); err != nil {
		return false, fmt.Errorf("bootstrap advisory lock: %w", err)
	}

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users`).Scan(&count); err != nil {
		return false, fmt.Errorf("bootstrap count users: %w", err)
	}

	if count > 0 {
		return false, nil
	}

	var userID string
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO auth_users (username, password_hash, must_change_password, password_expires_at)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		spec.Username, spec.PasswordHash, spec.MustChangePassword, spec.PasswordExpiresAt,
	).Scan(&userID); err != nil {
		return false, fmt.Errorf("bootstrap create admin user: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO permission_assignments (permission_target_id, principal_type, principal_id)
		SELECT id, 'user', $1 FROM permission_targets WHERE name = 'admin-all'
		ON CONFLICT (permission_target_id, principal_type, principal_id) DO NOTHING`,
		userID,
	)
	if err != nil {
		return false, fmt.Errorf("bootstrap assign admin-all: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return false, fmt.Errorf( //nolint:err113 // dynamic error with runtime context
			"bootstrap assign admin-all: permission target %q not found",
			"admin-all",
		)
	}

	if spec.TokenHash != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO auth_tokens (user_id, name, kind, token_hash, scopes, expires_at)
			VALUES ($1, $2, 'access', $3, '[]'::jsonb, $4)`,
			userID, spec.TokenName, spec.TokenHash, spec.TokenExpiresAt,
		); err != nil {
			return false, fmt.Errorf("bootstrap create admin token: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit bootstrap tx: %w", err)
	}

	return true, nil
}

// GetPasswordExpiry returns the password expiry of a user that must still
// change its password; nil when there is none.
func (p *UserStore) GetPasswordExpiry(ctx context.Context, userID string) (*time.Time, error) {
	var at sql.NullTime

	err := p.db.QueryRowContext(ctx,
		`SELECT password_expires_at FROM auth_users WHERE id = $1 AND must_change_password`,
		userID,
	).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no expiry applies
	}

	if err != nil {
		return nil, fmt.Errorf("get password expiry: %w", err)
	}

	if !at.Valid {
		return nil, nil //nolint:nilnil // never expires
	}

	return &at.Time, nil
}

// RepairBootstrapAdmin re-binds the named local admin user to admin-all when no
// principal at all holds that target, which is the state an interrupted
// bootstrap leaves behind. It never touches an instance where some principal
// already holds admin-all.
func (p *UserStore) RepairBootstrapAdmin(ctx context.Context, username string) (bool, error) {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin bootstrap repair tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, bootstrapAdvisoryLockKey); err != nil {
		return false, fmt.Errorf("bootstrap advisory lock: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO permission_assignments (permission_target_id, principal_type, principal_id)
		SELECT pt.id, 'user', u.id
		FROM permission_targets pt, auth_users u
		WHERE pt.name = 'admin-all'
		  AND u.username = $1 AND u.external_id IS NULL AND u.password_hash IS NOT NULL AND NOT u.disabled
		  AND NOT EXISTS (SELECT 1 FROM permission_assignments pa WHERE pa.permission_target_id = pt.id)
		ON CONFLICT (permission_target_id, principal_type, principal_id) DO NOTHING`,
		username,
	)
	if err != nil {
		return false, fmt.Errorf("bootstrap repair admin-all: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit bootstrap repair tx: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

func (p *UserStore) UpdateUserSSO(ctx context.Context, userID string, externalID, providerSlug string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx,
		`UPDATE auth_users SET external_id=$2, provider_slug=$3 WHERE id=$1`,
		userID, externalID, providerSlug,
	)
	if err != nil {
		return fmt.Errorf("failed to update user sso: %w", err)
	}

	return nil
}

func (p *UserStore) GetUserByExternalID(ctx context.Context, externalID, providerSlug string) (*models.User, error) {
	return p.getUserWhere(ctx, "external id", "u.external_id=$1 AND u.provider_slug=$2", externalID, providerSlug)
}

func (p *UserStore) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return p.getUserWhere(ctx, "email", "u.email=$1", email)
}
