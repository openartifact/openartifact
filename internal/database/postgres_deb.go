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

// debianKey is how one (parent id, name) keyed debian table is read and created
// by getOrCreateDebian. Both statements take ($1 parent id, $2 name) and return
// (id, created_at, updated_at); they are constants of this file, never user input.
type debianKey struct {
	what   string // for the error message
	lookup string // SELECT of the row
	insert string // INSERT ... ON CONFLICT DO NOTHING RETURNING
}

var (
	debianDistributionKey = debianKey{ //nolint:gochecknoglobals // constant statements
		what: "distribution",
		lookup: `SELECT id, created_at, updated_at FROM debian_distributions
			 WHERE repository_id = $1 AND name = $2`,
		insert: `INSERT INTO debian_distributions (repository_id, name) VALUES ($1, $2)
			 ON CONFLICT (repository_id, name) DO NOTHING
			 RETURNING id, created_at, updated_at`,
	}
	debianComponentKey = debianKey{ //nolint:gochecknoglobals // constant statements
		what: "component",
		lookup: `SELECT id, created_at, updated_at FROM debian_components
			 WHERE distribution_id = $1 AND name = $2`,
		insert: `INSERT INTO debian_components (distribution_id, name) VALUES ($1, $2)
			 ON CONFLICT (distribution_id, name) DO NOTHING
			 RETURNING id, created_at, updated_at`,
	}
	debianArchitectureKey = debianKey{ //nolint:gochecknoglobals // constant statements
		what: "architecture",
		lookup: `SELECT id, created_at, updated_at FROM debian_architectures
			 WHERE distribution_id = $1 AND name = $2`,
		insert: `INSERT INTO debian_architectures (distribution_id, name) VALUES ($1, $2)
			 ON CONFLICT (distribution_id, name) DO NOTHING
			 RETURNING id, created_at, updated_at`,
	}
)

// getOrCreateDebian returns the row of a (parent id, name) keyed debian table
// and stores its generated columns in the model, creating the row when missing.
//
// Every deb upload calls it for the distribution, the component and the
// architecture, and all but the first upload of a name find the row, so the
// lookup is a plain read: no row lock, no new row version, no updated_at bump
// and no recovery-journal write (an ON CONFLICT DO UPDATE upsert did all of
// those on every upload and serialized concurrent uploads on one row). Only a
// miss inserts; DO NOTHING returns no row when a concurrent upload created it
// first, and the second lookup then reads the winner's row.
func (p *DebStore) getOrCreateDebian(
	ctx context.Context,
	k debianKey,
	parentID, name string,
	id *string,
	createdAt, updatedAt *time.Time,
) error {
	// Look up; insert on a miss; look up again when the insert lost the race.
	for _, query := range [...]string{k.lookup, k.insert, k.lookup} {
		err := p.db.QueryRowContext(ctx, query, parentID, name).Scan(id, createdAt, updatedAt)
		if err == nil {
			return nil
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get or create debian %s: %w", k.what, err)
		}
	}

	// The row existed when the insert conflicted and was gone again at the
	// second lookup (its parent was deleted in between).
	return fmt.Errorf("get or create debian %s: %w", k.what, sql.ErrNoRows)
}

// GetOrCreateDebianDistribution returns the distribution, creating it atomically if missing.
func (p *DebStore) GetOrCreateDebianDistribution(ctx context.Context, repoID, name string) (*models.DebianDistribution, error) {
	m := &models.DebianDistribution{RepositoryID: repoID, Name: name}

	if err := p.getOrCreateDebian(ctx, debianDistributionKey, repoID, name, &m.ID, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}

	return m, nil
}

// GetOrCreateDebianComponent returns the component, creating it atomically if missing.
func (p *DebStore) GetOrCreateDebianComponent(ctx context.Context, distID, name string) (*models.DebianComponent, error) {
	m := &models.DebianComponent{DistributionID: distID, Name: name}

	if err := p.getOrCreateDebian(ctx, debianComponentKey, distID, name, &m.ID, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}

	return m, nil
}

// GetOrCreateDebianArchitecture returns the architecture, creating it atomically if missing.
func (p *DebStore) GetOrCreateDebianArchitecture(ctx context.Context, distID, name string) (*models.DebianArchitecture, error) {
	m := &models.DebianArchitecture{DistributionID: distID, Name: name}

	if err := p.getOrCreateDebian(ctx, debianArchitectureKey, distID, name, &m.ID, &m.CreatedAt, &m.UpdatedAt); err != nil {
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
