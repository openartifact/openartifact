// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TokenPrefix starts every peer token, so a leaked one is recognisable and
// secret scanners can match it.
const TokenPrefix = "oafed_"

// tokenRandomBytes of entropy: 32 bytes = 43 base64url characters.
const tokenRandomBytes = 32

// Token is an inbound credential: a peer presents it to pull one repository.
type Token struct {
	ID         string
	RepoID     string
	Name       string
	CreatedBy  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// HashToken is the stored form of a token.
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))

	return sum[:]
}

// GenerateToken returns a new random peer token.
func GenerateToken() (string, error) {
	b := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate peer token: %w", err)
	}

	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateToken issues a peer token for a repository. The clear text is returned
// once and never stored.
func (s *Store) CreateToken(ctx context.Context, repoID, name, createdBy string) (*Token, string, error) {
	plain, err := GenerateToken()
	if err != nil {
		return nil, "", err
	}

	t := &Token{RepoID: repoID, Name: name, CreatedBy: createdBy}

	err = s.db.QueryRowContext(ctx,
		`INSERT INTO federation_peer_tokens (repo_id, name, token_hash, created_by)
		 VALUES ($1::uuid, $2, $3, $4) RETURNING id::text, created_at`,
		repoID, name, HashToken(plain), createdBy).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return nil, "", fmt.Errorf("create peer token: %w", err)
	}

	return t, plain, nil
}

// ListTokens lists a repository's peer tokens, newest first.
func (s *Store) ListTokens(ctx context.Context, repoID string) ([]*Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id::text, repo_id::text, name, created_by, created_at, last_used_at, revoked_at
		   FROM federation_peer_tokens WHERE repo_id = $1::uuid ORDER BY created_at DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list peer tokens: %w", err)
	}
	defer rows.Close()

	var out []*Token

	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.RepoID, &t.Name, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan peer token: %w", err)
		}

		out = append(out, &t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list peer tokens: %w", err)
	}

	return out, nil
}

// RevokeToken revokes a peer token; its requests are refused from then on.
func (s *Store) RevokeToken(ctx context.Context, repoID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE federation_peer_tokens SET revoked_at = now()
		  WHERE id = $1::uuid AND repo_id = $2::uuid AND revoked_at IS NULL`, id, repoID)
	if err != nil {
		return fmt.Errorf("revoke peer token: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	return nil
}

// lastUsedGranularity bounds how often a busy peer rewrites its token row.
const lastUsedGranularity = time.Minute

// AuthenticateToken resolves a presented peer token to the repository it is
// bound to. Unknown, malformed and revoked tokens give ErrNotFound.
func (s *Store) AuthenticateToken(ctx context.Context, plain string) (string, error) {
	if !strings.HasPrefix(plain, TokenPrefix) || len(plain) > 128 {
		return "", ErrNotFound
	}

	var id, repoID string

	err := s.db.QueryRowContext(ctx,
		`SELECT id::text, repo_id::text FROM federation_peer_tokens WHERE token_hash = $1 AND revoked_at IS NULL`,
		HashToken(plain)).Scan(&id, &repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}

	if err != nil {
		return "", fmt.Errorf("authenticate peer token: %w", err)
	}

	// Best effort: a failed bookkeeping write must not refuse a valid peer.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE federation_peer_tokens SET last_used_at = now()
		  WHERE id = $1::uuid AND (last_used_at IS NULL OR last_used_at < now() - make_interval(secs => $2))`,
		id, lastUsedGranularity.Seconds())

	return repoID, nil
}
