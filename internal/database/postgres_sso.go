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

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

func scanSSOProvider(row interface {
	Scan(dest ...any) error
}) (*models.SSOProvider, error) {
	p := &models.SSOProvider{}

	var oidcConfig, samlConfig, claimMapping, groupMappingRaw, permissionMappingRaw []byte

	err := row.Scan(
		&p.ID, &p.Slug, &p.Kind, &p.DisplayName, &p.Enabled,
		&oidcConfig, &samlConfig, &claimMapping, &groupMappingRaw, &permissionMappingRaw,
		&p.AutoProvisionUsers, &p.LinkByEmail, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	p.OIDCConfig = oidcConfig
	p.SAMLConfig = samlConfig

	p.ClaimMapping = claimMapping
	if len(groupMappingRaw) > 0 {
		if err := json.Unmarshal(groupMappingRaw, &p.GroupMapping); err != nil {
			return nil, fmt.Errorf("failed to unmarshal group_mapping: %w", err)
		}
	}

	if p.GroupMapping == nil {
		p.GroupMapping = make(map[string]string)
	}

	if len(permissionMappingRaw) > 0 {
		if err := json.Unmarshal(permissionMappingRaw, &p.PermissionMapping); err != nil {
			return nil, fmt.Errorf("failed to unmarshal permission_mapping: %w", err)
		}
	}

	if p.PermissionMapping == nil {
		p.PermissionMapping = make(map[string]string)
	}

	return p, nil
}

func (p *PostgresDB) CreateSSOProvider(ctx context.Context, provider *models.SSOProvider) error {
	groupMappingJSON, err := json.Marshal(provider.GroupMapping)
	if err != nil {
		return fmt.Errorf("failed to marshal group_mapping: %w", err)
	}

	permissionMappingJSON, err := json.Marshal(provider.PermissionMapping)
	if err != nil {
		return fmt.Errorf("failed to marshal permission_mapping: %w", err)
	}

	return p.db.QueryRowContext(ctx,
		`INSERT INTO auth_sso_providers (slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9::jsonb, $10, $11)
		RETURNING id, created_at, updated_at`,
		provider.Slug, provider.Kind, provider.DisplayName, provider.Enabled,
		provider.OIDCConfig, provider.SAMLConfig, provider.ClaimMapping, groupMappingJSON, permissionMappingJSON,
		provider.AutoProvisionUsers, provider.LinkByEmail,
	).Scan(&provider.ID, &provider.CreatedAt, &provider.UpdatedAt)
}

func (p *PostgresDB) GetSSOProviderBySlug(ctx context.Context, slug string) (*models.SSOProvider, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email, created_at, updated_at
		FROM auth_sso_providers WHERE slug = $1`,
		slug,
	)

	provider, err := scanSSOProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get sso provider by slug: %w", err)
	}

	return provider, nil
}

func (p *PostgresDB) UpdateSSOProvider(ctx context.Context, provider *models.SSOProvider) error {
	groupMappingJSON, err := json.Marshal(provider.GroupMapping)
	if err != nil {
		return fmt.Errorf("failed to marshal group_mapping: %w", err)
	}

	permissionMappingJSON, err := json.Marshal(provider.PermissionMapping)
	if err != nil {
		return fmt.Errorf("failed to marshal permission_mapping: %w", err)
	}

	_, err = p.db.ExecContext(ctx,
		`UPDATE auth_sso_providers SET slug=$2, kind=$3, display_name=$4, enabled=$5, oidc_config=$6::jsonb, saml_config=$7::jsonb, claim_mapping=$8::jsonb, group_mapping=$9::jsonb, permission_mapping=$10::jsonb, auto_provision_users=$11, link_by_email=$12
		WHERE id=$1`,
		provider.ID, provider.Slug, provider.Kind, provider.DisplayName, provider.Enabled,
		provider.OIDCConfig, provider.SAMLConfig, provider.ClaimMapping, groupMappingJSON, permissionMappingJSON,
		provider.AutoProvisionUsers, provider.LinkByEmail,
	)
	if err != nil {
		return fmt.Errorf("failed to update sso provider: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteSSOProvider(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM auth_sso_providers WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete sso provider: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListEnabledSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email, created_at, updated_at
		FROM auth_sso_providers WHERE enabled = TRUE ORDER BY slug ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled sso providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanSSOProviderRows(rows)
}

func (p *PostgresDB) ListAllSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email, created_at, updated_at
		FROM auth_sso_providers ORDER BY slug ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list all sso providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanSSOProviderRows(rows)
}

func scanSSOProviderRows(rows *sql.Rows) ([]*models.SSOProvider, error) {
	var providers []*models.SSOProvider

	for rows.Next() {
		provider, err := scanSSOProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan sso provider: %w", err)
		}

		providers = append(providers, provider)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate sso providers: %w", err)
	}

	return providers, nil
}

func (p *PostgresDB) UpdateUserSSO(ctx context.Context, userID string, externalID, providerSlug string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx,
		`UPDATE auth_users SET external_id=$2, provider_slug=$3 WHERE id=$1`,
		userID, externalID, providerSlug,
	)
	if err != nil {
		return fmt.Errorf("failed to update user sso: %w", err)
	}

	return nil
}

func (p *PostgresDB) SyncSSOGroups(ctx context.Context, userID string, groupIDs []string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`DELETE FROM auth_user_groups WHERE user_id=$1 AND is_sso_managed=TRUE`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete sso-managed groups: %w", err)
	}

	for _, groupID := range groupIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO auth_user_groups (user_id, group_id, is_sso_managed) VALUES ($1, $2, TRUE) ON CONFLICT DO NOTHING`,
			userID, groupID,
		)
		if err != nil {
			return fmt.Errorf("failed to insert sso group membership: %w", err)
		}
	}

	if err := revokeExcessProjectTokens(ctx, tx, ""); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit sso group sync: %w", err)
	}

	return nil
}

func (p *PostgresDB) GetUserByExternalID(ctx context.Context, externalID, providerSlug string) (*models.User, error) {
	u := &models.User{}

	var (
		email, passwordHash, fullName, extID, provSlug sql.NullString
		lastLoginAt                                    sql.NullTime
	)

	err := p.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
		FROM auth_users WHERE external_id=$1 AND provider_slug=$2`,
		externalID, providerSlug,
	).Scan(&u.ID, &u.Username, &email, &passwordHash, &fullName, &u.Disabled, &u.MustChangePassword, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt, &extID, &provSlug, &u.SSOManagedGroups)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get user by external id: %w", err)
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

	if extID.Valid {
		u.ExternalID = &extID.String
	}

	if provSlug.Valid {
		u.ProviderSlug = &provSlug.String
	}

	return u, nil
}

func (p *PostgresDB) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	u := &models.User{}

	var (
		emailVal, passwordHash, fullName, externalID, providerSlug sql.NullString
		lastLoginAt                                                sql.NullTime
	)

	err := p.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, full_name, disabled, must_change_password, last_login_at, created_at, updated_at, external_id, provider_slug, sso_managed_groups
		FROM auth_users WHERE email=$1`,
		email,
	).Scan(&u.ID, &u.Username, &emailVal, &passwordHash, &fullName, &u.Disabled, &u.MustChangePassword, &lastLoginAt, &u.CreatedAt, &u.UpdatedAt, &externalID, &providerSlug, &u.SSOManagedGroups)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	if emailVal.Valid {
		u.Email = &emailVal.String
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

	return u, nil
}

func (p *PostgresDB) GetGroupByName(ctx context.Context, name string) (*models.Group, error) {
	g := &models.Group{}

	var description sql.NullString

	err := p.db.QueryRowContext(ctx,
		`SELECT id, name, description, created_at, updated_at FROM auth_groups WHERE name = $1`,
		name,
	).Scan(&g.ID, &g.Name, &description, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get group by name: %w", err)
	}

	if description.Valid {
		g.Description = &description.String
	}

	return g, nil
}

func (p *PostgresDB) GetOrCreateGroup(ctx context.Context, name string) (*models.Group, error) {
	existing, err := p.GetGroupByName(ctx, name)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		return existing, nil
	}

	now := time.Now().UTC()
	desc := "Auto-created by SSO group sync"
	g := &models.Group{
		ID:          uuid.New().String(),
		Name:        name,
		Description: &desc,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	return p.CreateGroup(ctx, g)
}

func (p *PostgresDB) GetOrCreatePermissionAssignment(ctx context.Context, targetID, principalType, principalID string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx,
		`INSERT INTO permission_assignments (permission_target_id, principal_type, principal_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (permission_target_id, principal_type, principal_id) DO NOTHING`,
		targetID, principalType, principalID,
	)
	if err != nil {
		return fmt.Errorf("failed to get or create permission assignment: %w", err)
	}

	return nil
}
