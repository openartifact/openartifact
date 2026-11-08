-- name: CreateSSOProvider :one
INSERT INTO auth_sso_providers (slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email)
VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9::jsonb, $10, $11)
RETURNING id, created_at, updated_at;

-- name: GetSSOProviderBySlug :one
SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email, created_at, updated_at
FROM auth_sso_providers WHERE slug = $1;

-- name: DeleteSSOProvider :exec
DELETE FROM auth_sso_providers WHERE id = $1;

-- name: ListEnabledSSOProviders :many
SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email, created_at, updated_at
FROM auth_sso_providers WHERE enabled = TRUE ORDER BY slug ASC;

-- name: ListAllSSOProviders :many
SELECT id, slug, kind, display_name, enabled, oidc_config, saml_config, claim_mapping, group_mapping, permission_mapping, auto_provision_users, link_by_email, created_at, updated_at
FROM auth_sso_providers ORDER BY slug ASC;

-- name: UpdateUserSSO :exec
UPDATE auth_users SET external_id = $2, provider_slug = $3 WHERE id = $1;
