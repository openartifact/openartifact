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

// GetOrCreateDebianDistribution returns the distribution, creating it atomically if missing.
func (p *PostgresDB) GetOrCreateDebianDistribution(ctx context.Context, repoID, name string) (*models.DebianDistribution, error) {
	d := &models.DebianDistribution{RepositoryID: repoID, Name: name}

	err := p.db.QueryRowContext(ctx,
		`INSERT INTO debian_distributions (repository_id, name) VALUES ($1, $2)
		 ON CONFLICT (repository_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, created_at, updated_at`, repoID, name).
		Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get or create debian distribution: %w", err)
	}

	return d, nil
}

// GetOrCreateDebianComponent returns the component, creating it atomically if missing.
func (p *PostgresDB) GetOrCreateDebianComponent(ctx context.Context, distID, name string) (*models.DebianComponent, error) {
	c := &models.DebianComponent{DistributionID: distID, Name: name}

	err := p.db.QueryRowContext(ctx,
		`INSERT INTO debian_components (distribution_id, name) VALUES ($1, $2)
		 ON CONFLICT (distribution_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, created_at, updated_at`, distID, name).
		Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get or create debian component: %w", err)
	}

	return c, nil
}

// GetOrCreateDebianArchitecture returns the architecture, creating it atomically if missing.
func (p *PostgresDB) GetOrCreateDebianArchitecture(ctx context.Context, distID, name string) (*models.DebianArchitecture, error) {
	a := &models.DebianArchitecture{DistributionID: distID, Name: name}

	err := p.db.QueryRowContext(ctx,
		`INSERT INTO debian_architectures (distribution_id, name) VALUES ($1, $2)
		 ON CONFLICT (distribution_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, created_at, updated_at`, distID, name).
		Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get or create debian architecture: %w", err)
	}

	return a, nil
}

// SetDebControlFields stores the control stanza, maintainer and homepage of a deb package.
func (p *PostgresDB) SetDebControlFields(ctx context.Context, packageID, maintainer, homepage string, fields map[string]string) error {
	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshal control fields: %w", err)
	}

	_, err = p.db.ExecContext(ctx,
		`UPDATE deb_packages SET control_fields = $2::jsonb, maintainer = $3, homepage = $4 WHERE id = $1 AND deleted_at IS NULL`,
		packageID, string(raw), maintainer, homepage)
	if err != nil {
		return fmt.Errorf("set deb control fields: %w", err)
	}

	return nil
}

// GetDebBlobInfo resolves a blobs.id UUID to its content sha256 and size ("" / 0 if unknown).
func (p *PostgresDB) GetDebBlobInfo(ctx context.Context, blobID string) (string, int64, error) {
	var (
		sum  string
		size int64
	)

	err := p.db.QueryRowContext(ctx, `SELECT COALESCE(sha256, ''), COALESCE(size_bytes, 0) FROM blobs WHERE id = $1`, blobID).Scan(&sum, &size)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, nil
		}

		return "", 0, fmt.Errorf("get blob info: %w", err)
	}

	return sum, size, nil
}

// ListDebPackageEntries returns package entries for a repository in a single
// query. Empty dist/comp/arch act as wildcards.
func (p *PostgresDB) ListDebPackageEntries(ctx context.Context, repoID, dist, comp, arch string) ([]models.DebPackageEntry, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT dp.id, dp.name, dp.version, dp.arch, COALESCE(dp.filename, ''),
		        dp.distribution_id, dd.name, dc.name,
		        COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0), COALESCE(dp.installed_size, 0),
		        dp.control_fields
		 FROM deb_packages dp
		 LEFT JOIN blobs b ON b.id = dp.blob_id
		 JOIN debian_distributions dd ON dd.id = dp.distribution_id
		 JOIN debian_components dc ON dc.id = dp.component_id
		 WHERE dp.repository_id = $1 AND dp.deleted_at IS NULL
		   AND ($2 = '' OR dd.name = $2)
		   AND ($3 = '' OR dc.name = $3)
		   AND ($4 = '' OR dp.arch = $4)
		 ORDER BY dd.name, dc.name, dp.arch, dp.name, dp.version`, repoID, dist, comp, arch)
	if err != nil {
		return nil, fmt.Errorf("list deb package entries: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []models.DebPackageEntry

	for rows.Next() {
		var (
			e   models.DebPackageEntry
			raw []byte
		)

		if err := rows.Scan(&e.ID, &e.Name, &e.Version, &e.Arch, &e.Filename,
			&e.DistributionID, &e.Distribution, &e.ComponentName,
			&e.SHA256, &e.Size, &e.InstalledSize, &raw); err != nil {
			return nil, fmt.Errorf("scan deb package entry: %w", err)
		}

		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &e.ControlFields); err != nil {
				return nil, fmt.Errorf("decode control fields for %s: %w", e.ID, err)
			}
		}

		out = append(out, e)
	}

	return out, rows.Err()
}
