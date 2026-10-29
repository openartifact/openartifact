// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"time"
)

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

// BootstrapAdmin describes the first admin account created on an empty instance.
type BootstrapAdmin struct {
	Username     string
	PasswordHash string
	// MustChangePassword forces a password change at first login.
	MustChangePassword bool
	// PasswordExpiresAt, when set, is the instant after which the password is
	// rejected at login.
	PasswordExpiresAt *time.Time
	// TokenHash is the lookup digest of an access token to create for the
	// admin; empty creates none.
	TokenHash      string
	TokenName      string
	TokenExpiresAt *time.Time
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
