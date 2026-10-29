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

// upsertDebian runs an idempotent "insert, or touch the existing row" upsert on a
// (parent id, name) keyed debian table and stores the generated columns in the model.
func (p *DebStore) upsertDebian(
	ctx context.Context,
	query, parentID, name, what string,
	id *string,
	createdAt, updatedAt *time.Time,
) error {
	if err := p.db.QueryRowContext(ctx, query, parentID, name).Scan(id, createdAt, updatedAt); err != nil {
		return fmt.Errorf("get or create debian %s: %w", what, err)
	}

	return nil
}

// GetOrCreateDebianDistribution returns the distribution, creating it atomically if missing.
func (p *DebStore) GetOrCreateDebianDistribution(ctx context.Context, repoID, name string) (*models.DebianDistribution, error) {
	m := &models.DebianDistribution{RepositoryID: repoID, Name: name}

	err := p.upsertDebian(ctx, `INSERT INTO debian_distributions (repository_id, name) VALUES ($1, $2)
		 ON CONFLICT (repository_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, created_at, updated_at`, repoID, name, "distribution", &m.ID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return m, nil
}

// GetOrCreateDebianComponent returns the component, creating it atomically if missing.
func (p *DebStore) GetOrCreateDebianComponent(ctx context.Context, distID, name string) (*models.DebianComponent, error) {
	m := &models.DebianComponent{DistributionID: distID, Name: name}

	err := p.upsertDebian(ctx, `INSERT INTO debian_components (distribution_id, name) VALUES ($1, $2)
		 ON CONFLICT (distribution_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, created_at, updated_at`, distID, name, "component", &m.ID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return m, nil
}

// GetOrCreateDebianArchitecture returns the architecture, creating it atomically if missing.
func (p *DebStore) GetOrCreateDebianArchitecture(ctx context.Context, distID, name string) (*models.DebianArchitecture, error) {
	m := &models.DebianArchitecture{DistributionID: distID, Name: name}

	err := p.upsertDebian(ctx, `INSERT INTO debian_architectures (distribution_id, name) VALUES ($1, $2)
		 ON CONFLICT (distribution_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, created_at, updated_at`, distID, name, "architecture", &m.ID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return m, nil
}

// SetDebControlFields stores the control stanza of a live deb package (deb_files.control_fields)
// and its maintainer and homepage (the version metadata).
func (p *DebStore) SetDebControlFields(ctx context.Context, packageID, maintainer, homepage string, fields map[string]string) error {
	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshal control fields: %w", err)
	}

	meta, err := json.Marshal(nonEmptyStrings(map[string]string{"maintainer": maintainer, "homepage": homepage}))
	if err != nil {
		return fmt.Errorf("marshal deb metadata: %w", err)
	}

	if err := p.q.SetDebControlFields(ctx, dbsqlc.SetDebControlFieldsParams{
		ID: packageID, ControlFields: raw, Metadata: meta,
	}); err != nil {
		return fmt.Errorf("set deb control fields: %w", err)
	}

	return nil
}

// GetDebBlobInfo resolves a blobs.id UUID to its content sha256 and size ("" / 0 if unknown).
func (p *DebStore) GetDebBlobInfo(ctx context.Context, blobID string) (string, int64, error) {
	var (
		sum  string
		size int64
	)

	err := p.db.QueryRowContext(ctx, `SELECT COALESCE(sha256, ''), COALESCE(size_bytes, 0) FROM blobs WHERE id = $1`, blobID).
		Scan(&sum, &size)
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
func (p *DebStore) ListDebPackageEntries(ctx context.Context, repoID, dist, comp, arch string) ([]models.DebPackageEntry, error) {
	rows, err := p.q.ListDebPackageEntries(ctx, dbsqlc.ListDebPackageEntriesParams{
		RepositoryID: repoID, Dist: dist, Comp: comp, Arch: arch,
	})
	if err != nil {
		return nil, fmt.Errorf("list deb package entries: %w", err)
	}

	var out []models.DebPackageEntry

	for _, row := range rows {
		e := models.DebPackageEntry{
			ID:             row.ID,
			Name:           row.Name,
			Version:        row.Version,
			Arch:           row.Arch,
			Filename:       row.Filename,
			DistributionID: row.DistributionID,
			Distribution:   row.Distribution,
			ComponentName:  row.ComponentName,
			SHA256:         row.Sha256,
			Size:           row.Size,
			InstalledSize:  row.InstalledSize,
		}

		if len(row.ControlFields) > 0 {
			if err := json.Unmarshal(row.ControlFields, &e.ControlFields); err != nil {
				return nil, fmt.Errorf("decode control fields for %s: %w", e.ID, err)
			}
		}

		out = append(out, e)
	}

	return out, nil
}
