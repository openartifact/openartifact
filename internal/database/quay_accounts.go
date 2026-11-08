// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// QuayRobotSpec describes a Quay robot account to create: the service account
// (its username), the deploy token minted for it and the sealed token the Quay
// API hands out again.
type QuayRobotSpec struct {
	ID           string
	RepositoryID string
	Name         string
	Description  string
	Username     string
	TokenKind    string
	TokenName    string
	TokenHash    string
	TokenEnc     string
}

const quayRobotColumns = `qr.id, qr.repository_id, r.name, qr.name, qr.description, qr.user_id,
	COALESCE(qr.token_id::text, ''), qr.token_enc, qr.created_at`

func scanQuayRobot(row rowScanner) (*models.QuayRobot, error) {
	b := &models.QuayRobot{}
	if err := row.Scan(
		&b.ID,
		&b.RepositoryID,
		&b.OrgName,
		&b.Name,
		&b.Description,
		&b.UserID,
		&b.TokenID,
		&b.TokenEnc,
		&b.CreatedAt,
	); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	return b, nil
}

// execQuerier is the part of *sql.DB and *sql.Tx the quay helpers run on.
type execQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// insertQuayToken stores a robot's deploy token and returns its id.
func insertQuayToken(ctx context.Context, tx execQuerier, userID, kind, name, hash string) (string, error) {
	var id string
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO auth_tokens (user_id, name, kind, token_hash, scopes) VALUES ($1, $2, $3, $4, '[]'::jsonb) RETURNING id`,
		userID, name, kind, hash).Scan(&id); err != nil {
		return "", fmt.Errorf("create quay robot token: %w", err)
	}

	return id, nil
}

// CreateQuayRobot creates the service account, its token and the robot row in
// one transaction. A clash with an existing robot name is IsUniqueViolation.
func (p *QuayStore) CreateQuayRobot(ctx context.Context, spec QuayRobotSpec) (*models.QuayRobot, error) {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create quay robot tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var userID string
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO auth_users (username, full_name, kind) VALUES ($1, $2, 'service_account') RETURNING id`,
		spec.Username, "Quay robot account "+spec.Name).Scan(&userID); err != nil {
		return nil, fmt.Errorf("create quay robot service account: %w", err)
	}

	tokenID, err := insertQuayToken(ctx, tx, userID, spec.TokenKind, spec.TokenName, spec.TokenHash)
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO quay_robots (id, repository_id, name, description, user_id, token_id, token_enc) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		spec.ID, spec.RepositoryID, spec.Name, spec.Description, userID, tokenID, spec.TokenEnc); err != nil {
		return nil, fmt.Errorf("create quay robot: %w", err)
	}

	b, err := scanQuayRobot(tx.QueryRowContext(ctx,
		`SELECT `+quayRobotColumns+` FROM quay_robots qr JOIN repositories r ON r.id = qr.repository_id WHERE qr.id = $1`, spec.ID))
	if err != nil {
		return nil, fmt.Errorf("reload quay robot: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create quay robot: %w", err)
	}

	return b, nil
}

// GetQuayRobot returns the robot of an organization, nil when there is none.
func (p *QuayStore) GetQuayRobot(ctx context.Context, repositoryID, name string) (*models.QuayRobot, error) {
	b, err := scanQuayRobot(p.db.QueryRowContext(ctx,
		`SELECT `+quayRobotColumns+` FROM quay_robots qr JOIN repositories r ON r.id = qr.repository_id
		WHERE qr.repository_id = $1 AND qr.name = $2`, repositoryID, name))

	return orNotFound(b, err, "get quay robot")
}

// ListQuayRobots returns the robots of an organization by name.
func (p *QuayStore) ListQuayRobots(ctx context.Context, repositoryID string) ([]*models.QuayRobot, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+quayRobotColumns+` FROM quay_robots qr JOIN repositories r ON r.id = qr.repository_id
		WHERE qr.repository_id = $1 ORDER BY qr.name`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("failed to list quay robots: %w", err)
	}

	return collectRows(rows, "failed to scan quay robot", "failed to iterate quay robots", scanQuayRobot)
}

// RotateQuayRobotToken revokes a robot's current token and stores a new one.
func (p *QuayStore) RotateQuayRobotToken(
	ctx context.Context,
	b *models.QuayRobot,
	kind, name, hash, tokenEnc string,
) (*models.QuayRobot, error) {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin rotate quay robot token tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE auth_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, b.UserID); err != nil {
		return nil, fmt.Errorf("revoke quay robot tokens: %w", err)
	}

	tokenID, err := insertQuayToken(ctx, tx, b.UserID, kind, name, hash)
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE quay_robots SET token_id = $2, token_enc = $3 WHERE id = $1`,
		b.ID,
		tokenID,
		tokenEnc,
	); err != nil {
		return nil, fmt.Errorf("update quay robot token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit rotate quay robot token: %w", err)
	}

	rotated := *b
	rotated.TokenID, rotated.TokenEnc = tokenID, tokenEnc

	return &rotated, nil
}

// DeleteQuayRobot deletes a robot with its service account (and so its tokens
// and permission assignments).
func (p *QuayStore) DeleteQuayRobot(ctx context.Context, b *models.QuayRobot) error {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete quay robot tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	// permission_assignments.principal_id is polymorphic (no FK): remove the
	// robot's assignments explicitly.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM permission_assignments WHERE principal_type = 'user' AND principal_id = $1`, b.UserID); err != nil {
		return fmt.Errorf("delete quay robot assignments: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_users WHERE id = $1 AND kind = 'service_account'`, b.UserID); err != nil {
		return fmt.Errorf("delete quay robot service account: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete quay robot: %w", err)
	}

	return nil
}

// CreateQuayTeam creates a team and the group behind it (named groupName)
// in one transaction. A clash with an existing team is IsUniqueViolation.
func (p *QuayStore) CreateQuayTeam(ctx context.Context, repositoryID, name, groupName string) (*models.QuayTeam, error) {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create quay team tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	t := &models.QuayTeam{RepositoryID: repositoryID, Name: name}

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO auth_groups (name, description) VALUES ($1, $2) RETURNING id`,
		groupName, "Quay team "+name).Scan(&t.GroupID); err != nil {
		return nil, fmt.Errorf("create quay team group: %w", err)
	}

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO quay_teams (repository_id, name, group_id) VALUES ($1, $2, $3) RETURNING id, created_at`,
		repositoryID, name, t.GroupID).Scan(&t.ID, &t.CreatedAt); err != nil {
		return nil, fmt.Errorf("create quay team: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create quay team: %w", err)
	}

	return t, nil
}

// GetQuayTeam returns the team of an organization, nil when there is none.
func (p *QuayStore) GetQuayTeam(ctx context.Context, repositoryID, name string) (*models.QuayTeam, error) {
	t := &models.QuayTeam{}

	err := p.db.QueryRowContext(ctx,
		`SELECT id, repository_id, name, group_id, created_at FROM quay_teams WHERE repository_id = $1 AND name = $2`,
		repositoryID, name).Scan(&t.ID, &t.RepositoryID, &t.Name, &t.GroupID, &t.CreatedAt)

	return orNotFound(t, err, "get quay team")
}

func scanQuayNotification(row rowScanner) (*models.QuayNotification, error) {
	n := &models.QuayNotification{}
	if err := row.Scan(&n.ID, &n.QuayRepositoryID, &n.Title, &n.Event, &n.Method, &n.Config, &n.EventConfig, &n.CreatedAt); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	return n, nil
}

const quayNotificationColumns = `id, quay_repository_id, title, event, method, config, event_config, created_at`

// CreateQuayNotification stores a notification for a Quay repository.
func (p *QuayStore) CreateQuayNotification(ctx context.Context, n *models.QuayNotification) (*models.QuayNotification, error) {
	created, err := scanQuayNotification(p.db.QueryRowContext(ctx,
		`INSERT INTO quay_notifications (quay_repository_id, title, event, method, config, event_config)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb) RETURNING `+quayNotificationColumns,
		n.QuayRepositoryID, n.Title, n.Event, n.Method, quayJSON(n.Config), quayJSON(n.EventConfig)))
	if err != nil {
		return nil, fmt.Errorf("failed to create quay notification: %w", err)
	}

	return created, nil
}

func quayJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}

	return string(raw)
}

// GetQuayNotification returns one notification of a Quay repository, nil when
// there is none (or the id is not a UUID).
func (p *QuayStore) GetQuayNotification(ctx context.Context, quayRepoID, id string) (*models.QuayNotification, error) {
	n, err := scanQuayNotification(p.db.QueryRowContext(ctx,
		`SELECT `+quayNotificationColumns+` FROM quay_notifications WHERE quay_repository_id = $1 AND id::text = $2`, quayRepoID, id))

	return orNotFound(n, err, "get quay notification")
}

// ListQuayNotifications returns the notifications of a Quay repository, oldest first.
func (p *QuayStore) ListQuayNotifications(ctx context.Context, quayRepoID string) ([]*models.QuayNotification, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+quayNotificationColumns+` FROM quay_notifications WHERE quay_repository_id = $1 ORDER BY created_at, id`, quayRepoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list quay notifications: %w", err)
	}

	return collectRows(rows, "failed to scan quay notification", "failed to iterate quay notifications", scanQuayNotification)
}

// UpdateQuayNotification replaces a notification's content, nil when there is
// no such notification.
func (p *QuayStore) UpdateQuayNotification(ctx context.Context, n *models.QuayNotification) (*models.QuayNotification, error) {
	updated, err := scanQuayNotification(p.db.QueryRowContext(ctx,
		`UPDATE quay_notifications SET title = $3, event = $4, method = $5, config = $6::jsonb, event_config = $7::jsonb
		WHERE quay_repository_id = $1 AND id::text = $2 RETURNING `+quayNotificationColumns,
		n.QuayRepositoryID, n.ID, n.Title, n.Event, n.Method, quayJSON(n.Config), quayJSON(n.EventConfig)))

	return orNotFound(updated, err, "update quay notification")
}

// DeleteQuayNotification deletes a notification and reports whether it existed.
func (p *QuayStore) DeleteQuayNotification(ctx context.Context, quayRepoID, id string) (bool, error) {
	res, err := p.db.ExecContext(ctx, `DELETE FROM quay_notifications WHERE quay_repository_id = $1 AND id::text = $2`, quayRepoID, id)
	if err != nil {
		return false, fmt.Errorf("failed to delete quay notification: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to count deleted quay notifications: %w", err)
	}

	return n > 0, nil
}
