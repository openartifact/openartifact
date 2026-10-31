// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ServiceAccountStore owns the administrator-managed service accounts: auth_users
// rows of kind service_account. They share the users table (so tokens,
// permission assignments, group memberships and project roles work unchanged)
// but are never listed with the users and never log in.
type ServiceAccountStore struct{ *core }

// CreateServiceAccount creates a service account. The name is its username,
// unique among all accounts (a clash is IsUniqueViolation).
func (p *ServiceAccountStore) CreateServiceAccount(
	ctx context.Context,
	name string,
	description *string,
	disabled bool,
) (*models.User, error) {
	defer p.authCache.invalidate()

	u, err := scanUser(p.db.QueryRowContext(ctx,
		`INSERT INTO auth_users (username, full_name, disabled, kind) VALUES ($1, $2, $3, 'service_account')
		RETURNING `+bareUserColumns, name, description, disabled))
	if err != nil {
		return nil, fmt.Errorf("failed to create service account: %w", err)
	}

	return u, nil
}

// bareUserColumns is userColumns without the table alias, for RETURNING.
const bareUserColumns = `id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at,
	created_at, updated_at, external_id, provider_slug, sso_managed_groups, kind, project_id`

// GetServiceAccount returns the service account with the id, nil when there is
// none (a user of another kind is not one).
func (p *ServiceAccountStore) GetServiceAccount(ctx context.Context, id string) (*models.User, error) {
	u, err := scanUser(p.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM auth_users u WHERE u.id = $1 AND u.kind = 'service_account'`, id))

	return orNotFound(u, err, "get service account")
}

// ListServiceAccounts returns one page of service accounts by name and the total.
func (p *ServiceAccountStore) ListServiceAccounts(ctx context.Context, page, perPage int) ([]*models.User, int, error) {
	var total int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE kind = 'service_account'`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count service accounts: %w", err)
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM auth_users u WHERE u.kind = 'service_account' ORDER BY u.username, u.id LIMIT $1 OFFSET $2`,
		perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list service accounts: %w", err)
	}

	accounts, err := collectRows(rows, "failed to scan service account", "failed to iterate service accounts", scanUser)
	if err != nil {
		return nil, 0, err
	}

	return accounts, total, nil
}

// UpdateServiceAccount sets the description (when non-nil) and the disabled flag
// (when non-nil) and returns the account, nil when there is no such account.
// Disabling takes effect for its tokens at once: a token of a disabled account
// authenticates nothing.
func (p *ServiceAccountStore) UpdateServiceAccount(
	ctx context.Context, id string, description *string, disabled *bool,
) (*models.User, error) {
	defer p.authCache.invalidate()

	u, err := scanUser(p.db.QueryRowContext(ctx,
		`UPDATE auth_users SET
			full_name = CASE WHEN $2::boolean THEN $3 ELSE full_name END,
			disabled = COALESCE($4, disabled),
			updated_at = now()
		WHERE id = $1 AND kind = 'service_account'
		RETURNING `+bareUserColumns, id, description != nil, description, disabled))

	return orNotFound(u, err, "update service account")
}

// DeleteServiceAccount deletes a service account with everything that belongs to
// it: tokens, group memberships, project roles and trust policies go by their
// foreign keys, the direct permission assignments (which have none) are removed
// explicitly. It reports whether the account existed.
func (p *ServiceAccountStore) DeleteServiceAccount(ctx context.Context, id string) (bool, error) {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin delete service account tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM auth_users WHERE id = $1 AND kind = 'service_account')`, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("delete service account lookup: %w", err)
	}

	if !exists {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM permission_assignments WHERE principal_type = 'user' AND principal_id = $1`, id); err != nil {
		return false, fmt.Errorf("delete service account assignments: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_users WHERE id = $1`, id); err != nil {
		return false, fmt.Errorf("delete service account: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit delete service account: %w", err)
	}

	return true, nil
}
