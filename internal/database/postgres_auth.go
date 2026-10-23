// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

func (p *PostgresDB) CreateUser(ctx context.Context, user *models.User) (*models.User, error) {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO auth_users (username, email, password_hash, full_name, disabled, must_change_password, external_id, provider_slug, sso_managed_groups)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`,
		user.Username, user.Email, user.PasswordHash, user.FullName, user.Disabled, user.MustChangePassword,
		user.ExternalID, user.ProviderSlug, user.SSOManagedGroups,
	).Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

//nolint:dupl // handler boilerplate: identical structure, different entities
func (p *PostgresDB) GetUserByIDUncached(ctx context.Context, id string) (*models.User, error) {
	u := &models.User{}

	var (
		email, passwordHash, fullName, externalID, providerSlug sql.NullString
		lastLoginAt                                             sql.NullTime
		userProject                                             sql.NullString
	)

	err := p.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups, kind, project_id
		FROM auth_users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.Username, &email, &passwordHash, &fullName, &u.Disabled, &u.MustChangePassword, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt, &externalID, &providerSlug, &u.SSOManagedGroups, &u.Kind, &userProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}

	if email.Valid {
		u.Email = &email.String
	}

	if passwordHash.Valid {
		u.PasswordHash = &passwordHash.String
	}

	if fullName.Valid {
		u.FullName = &fullName.String
	}

	if lastLoginAt.Valid {
		u.LastLoginAt = &lastLoginAt.Time
	}

	if externalID.Valid {
		u.ExternalID = &externalID.String
	}

	if providerSlug.Valid {
		u.ProviderSlug = &providerSlug.String
	}

	if userProject.Valid {
		u.ProjectID = &userProject.String
	}

	return u, nil
}

//nolint:dupl // handler boilerplate: identical structure, different entities
func (p *PostgresDB) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	u := &models.User{}

	var (
		email, passwordHash, fullName, externalID, providerSlug sql.NullString
		lastLoginAt                                             sql.NullTime
		userProject                                             sql.NullString
	)

	err := p.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups, kind, project_id
		FROM auth_users WHERE username = $1`,
		username,
	).Scan(&u.ID, &u.Username, &email, &passwordHash, &fullName, &u.Disabled, &u.MustChangePassword, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt, &externalID, &providerSlug, &u.SSOManagedGroups, &u.Kind, &userProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get user by username: %w", err)
	}

	if email.Valid {
		u.Email = &email.String
	}

	if passwordHash.Valid {
		u.PasswordHash = &passwordHash.String
	}

	if fullName.Valid {
		u.FullName = &fullName.String
	}

	if lastLoginAt.Valid {
		u.LastLoginAt = &lastLoginAt.Time
	}

	if externalID.Valid {
		u.ExternalID = &externalID.String
	}

	if providerSlug.Valid {
		u.ProviderSlug = &providerSlug.String
	}

	if userProject.Valid {
		u.ProjectID = &userProject.String
	}

	return u, nil
}

func (p *PostgresDB) UpdateUser(ctx context.Context, user *models.User) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`UPDATE auth_users SET username=$2, email=$3, password_hash=$4, full_name=$5, disabled=$6, must_change_password=$7, last_login_at=$8, external_id=$9, provider_slug=$10, sso_managed_groups=$11
		WHERE id=$1`,
		user.ID, user.Username, user.Email, user.PasswordHash, user.FullName, user.Disabled, user.MustChangePassword, user.LastLoginAt,
		user.ExternalID, user.ProviderSlug, user.SSOManagedGroups,
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

func (p *PostgresDB) UpdateUserLastLogin(ctx context.Context, id string, at time.Time) error {
	if _, err := p.db.ExecContext(ctx, `UPDATE auth_users SET last_login_at=$2 WHERE id=$1`, id, at); err != nil {
		return fmt.Errorf("failed to update user last login: %w", err)
	}

	return nil
}

func (p *PostgresDB) UpdateUserSSOProfile(ctx context.Context, id string, email, fullName *string, lastLogin time.Time) error {
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

func (p *PostgresDB) UpdateUserPassword(ctx context.Context, id, passwordHash string, mustChange bool) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	if _, err := p.db.ExecContext(ctx, `UPDATE auth_users SET password_hash=$2, must_change_password=$3 WHERE id=$1`, id, passwordHash, mustChange); err != nil {
		return fmt.Errorf("failed to update user password: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListUsers(ctx context.Context, page, perPage int) ([]*models.User, int, error) {
	offset := (page - 1) * perPage

	var total int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE kind = 'human'`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count users: %w", err)
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
		FROM auth_users WHERE kind = 'human' ORDER BY created_at ASC, id LIMIT $1 OFFSET $2`,
		perPage, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var users []*models.User

	for rows.Next() {
		u := &models.User{}

		var (
			email, passwordHash, fullName, externalID, providerSlug sql.NullString
			lastLoginAt                                             sql.NullTime
		)
		if err := rows.Scan(&u.ID, &u.Username, &email, &passwordHash, &fullName, &u.Disabled, &u.MustChangePassword, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt, &externalID, &providerSlug, &u.SSOManagedGroups); err != nil {
			return nil, 0, fmt.Errorf("failed to scan user: %w", err)
		}

		if email.Valid {
			u.Email = &email.String
		}

		if passwordHash.Valid {
			u.PasswordHash = &passwordHash.String
		}

		if fullName.Valid {
			u.FullName = &fullName.String
		}

		if lastLoginAt.Valid {
			u.LastLoginAt = &lastLoginAt.Time
		}

		if externalID.Valid {
			u.ExternalID = &externalID.String
		}

		if providerSlug.Valid {
			u.ProviderSlug = &providerSlug.String
		}

		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to iterate users: %w", err)
	}

	return users, total, nil
}

func (p *PostgresDB) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE kind = 'human'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count users: %w", err)
	}

	return count, nil
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
	g := &models.Group{}

	var description sql.NullString

	err := p.db.QueryRowContext(ctx,
		`SELECT id, name, description, created_at, updated_at FROM auth_groups WHERE id = $1`,
		id,
	).Scan(&g.ID, &g.Name, &description, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get group by id: %w", err)
	}

	if description.Valid {
		g.Description = &description.String
	}

	return g, nil
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
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, name, description, created_at, updated_at FROM auth_groups ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var groups []*models.Group

	for rows.Next() {
		g := &models.Group{}

		var description sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &description, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan group: %w", err)
		}

		if description.Valid {
			g.Description = &description.String
		}

		groups = append(groups, g)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate groups: %w", err)
	}

	return groups, nil
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
		`SELECT g.id, g.name, g.description, g.created_at, g.updated_at
		FROM auth_groups g
		JOIN auth_user_groups ug ON ug.group_id = g.id
		WHERE ug.user_id = $1
		ORDER BY g.name ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups by user: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var groups []*models.Group

	for rows.Next() {
		g := &models.Group{}

		var description sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &description, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan group: %w", err)
		}

		if description.Valid {
			g.Description = &description.String
		}

		groups = append(groups, g)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate groups: %w", err)
	}

	return groups, nil
}

func (p *PostgresDB) ListUsersByGroup(ctx context.Context, groupID string) ([]*models.User, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT u.id, u.username, u.email, u.password_hash, u.full_name, u.disabled, u.must_change_password, u.last_login_at, u.created_at, u.updated_at, u.external_id, u.provider_slug, u.sso_managed_groups
		FROM auth_users u
		JOIN auth_user_groups ug ON ug.user_id = u.id
		WHERE ug.group_id = $1
		ORDER BY u.username ASC`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list users by group: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var users []*models.User

	for rows.Next() {
		u := &models.User{}

		var (
			email, passwordHash, fullName, externalID, providerSlug sql.NullString
			lastLoginAt                                             sql.NullTime
		)
		if err := rows.Scan(&u.ID, &u.Username, &email, &passwordHash, &fullName, &u.Disabled, &u.MustChangePassword, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt, &externalID, &providerSlug, &u.SSOManagedGroups); err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}

		if email.Valid {
			u.Email = &email.String
		}

		if passwordHash.Valid {
			u.PasswordHash = &passwordHash.String
		}

		if fullName.Valid {
			u.FullName = &fullName.String
		}

		if lastLoginAt.Valid {
			u.LastLoginAt = &lastLoginAt.Time
		}

		if externalID.Valid {
			u.ExternalID = &externalID.String
		}

		if providerSlug.Valid {
			u.ProviderSlug = &providerSlug.String
		}

		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate users: %w", err)
	}

	return users, nil
}

func (p *PostgresDB) CreateSession(ctx context.Context, session *models.Session) (*models.Session, error) {
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

func (p *PostgresDB) GetSessionByTokenHash(ctx context.Context, hash string) (*models.Session, error) {
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

func (p *PostgresDB) SetSessionSSO(ctx context.Context, sessionID, providerSlug, encryptedIDToken string) error {
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

func (p *PostgresDB) GetSessionSSO(ctx context.Context, tokenHash string) (*models.SessionSSO, error) {
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

func (p *PostgresDB) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash=$1`, tokenHash)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}

func (p *PostgresDB) UpdateSessionLastSeen(ctx context.Context, sessionID string, expiresAt time.Time) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen_at=now(), expires_at=$2 WHERE id=$1`,
		sessionID, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("failed to update session last seen: %w", err)
	}

	return nil
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

func (p *PostgresDB) GetTokenByHashUncached(ctx context.Context, hash string) (*models.Token, error) {
	t, err := scanToken(p.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE t.token_hash = $1`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get token by hash: %w", err)
	}

	return t, nil
}

func (p *PostgresDB) GetTokenByID(ctx context.Context, tokenID string) (*models.Token, error) {
	t, err := scanToken(p.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE t.id = $1`, tokenID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get token by id: %w", err)
	}

	return t, nil
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
	defer func() { _ = rows.Close() }()

	var tokens []*models.Token

	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan token: %w", err)
		}

		tokens = append(tokens, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate tokens: %w", err)
	}

	return tokens, nil
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

func (p *PostgresDB) GetPermissionTargetByID(ctx context.Context, id string) (*models.PermissionTarget, error) {
	row, err := p.q.GetPermissionTargetByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get permission target by id: %w", err)
	}

	return &models.PermissionTarget{
		ID:               row.ID,
		Name:             row.Name,
		RepositoryFilter: row.RepositoryFilter,
		PathPattern:      row.PathPattern,
		Actions:          row.Actions,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error) {
	row, err := p.q.GetPermissionTargetByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get permission target by name: %w", err)
	}

	return &models.PermissionTarget{
		ID:               row.ID,
		Name:             row.Name,
		RepositoryFilter: row.RepositoryFilter,
		PathPattern:      row.PathPattern,
		Actions:          row.Actions,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}, nil
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

func (p *PostgresDB) GetPermissionTargetsForPrincipalUncached(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error) {
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
