// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// UpsertBundleCheck stores a check result: one row per (bundle, name), a new
// report replacing the previous one. c.ID, CreatedAt and UpdatedAt are filled
// from the stored row.
func (p *BundleStore) UpsertBundleCheck(ctx context.Context, c *models.BundleCheck) error {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO bundle_checks (bundle_id, name, status, summary, details_url, reported_by, expires_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now(), now())
		 ON CONFLICT (bundle_id, name) DO UPDATE
		   SET status = EXCLUDED.status, summary = EXCLUDED.summary, details_url = EXCLUDED.details_url,
		       reported_by = EXCLUDED.reported_by, expires_at = EXCLUDED.expires_at, updated_at = now()
		 RETURNING id, created_at, updated_at`,
		c.BundleID, c.Name, string(c.Status), c.Summary, c.DetailsURL, c.ReportedBy, c.ExpiresAt,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert bundle check: %w", err)
	}

	return nil
}

// ListBundleChecks returns the latest result of every check reported against
// the bundle, by name.
func (p *BundleStore) ListBundleChecks(ctx context.Context, bundleID string) ([]*models.BundleCheck, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, bundle_id, name, status, summary, details_url, reported_by, expires_at, created_at, updated_at
		 FROM bundle_checks WHERE bundle_id = $1 ORDER BY name`, bundleID)
	if err != nil {
		return nil, fmt.Errorf("failed to list bundle checks: %w", err)
	}
	defer rows.Close()

	var out []*models.BundleCheck

	for rows.Next() {
		c := &models.BundleCheck{}

		if err := rows.Scan(&c.ID, &c.BundleID, &c.Name, &c.Status, &c.Summary, &c.DetailsURL, &c.ReportedBy,
			&c.ExpiresAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan bundle check: %w", err)
		}

		out = append(out, c)
	}

	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}
