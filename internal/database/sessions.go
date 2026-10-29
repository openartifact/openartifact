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

// SessionStore owns login sessions and consumed SAML assertions.
type SessionStore struct{ *core }

func (p *SessionStore) CreateSession(ctx context.Context, session *models.Session) (*models.Session, error) {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO auth_sessions (user_id, token_hash, ip_address, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, last_seen_at`,
		session.UserID, session.TokenHash, session.IPAddress, session.UserAgent, session.ExpiresAt,
	).Scan(&session.ID, &session.CreatedAt, &session.LastSeenAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return session, nil
}

func (p *SessionStore) GetSessionByTokenHash(ctx context.Context, hash string) (*models.Session, error) {
	s := &models.Session{}

	var ipAddress, userAgent sql.NullString

	err := p.db.QueryRowContext(ctx,
		`SELECT id, user_id, token_hash, ip_address, user_agent, expires_at, created_at, last_seen_at
		FROM auth_sessions WHERE token_hash = $1`,
		hash,
	).Scan(&s.ID, &s.UserID, &s.TokenHash, &ipAddress, &userAgent, &s.ExpiresAt, &s.CreatedAt, &s.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get session by token hash: %w", err)
	}

	if ipAddress.Valid {
		s.IPAddress = &ipAddress.String
	}

	if userAgent.Valid {
		s.UserAgent = &userAgent.String
	}

	return s, nil
}

func (p *SessionStore) SetSessionSSO(ctx context.Context, sessionID, providerSlug, encryptedIDToken string) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO auth_session_sso (session_id, provider_slug, id_token) VALUES ($1, $2, $3)
		ON CONFLICT (session_id) DO UPDATE SET provider_slug = EXCLUDED.provider_slug, id_token = EXCLUDED.id_token`,
		sessionID, providerSlug, encryptedIDToken,
	)
	if err != nil {
		return fmt.Errorf("failed to set session sso: %w", err)
	}

	return nil
}

func (p *SessionStore) GetSessionSSO(ctx context.Context, tokenHash string) (*models.SessionSSO, error) {
	rec := &models.SessionSSO{}

	err := p.db.QueryRowContext(ctx,
		`SELECT x.provider_slug, x.id_token FROM auth_session_sso x
		JOIN auth_sessions s ON s.id = x.session_id WHERE s.token_hash = $1`,
		tokenHash,
	).Scan(&rec.ProviderSlug, &rec.IDToken)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get session sso: %w", err)
	}

	return rec, nil
}

func (p *SessionStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash=$1`, tokenHash)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}

func (p *SessionStore) UpdateSessionLastSeen(ctx context.Context, sessionID string, expiresAt time.Time) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen_at=now(), expires_at=$2 WHERE id=$1`,
		sessionID, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("failed to update session last seen: %w", err)
	}

	return nil
}
