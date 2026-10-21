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
