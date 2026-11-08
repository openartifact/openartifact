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

	"github.com/openartifact/openartifact/internal/database/models"
)

// SSOStore owns the configured SSO providers (OIDC and SAML).
type SSOStore struct{ *core }

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

func (p *SSOStore) CreateSSOProvider(ctx context.Context, provider *models.SSOProvider) error {
	groupMappingJSON, err := json.Marshal(provider.GroupMapping)
	if err != nil {
		return fmt.Errorf("failed to marshal group_mapping: %w", err)
	}

	permissionMappingJSON, err := json.Marshal(provider.PermissionMapping)
	if err != nil {
		return fmt.Errorf("failed to marshal permission_mapping: %w", err)
	}

	return p.db.QueryRowContext(
		ctx,
		`INSERT INTO auth_sso_providers (slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping,
			permission_mapping, auto_provision_users, link_by_email)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9::jsonb, $10, $11)
		RETURNING id, created_at, updated_at`,
		provider.Slug,
		provider.Kind,
		provider.DisplayName,
		provider.Enabled,
		provider.OIDCConfig,
		provider.SAMLConfig,
		provider.ClaimMapping,
		groupMappingJSON,
		permissionMappingJSON,
		provider.AutoProvisionUsers,
		provider.LinkByEmail,
	).Scan(&provider.ID, &provider.CreatedAt, &provider.UpdatedAt)
}

func (p *SSOStore) GetSSOProviderBySlug(ctx context.Context, slug string) (*models.SSOProvider, error) {
	row := p.db.QueryRowContext(
		ctx,
		`SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping,
			auto_provision_users, link_by_email, created_at, updated_at
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

// SSOProviderPatch names the columns an admin edit writes; nil fields leave
// the stored value alone. A nil mapping pointer is unchanged, a pointer to a
// nil map writes JSON null.
type SSOProviderPatch struct {
	DisplayName        *string
	Enabled            *bool
	AutoProvisionUsers *bool
	LinkByEmail        *bool
	OIDCConfig         json.RawMessage
	SAMLConfig         json.RawMessage
	ClaimMapping       json.RawMessage
	GroupMapping       *map[string]string
	PermissionMapping  *map[string]string
}

// marshalOptional is nil for an absent value, else its JSON encoding.
func marshalOptional[T any](v *T, field string) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // nil is the SQL NULL that leaves the column unchanged
	}

	b, err := json.Marshal(*v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s: %w", field, err)
	}

	return b, nil
}

// rawOptional is nil for an absent document, so the column keeps its value.
func rawOptional(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}

	return []byte(raw)
}

// PatchSSOProvider writes only the columns in patch, so a concurrent edit of
// other columns is not reverted by a stale snapshot. It returns the updated
// row, or nil when the provider does not exist.
func (p *SSOStore) PatchSSOProvider(ctx context.Context, id string, patch SSOProviderPatch) (*models.SSOProvider, error) {
	groupMapping, err := marshalOptional(patch.GroupMapping, "group_mapping")
	if err != nil {
		return nil, err
	}

	permissionMapping, err := marshalOptional(patch.PermissionMapping, "permission_mapping")
	if err != nil {
		return nil, err
	}

	row := p.db.QueryRowContext(
		ctx,
		`UPDATE auth_sso_providers SET
			display_name = COALESCE($2, display_name),
			enabled = COALESCE($3, enabled),
			oidc_config = COALESCE($4::jsonb, oidc_config),
			saml_config = COALESCE($5::jsonb, saml_config),
			claim_mapping = COALESCE($6::jsonb, claim_mapping),
			group_mapping = COALESCE($7::jsonb, group_mapping),
			permission_mapping = COALESCE($8::jsonb, permission_mapping),
			auto_provision_users = COALESCE($9, auto_provision_users),
			link_by_email = COALESCE($10, link_by_email)
		WHERE id = $1
		RETURNING id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping,
			auto_provision_users, link_by_email, created_at, updated_at`,
		id,
		patch.DisplayName,
		patch.Enabled,
		rawOptional(patch.OIDCConfig),
		rawOptional(patch.SAMLConfig),
		rawOptional(patch.ClaimMapping),
		groupMapping,
		permissionMapping,
		patch.AutoProvisionUsers,
		patch.LinkByEmail,
	)

	provider, err := scanSSOProvider(row)

	return orNotFound(provider, err, "update sso provider")
}

func (p *SSOStore) DeleteSSOProvider(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM auth_sso_providers WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete sso provider: %w", err)
	}

	return nil
}

func (p *SSOStore) ListEnabledSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	rows, err := p.db.QueryContext(
		ctx,
		`SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping,
			auto_provision_users, link_by_email, created_at, updated_at
		FROM auth_sso_providers WHERE enabled = TRUE ORDER BY slug ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled sso providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanSSOProviderRows(rows)
}

func (p *SSOStore) ListAllSSOProviders(ctx context.Context) ([]*models.SSOProvider, error) {
	rows, err := p.db.QueryContext(
		ctx,
		`SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping,
			auto_provision_users, link_by_email, created_at, updated_at
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
