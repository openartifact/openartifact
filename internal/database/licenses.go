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

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// LicenseStore owns license scan results and license policies.
type LicenseStore struct{ *core }

// ErrLicenseScanNotFound is returned when a license scan result does not exist in the repository.
var ErrLicenseScanNotFound = errors.New("license scan result not found")

// SetLicenseScanQuarantine quarantines (true) or approves (false) the artifact
// of exactly one license scan result (by ID, scoped to repoID). Approving
// records the decision for the licenses currently stored, so a later scan that
// finds the same licenses does not quarantine the artifact again; quarantining
// clears it. Returns ErrLicenseScanNotFound when nothing matched.
func (p *LicenseStore) SetLicenseScanQuarantine(ctx context.Context, repoID, id string, quarantined bool) error {
	return p.execMatching(ctx, "set license scan quarantine", ErrLicenseScanNotFound, `
		UPDATE license_results lr SET quarantined = $3, quarantine_approved = NOT $3
		FROM scan_jobs sj, artifact_refs ref
		WHERE lr.id = $2 AND sj.id = lr.scan_job_id AND sj.scan_type = 'license'
		  AND ref.id = sj.artifact_ref_id AND ref.repository_id = $1`, repoID, id, quarantined)
}

// CreateLicenseScanResult creates a new license scan result (UPSERT).
func (p *LicenseStore) CreateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error {
	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	err := p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
			SELECT ref.id, 'license', '', $3, NULLIF($4, ''), $5 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message,
				completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id
		)
		INSERT INTO license_results (scan_job_id, licenses, spdx_expressions, quarantined, quarantine_approved,
			unresolved_licenses, license_source)
		SELECT job.id, $6, $7, $8, $9, $10, $11 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			licenses = EXCLUDED.licenses, spdx_expressions = EXCLUDED.spdx_expressions,
			quarantined = EXCLUDED.quarantined, quarantine_approved = EXCLUDED.quarantine_approved,
			unresolved_licenses = EXCLUDED.unresolved_licenses, license_source = EXCLUDED.license_source
		RETURNING id`,
		result.RepositoryID, result.ArtifactID, result.ScanStatus, result.ScanError, scannedAt,
		nonNilStrings(result.Licenses), nonNilStrings(result.SPDXExpressions), result.Quarantined, result.QuarantineApproved,
		nonNilStrings(result.UnresolvedLicenses), licenseSourceOrDefault(result.LicenseSource),
	).Scan(&result.ID)
	if err != nil {
		return fmt.Errorf("failed to create license scan result: %w", err)
	}

	return nil
}

// licenseSourceOrDefault maps an unset source onto the conservative
// "client_declared".
func licenseSourceOrDefault(source string) string {
	if source == "" {
		return models.LicenseSourceClientDeclared
	}

	return source
}

// GetLicenseScanResult gets a license scan result by repo and artifact ID.
func (p *LicenseStore) GetLicenseScanResult(ctx context.Context, repoID, artifactID string) (*models.LicenseScanResult, error) {
	result, err := scanLicenseResult(p.db.QueryRowContext(ctx, `
		SELECT `+licenseResultColumns+`
		FROM license_results lr
		JOIN scan_jobs sj ON sj.id = lr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'license' AND ref.repository_id = $1 AND ref.artifact_path = $2`,
		repoID, artifactID,
	))

	return orNotFound(result, err, "get license scan result")
}

// ListLicenseScanResults lists all license scan results for a repository.
func (p *LicenseStore) ListLicenseScanResults(ctx context.Context, repoID string) ([]*models.LicenseScanResult, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+licenseResultColumns+`
		FROM license_results lr
		JOIN scan_jobs sj ON sj.id = lr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'license' AND ref.repository_id = $1
		ORDER BY sj.created_at DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list license scan results: %w", err)
	}
	defer rows.Close()

	var results []*models.LicenseScanResult

	for rows.Next() {
		result, err := scanLicenseResult(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan license scan result: %w", err)
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// CreateLicensePolicy creates a new license policy.
func (p *LicenseStore) CreateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	id, err := p.q.CreateLicensePolicy(ctx, dbsqlc.CreateLicensePolicyParams{
		RepositoryID:    policy.RepositoryID,
		Name:            policy.Name,
		Description:     strPtr(policy.Description),
		Action:          policy.Action,
		AllowedLicenses: nonNilStrings(policy.AllowedLicenses),
		DeniedLicenses:  nonNilStrings(policy.DeniedLicenses),
		Enabled:         policy.Enabled,
		CreatedAt:       orNow(policy.CreatedAt),
		UpdatedAt:       orNow(policy.UpdatedAt),
	})
	if err != nil {
		return fmt.Errorf("failed to create license policy: %w", err)
	}

	policy.ID = id

	return nil
}

// GetLicensePolicy gets a license policy by ID.
func (p *LicenseStore) GetLicensePolicy(ctx context.Context, id string) (*models.LicensePolicy, error) {
	row, err := p.q.GetLicensePolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get license policy: %w", err)
	}

	return &models.LicensePolicy{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		Description:     derefStr(row.Description),
		Action:          row.Action,
		AllowedLicenses: row.AllowedLicenses,
		DeniedLicenses:  row.DeniedLicenses,
		Enabled:         row.Enabled,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

// UpdateLicensePolicy updates a license policy.
func (p *LicenseStore) UpdateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	err := p.q.UpdateLicensePolicy(ctx, dbsqlc.UpdateLicensePolicyParams{
		ID:              policy.ID,
		Name:            policy.Name,
		Description:     strPtr(policy.Description),
		Action:          policy.Action,
		AllowedLicenses: nonNilStrings(policy.AllowedLicenses),
		DeniedLicenses:  nonNilStrings(policy.DeniedLicenses),
		Enabled:         policy.Enabled,
		UpdatedAt:       policy.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("failed to update license policy: %w", err)
	}

	return nil
}

// DeleteLicensePolicy deletes a license policy by ID.
func (p *LicenseStore) DeleteLicensePolicy(ctx context.Context, id string) error {
	rowsAffected, err := p.q.DeleteLicensePolicy(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete license policy: %w", err)
	}

	if rowsAffected == 0 {
		return ErrLicensePolicyNotFound
	}

	return nil
}

// ListLicensePolicies lists all license policies for a repository (including global policies).
func (p *LicenseStore) ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error) {
	rows, err := p.q.ListLicensePolicies(ctx, &repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list license policies: %w", err)
	}

	var policies []*models.LicensePolicy

	for _, row := range rows {
		policy := &models.LicensePolicy{
			ID:              row.ID,
			RepositoryID:    row.RepositoryID,
			Name:            row.Name,
			Description:     derefStr(row.Description),
			Action:          row.Action,
			AllowedLicenses: row.AllowedLicenses,
			DeniedLicenses:  row.DeniedLicenses,
			Enabled:         row.Enabled,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
		}
		policies = append(policies, policy)
	}

	return policies, nil
}

// GetLicenseScanResultByID returns the license scan result with the given ID scoped to repoID, or nil.
func (p *LicenseStore) GetLicenseScanResultByID(ctx context.Context, repoID, id string) (*models.LicenseScanResult, error) {
	result, err := scanLicenseResult(p.db.QueryRowContext(ctx, `
		SELECT `+licenseResultColumns+`
		FROM license_results lr
		JOIN scan_jobs sj ON sj.id = lr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'license' AND ref.repository_id = $1 AND lr.id = $2`, repoID, id,
	))

	return orNotFound(result, err, "get license scan result by id")
}

// ListGlobalLicensePolicies lists only the global (repository_id IS NULL) license policies.
func (p *LicenseStore) ListGlobalLicensePolicies(ctx context.Context) ([]*models.LicensePolicy, error) {
	// A NULL repository filter makes "repository_id = $1" never match, leaving
	// only the "repository_id IS NULL" branch of the shared query.
	rows, err := p.q.ListLicensePolicies(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list global license policies: %w", err)
	}

	policies := make([]*models.LicensePolicy, 0, len(rows))

	for _, row := range rows {
		policies = append(policies, &models.LicensePolicy{
			ID:              row.ID,
			RepositoryID:    row.RepositoryID,
			Name:            row.Name,
			Description:     derefStr(row.Description),
			Action:          row.Action,
			AllowedLicenses: row.AllowedLicenses,
			DeniedLicenses:  row.DeniedLicenses,
			Enabled:         row.Enabled,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
		})
	}

	return policies, nil
}
