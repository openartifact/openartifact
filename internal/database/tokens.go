// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TokenStore owns API tokens.
type TokenStore struct{ *core }

func cloneToken(t *models.Token) *models.Token {
	cp := *t
	cp.Scopes = append([]string(nil), t.Scopes...)

	return &cp
}

// GetTokenByHash returns the token with the given hash, from the auth cache
// when fresh.
func (p *TokenStore) GetTokenByHash(ctx context.Context, hash string) (*models.Token, error) {
	return cachedPtr(ctx, &p.authCache, &p.authCache.tokens, hash, cloneToken, p.GetTokenByHashUncached)
}

func (p *TokenStore) ListAllTokens(ctx context.Context, page, perPage int) ([]*models.Token, int, error) {
	var total int
	if err := p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_tokens WHERE revoked_at IS NULL").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tokens: %w", err)
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE t.revoked_at IS NULL ORDER BY t.created_at DESC, t.id LIMIT $1 OFFSET $2`,
		perPage, (page-1)*perPage,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list all tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tokens []*models.Token

	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan token: %w", err)
		}

		tokens = append(tokens, t)
	}

	return tokens, total, rows.Err()
}

func (p *TokenStore) CreateToken(ctx context.Context, token *models.Token) (*models.Token, error) {
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
func (p *TokenStore) getTokenWhere(ctx context.Context, what, where string, arg any) (*models.Token, error) {
	t, err := scanToken(p.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE `+where, arg))

	return orNotFound(t, err, "get token by "+what)
}

func (p *TokenStore) GetTokenByHashUncached(ctx context.Context, hash string) (*models.Token, error) {
	return p.getTokenWhere(ctx, "hash", "t.token_hash = $1", hash)
}

func (p *TokenStore) GetTokenByID(ctx context.Context, tokenID string) (*models.Token, error) {
	return p.getTokenWhere(ctx, "id", "t.id = $1", tokenID)
}

func (p *TokenStore) RevokeToken(ctx context.Context, tokenID string) error {
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

func (p *TokenStore) UpdateTokenLastUsed(ctx context.Context, tokenID string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE auth_tokens SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now() - interval '60 seconds')`,
		tokenID,
	)
	if err != nil {
		return fmt.Errorf("failed to update token last used: %w", err)
	}

	return nil
}

func (p *TokenStore) ListTokensByUser(ctx context.Context, userID string) ([]*models.Token, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE t.user_id=$1 ORDER BY t.created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list tokens by user: %w", err)
	}

	return collectRows(rows, "failed to scan token", "failed to iterate tokens", scanToken)
}

// unifiedTokensCTE unions the three token stores into one row shape. $1 admin,
// $2 viewer id (” for none; compared as text so a missing viewer matches
// nothing), $3 project ids visible to a non-admin, $4 kinds, $5 expires-before,
// $6 name pattern, $7 exact project name (” = any). Revoked tokens are not
// listed. A federation token has no expiry.
//
//nolint:gosec // SQL over token metadata; no credential here
const unifiedTokensCTE = `
WITH u AS (
    SELECT t.id::text AS id, 'personal'::text AS type, t.name::text AS name, ou.username::text AS owner,
           ''::text AS project, ''::text AS repository, ''::text AS role, t.scopes::text AS scopes,
           t.created_at AS created_at, t.expires_at AS expires_at, t.last_used_at AS last_used_at
      FROM auth_tokens t JOIN auth_users ou ON ou.id = t.user_id
     WHERE t.project_id IS NULL AND t.revoked_at IS NULL AND ($1 OR t.user_id::text = $2)
    UNION ALL
    SELECT t.id::text, 'project', t.name::text, COALESCE(cu.username, '')::text,
           pr.name::text, '', COALESCE(t.project_role, '')::text, t.scopes::text,
           t.created_at, t.expires_at, t.last_used_at
      FROM auth_tokens t
      JOIN projects pr ON pr.id = t.project_id
      LEFT JOIN auth_users cu ON cu.id = t.created_by
     WHERE t.revoked_at IS NULL AND ($1 OR t.project_id = ANY($3::uuid[]))
    UNION ALL
    SELECT f.id::text, 'federation', f.name::text, f.created_by::text,
           COALESCE(pr.name, '')::text, r.name::text, '', '[]',
           f.created_at, NULL::timestamptz, f.last_used_at
      FROM federation_peer_tokens f
      JOIN repositories r ON r.id = f.repo_id
      LEFT JOIN projects pr ON pr.id = r.project_id
     WHERE f.revoked_at IS NULL AND r.deleted_at IS NULL AND ($1 OR r.project_id = ANY($3::uuid[]))
)
SELECT id, type, name, owner, project, repository, role, scopes, created_at, expires_at, last_used_at
  FROM u
 WHERE type = ANY($4::text[])
   AND ($5::timestamptz IS NULL OR (expires_at IS NOT NULL AND expires_at < $5))
   AND ($6 = '' OR name ILIKE $6 OR owner ILIKE $6)
   AND ($7 = '' OR project = $7)`

// ListUnifiedTokens returns one page of the unified token list (personal,
// project and federation tokens), newest first, and the number of tokens the
// caller may see that match the filters.
func (p *TokenStore) ListUnifiedTokens(ctx context.Context, params models.TokenListParams) ([]*models.TokenListItem, int, error) {
	types := params.Types
	if len(types) == 0 {
		types = []string{models.TokenListPersonal, models.TokenListProject, models.TokenListFederation}
	}

	pattern := ""
	if params.Query != "" {
		pattern = "%" + escapeLike(params.Query) + "%"
	}

	args := []any{params.Admin, params.ViewerID, nonNil(params.ProjectIDs), types, params.ExpiresBefore, pattern, params.Project}

	var total int

	if err := p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+unifiedTokensCTE+") c", args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tokens: %w", err)
	}

	rows, err := p.db.QueryContext(ctx, unifiedTokensCTE+" ORDER BY created_at DESC, id LIMIT $8 OFFSET $9",
		append(args, params.Limit, (params.Page-1)*params.Limit)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tokens: %w", err)
	}

	defer func() { _ = rows.Close() }()

	out := []*models.TokenListItem{}

	for rows.Next() {
		t := &models.TokenListItem{}

		var scopes string

		if err := rows.Scan(
			&t.ID,
			&t.Type,
			&t.Name,
			&t.Owner,
			&t.Project,
			&t.Repository,
			&t.Role,
			&scopes,
			&t.CreatedAt,
			&t.ExpiresAt,
			&t.LastUsedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan token: %w", err)
		}

		if err := json.Unmarshal([]byte(scopes), &t.Scopes); err != nil {
			return nil, 0, fmt.Errorf("unmarshal token scopes: %w", err)
		}

		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list tokens: %w", err)
	}

	return out, total, nil
}
