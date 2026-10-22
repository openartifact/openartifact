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

// Errors of project token creation.
var (
	// ErrProjectTokenLimit: the project already has the maximum of active tokens.
	ErrProjectTokenLimit = errors.New("project token limit reached")
	// ErrProjectTokenName: an active token of the project already has the name.
	ErrProjectTokenName = errors.New("project token name already in use")
)

// ProjectTokenSpec describes a project token to mint.
type ProjectTokenSpec struct {
	ProjectID string
	// ProjectSlug seeds the service user's name.
	ProjectSlug string
	Name        string
	// Role is the token's project role (viewer or developer).
	Role      string
	CreatedBy string
	TokenHash string
	ExpiresAt *time.Time
	// MaxActive caps the active (not revoked) tokens of the project.
	MaxActive int
}

// CreateProjectToken creates, in one transaction that holds the project row
// lock, the service user, its membership with the token's role and the deploy
// token. It returns ErrProjectNotFound, ErrProjectTokenLimit or
// ErrProjectTokenName when the token cannot be minted.
func (p *PostgresDB) CreateProjectToken(ctx context.Context, spec ProjectTokenSpec) (*models.Token, error) {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin project token: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := lockProject(ctx, tx, spec.ProjectID); err != nil {
		return nil, err
	}

	var active int

	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM auth_tokens WHERE project_id = $1 AND revoked_at IS NULL`, spec.ProjectID,
	).Scan(&active); err != nil {
		return nil, fmt.Errorf("count project tokens: %w", err)
	}

	if spec.MaxActive > 0 && active >= spec.MaxActive {
		return nil, ErrProjectTokenLimit
	}

	var taken bool

	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM auth_tokens WHERE project_id = $1 AND name = $2 AND revoked_at IS NULL)`,
		spec.ProjectID, spec.Name,
	).Scan(&taken); err != nil {
		return nil, fmt.Errorf("check project token name: %w", err)
	}

	if taken {
		return nil, ErrProjectTokenName
	}

	var userID string

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO auth_users (username, disabled, kind, project_id)
		VALUES ('svc-' || $1 || '-' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 10), false, 'service', $2)
		RETURNING id`,
		spec.ProjectSlug, spec.ProjectID,
	).Scan(&userID); err != nil {
		return nil, fmt.Errorf("create service user: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`,
		spec.ProjectID, userID, spec.Role,
	); err != nil {
		return nil, fmt.Errorf("add service user to project: %w", err)
	}

	token := &models.Token{
		UserID: userID, Name: spec.Name, Kind: "deploy", TokenHash: spec.TokenHash, Scopes: []string{},
		ExpiresAt: spec.ExpiresAt, ProjectID: &spec.ProjectID, CreatedBy: &spec.CreatedBy, ProjectRole: &spec.Role,
	}

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO auth_tokens (user_id, name, kind, token_hash, scopes, expires_at, project_id, created_by, project_role)
		VALUES ($1, $2, 'deploy', $3, '[]'::jsonb, $4, $5, $6, $7)
		RETURNING id, created_at`,
		userID, spec.Name, spec.TokenHash, spec.ExpiresAt, spec.ProjectID, spec.CreatedBy, spec.Role,
	).Scan(&token.ID, &token.CreatedAt); err != nil {
		if IsUniqueViolation(err) {
			return nil, ErrProjectTokenName
		}

		return nil, fmt.Errorf("create project token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit project token: %w", err)
	}

	return token, nil
}

// ListProjectTokens returns the project's tokens that are not revoked, newest
// first. createdBy limits them to the tokens minted by that user.
func (p *PostgresDB) ListProjectTokens(ctx context.Context, projectID string, createdBy *string) ([]*models.Token, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM `+tokenFrom+`
		WHERE t.project_id = $1 AND t.revoked_at IS NULL AND ($2::uuid IS NULL OR t.created_by = $2)
		ORDER BY t.created_at DESC, t.id`,
		projectID, createdBy,
	)
	if err != nil {
		return nil, fmt.Errorf("list project tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tokens []*models.Token

	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project token: %w", err)
		}

		tokens = append(tokens, t)
	}

	return tokens, rows.Err()
}

// RevokeProjectToken revokes a token of the project and disables its service
// user in one transaction; the auth cache is dropped so the token stops working
// at once. It reports false when the project has no such active token.
func (p *PostgresDB) RevokeProjectToken(ctx context.Context, projectID, tokenID string) (bool, error) {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin project token revoke: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var userID string

	err = tx.QueryRowContext(ctx,
		`UPDATE auth_tokens SET revoked_at = now()
		WHERE id = $1 AND project_id = $2 AND revoked_at IS NULL RETURNING user_id`,
		tokenID, projectID,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("revoke project token: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE auth_users SET disabled = true WHERE id = $1 AND kind = 'service'`, userID); err != nil {
		return false, fmt.Errorf("disable service user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit project token revoke: %w", err)
	}

	return true, nil
}

// revokeProjectTokensOf revokes the project tokens minted by creatorID (in
// projectID, or in every project when empty) and disables their service
// users. It runs inside the caller's transaction so the revocation commits
// with the change that caused it: the creator leaving the project, being
// disabled or deleted.
func revokeProjectTokensOf(ctx context.Context, tx *sql.Tx, creatorID, projectID string) error {
	_, err := tx.ExecContext(ctx,
		`WITH revoked AS (
			UPDATE auth_tokens SET revoked_at = now()
			WHERE created_by = $1 AND project_id IS NOT NULL AND revoked_at IS NULL
			  AND ($2 = '' OR project_id = $2::uuid)
			RETURNING user_id)
		UPDATE auth_users SET disabled = true WHERE id IN (SELECT user_id FROM revoked) AND kind = 'service'`,
		creatorID, projectID)
	if err != nil {
		return fmt.Errorf("revoke project tokens of creator: %w", err)
	}

	return nil
}

// revokeExcessProjectTokens revokes the live project tokens whose role outranks
// what their creator still holds in that project (recorded owner, membership,
// or group attachment), and disables the service accounts behind them. It
// runs in the caller's transaction after any change that can lower a user's
// project role: demotion, group detachment, group membership removal. An empty
// projectID checks every project.
func revokeExcessProjectTokens(ctx context.Context, tx *sql.Tx, projectID string) error {
	// Role ranks mirror models.ProjectRoleRank (viewer 1 .. owner 4).
	_, err := tx.ExecContext(ctx,
		`WITH held AS (
			SELECT p.id AS project_id, p.owner_id AS user_id, 4 AS r
			FROM projects p WHERE p.owner_id IS NOT NULL
			UNION ALL
			SELECT pm.project_id, pm.user_id,
			       CASE pm.role WHEN 'viewer' THEN 1 WHEN 'developer' THEN 2 WHEN 'maintainer' THEN 3 WHEN 'owner' THEN 4 ELSE 0 END
			FROM project_members pm
			UNION ALL
			SELECT pg.project_id, ug.user_id,
			       CASE pg.role WHEN 'viewer' THEN 1 WHEN 'developer' THEN 2 WHEN 'maintainer' THEN 3 WHEN 'owner' THEN 4 ELSE 0 END
			FROM project_groups pg JOIN auth_user_groups ug ON ug.group_id = pg.group_id
		), revoked AS (
			UPDATE auth_tokens t SET revoked_at = now()
			WHERE t.project_id IS NOT NULL AND t.revoked_at IS NULL AND t.created_by IS NOT NULL
			  AND ($1 = '' OR t.project_id = $1::uuid)
			  AND CASE t.project_role WHEN 'viewer' THEN 1 WHEN 'developer' THEN 2 WHEN 'maintainer' THEN 3 WHEN 'owner' THEN 4 ELSE 0 END
			      > COALESCE((SELECT max(h.r) FROM held h WHERE h.project_id = t.project_id AND h.user_id = t.created_by), 0)
			RETURNING user_id)
		UPDATE auth_users SET disabled = true WHERE id IN (SELECT user_id FROM revoked) AND kind = 'service'`,
		projectID)
	if err != nil {
		return fmt.Errorf("revoke project tokens above creator role: %w", err)
	}

	return nil
}
