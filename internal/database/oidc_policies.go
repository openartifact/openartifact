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

// OIDCPolicyStore owns the CI OIDC trust policies: the rules that exchange an ID
// token of an external issuer for a token of a service account.
//
// Policies are read uncached: a policy an administrator disables or deletes
// must stop matching at once, and the exchange is nowhere near a hot path.
type OIDCPolicyStore struct{ *core }

// oidcPolicyColumns is the column list scanOIDCPolicy reads, FROM oidcPolicyFrom.
const oidcPolicyColumns = `p.id, p.name, p.issuer, p.audience, p.conditions, p.service_account_id, p.token_ttl_seconds,
	p.enabled, p.created_by, p.created_at, p.updated_at, u.username, u.disabled`

const oidcPolicyFrom = ` oidc_trust_policies p JOIN auth_users u ON u.id = p.service_account_id `

func scanOIDCPolicy(row rowScanner) (*models.OIDCTrustPolicy, error) {
	var (
		pol  models.OIDCTrustPolicy
		cond []byte
	)

	if err := row.Scan(&pol.ID, &pol.Name, &pol.Issuer, &pol.Audience, &cond, &pol.ServiceAccountID, &pol.TokenTTLSeconds,
		&pol.Enabled, &pol.CreatedBy, &pol.CreatedAt, &pol.UpdatedAt, &pol.ServiceAccountName, &pol.ServiceAccountDisabled); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	if err := json.Unmarshal(cond, &pol.Conditions); err != nil {
		return nil, fmt.Errorf("decode conditions of policy %s: %w", pol.ID, err)
	}

	return &pol, nil
}

// CreateOIDCPolicy stores a policy and returns it with its id and times. A name
// that is taken is IsUniqueViolation; a service account that does not exist is
// IsForeignKeyViolation.
func (p *OIDCPolicyStore) CreateOIDCPolicy(ctx context.Context, pol *models.OIDCTrustPolicy) (*models.OIDCTrustPolicy, error) {
	cond, err := json.Marshal(pol.Conditions)
	if err != nil {
		return nil, fmt.Errorf("encode conditions: %w", err)
	}

	var id string
	if err := p.db.QueryRowContext(ctx,
		`INSERT INTO oidc_trust_policies (name, issuer, audience, conditions, service_account_id, token_ttl_seconds, enabled, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8) RETURNING id`,
		pol.Name, pol.Issuer, pol.Audience, cond, pol.ServiceAccountID, pol.TokenTTLSeconds, pol.Enabled, pol.CreatedBy,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("failed to create oidc policy: %w", err)
	}

	return p.GetOIDCPolicy(ctx, id)
}

// GetOIDCPolicy returns the policy with the id, nil when there is none.
func (p *OIDCPolicyStore) GetOIDCPolicy(ctx context.Context, id string) (*models.OIDCTrustPolicy, error) {
	pol, err := scanOIDCPolicy(p.db.QueryRowContext(ctx, `SELECT `+oidcPolicyColumns+` FROM`+oidcPolicyFrom+`WHERE p.id = $1`, id))

	return orNotFound(pol, err, "get oidc policy")
}

// ListOIDCPolicies returns every policy by name.
func (p *OIDCPolicyStore) ListOIDCPolicies(ctx context.Context) ([]*models.OIDCTrustPolicy, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+oidcPolicyColumns+` FROM`+oidcPolicyFrom+`ORDER BY p.name, p.id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list oidc policies: %w", err)
	}

	return collectRows(rows, "failed to scan oidc policy", "failed to iterate oidc policies", scanOIDCPolicy)
}

// ListEnabledOIDCPoliciesByIssuer returns the enabled policies of an issuer,
// oldest first, so the first match of an exchange is stable.
func (p *OIDCPolicyStore) ListEnabledOIDCPoliciesByIssuer(ctx context.Context, issuer string) ([]*models.OIDCTrustPolicy, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+oidcPolicyColumns+` FROM`+oidcPolicyFrom+`WHERE p.issuer = $1 AND p.enabled ORDER BY p.created_at, p.id`, issuer)
	if err != nil {
		return nil, fmt.Errorf("failed to list oidc policies of issuer: %w", err)
	}

	return collectRows(rows, "failed to scan oidc policy", "failed to iterate oidc policies", scanOIDCPolicy)
}

// UpdateOIDCPolicy replaces every editable field of the policy (name, issuer,
// audience, conditions, service account, ttl, enabled) and returns it, nil when
// there is no such policy.
func (p *OIDCPolicyStore) UpdateOIDCPolicy(ctx context.Context, pol *models.OIDCTrustPolicy) (*models.OIDCTrustPolicy, error) {
	cond, err := json.Marshal(pol.Conditions)
	if err != nil {
		return nil, fmt.Errorf("encode conditions: %w", err)
	}

	res, err := p.db.ExecContext(ctx,
		`UPDATE oidc_trust_policies SET name = $2, issuer = $3, audience = $4, conditions = $5::jsonb,
			service_account_id = $6, token_ttl_seconds = $7, enabled = $8, updated_at = now()
		WHERE id = $1`,
		pol.ID, pol.Name, pol.Issuer, pol.Audience, cond, pol.ServiceAccountID, pol.TokenTTLSeconds, pol.Enabled)
	if err != nil {
		return nil, fmt.Errorf("failed to update oidc policy: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil //nolint:nilnil // not found
	}

	return p.GetOIDCPolicy(ctx, pol.ID)
}

// DeleteOIDCPolicy deletes a policy and reports whether it existed.
func (p *OIDCPolicyStore) DeleteOIDCPolicy(ctx context.Context, id string) (bool, error) {
	res, err := p.db.ExecContext(ctx, `DELETE FROM oidc_trust_policies WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("failed to delete oidc policy: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}
