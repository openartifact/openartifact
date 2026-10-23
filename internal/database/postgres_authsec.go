// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"time"
)

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
func (p *PostgresDB) DeleteUser(ctx context.Context, id string) (bool, error) {
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM auth_users WHERE id = $1 AND kind = 'human')`, id).Scan(&exists); err != nil {
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM permission_assignments WHERE principal_type = 'user' AND principal_id = $1`, id); err != nil {
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

// DeleteSessionsByUser revokes every session of a user.
func (p *PostgresDB) DeleteSessionsByUser(ctx context.Context, userID string) error {
	if _, err := p.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("failed to delete sessions by user: %w", err)
	}

	return nil
}

// DeleteOtherSessionsByUser revokes every session of a user except the one
// identified by keepTokenHash.
func (p *PostgresDB) DeleteOtherSessionsByUser(ctx context.Context, userID, keepTokenHash string) error {
	if _, err := p.db.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE user_id = $1 AND token_hash <> $2`,
		userID, keepTokenHash,
	); err != nil {
		return fmt.Errorf("failed to delete other sessions by user: %w", err)
	}

	return nil
}

// CreateBootstrapAdmin creates the first admin user and binds it to the
// admin-all permission target in one transaction guarded by an advisory lock.
// It returns false (and creates nothing) when a user already exists, which is
// what a replica that lost the first-boot race observes.
func (p *PostgresDB) CreateBootstrapAdmin(ctx context.Context, username, passwordHash string) (bool, error) {
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
		`INSERT INTO auth_users (username, password_hash, must_change_password)
		VALUES ($1, $2, true) RETURNING id`,
		username, passwordHash,
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
		return false, fmt.Errorf("bootstrap assign admin-all: permission target %q not found", "admin-all") //nolint:err113 // dynamic error with runtime context
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit bootstrap tx: %w", err)
	}

	return true, nil
}

// RepairBootstrapAdmin re-binds the named local admin user to admin-all when no
// principal at all holds that target, which is the state an interrupted
// bootstrap leaves behind. It never touches an instance where some principal
// already holds admin-all.
func (p *PostgresDB) RepairBootstrapAdmin(ctx context.Context, username string) (bool, error) {
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

// ConsumeSAMLAssertion records a SAML assertion ID as used and reports whether
// this call was the first to consume it. A false result means the assertion was
// already accepted before (replay). Expired records are purged opportunistically.
func (p *PostgresDB) ConsumeSAMLAssertion(ctx context.Context, providerSlug, assertionID string, expiresAt time.Time) (bool, error) {
	if _, err := p.db.ExecContext(ctx, `DELETE FROM auth_saml_assertions WHERE expires_at < now()`); err != nil {
		return false, fmt.Errorf("purge saml assertions: %w", err)
	}

	res, err := p.db.ExecContext(ctx,
		`INSERT INTO auth_saml_assertions (assertion_id, provider_slug, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (provider_slug, assertion_id) DO NOTHING`,
		assertionID, providerSlug, expiresAt,
	)
	if err != nil {
		return false, fmt.Errorf("record saml assertion: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}
