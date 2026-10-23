// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// conanDB is an in-memory conan_packages table with the semantics of the real
// one: one live row per ConanFileKey, an upsert replacing the blob, and a clock
// that advances one second per write so "latest" is deterministic.
type conanDB struct {
	*formattest.DB
	quotaFake

	mu    sync.Mutex
	rows  []*models.ConanPackage
	next  int
	clock time.Time
}

func newConanDB() *conanDB {
	return &conanDB{DB: formattest.NewDB(), clock: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func keyOf(p *models.ConanPackage) models.ConanFileKey {
	return models.ConanFileKey{
		Name: p.Name, Version: p.Version, UserChannel: p.UserChannel, RecipeRevision: p.RecipeRevision,
		PackageID: p.PackageID, PackageRevision: p.PackageRevision, Filename: p.Filename,
	}
}

func (d *conanDB) CreateConanPackage(_ context.Context, p *models.ConanPackage) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.clock = d.clock.Add(time.Second)

	for _, row := range d.rows {
		if row.RepositoryID == p.RepositoryID && keyOf(row) == keyOf(p) {
			row.BlobKey, row.SHA256, row.SizeBytes, row.MD5, row.SHA1, row.UpdatedAt = p.BlobKey, p.SHA256, p.SizeBytes, p.MD5, p.SHA1, d.clock
			p.ID, p.CreatedAt, p.UpdatedAt = row.ID, row.CreatedAt, row.UpdatedAt

			return nil
		}
	}

	d.next++
	p.ID = fmt.Sprintf("row-%d", d.next)
	// Like the real INSERT: an origin created_at wins over now.
	if p.CreatedAt.IsZero() {
		p.CreatedAt = d.clock
	}

	p.UpdatedAt = d.clock
	cp := *p
	d.rows = append(d.rows, &cp)

	return nil
}

func (d *conanDB) GetConanPackage(_ context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.rows {
		if row.RepositoryID == repoID && keyOf(row) == key {
			cp := *row
			return &cp, nil
		}
	}

	return nil, nil // not found is (nil, nil) in the Database contract
}

func (d *conanDB) ListConanRecipeFiles(_ context.Context, repoID, name, version, userChannel string) ([]*models.ConanPackage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.ConanPackage

	for _, row := range d.rows {
		if row.RepositoryID == repoID && row.Name == name && row.Version == version && row.UserChannel == userChannel {
			cp := *row
			out = append(out, &cp)
		}
	}

	return out, nil
}

func (d *conanDB) ListConanRecipes(_ context.Context, repoID string) ([]models.ConanRecipe, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	seen := map[models.ConanRecipe]bool{}

	for _, row := range d.rows {
		if row.RepositoryID == repoID && row.Filename != "" {
			seen[models.ConanRecipe{Name: row.Name, Version: row.Version, UserChannel: row.UserChannel}] = true
		}
	}

	out := make([]models.ConanRecipe, 0, len(seen))
	for rec := range seen {
		out = append(out, rec)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name+"/"+out[i].Version+"/"+out[i].UserChannel < out[j].Name+"/"+out[j].Version+"/"+out[j].UserChannel
	})

	return out, nil
}

// FindConanPackageBySHA1 is the first (oldest) live row of the repository with
// the SHA-1.
func (d *conanDB) FindConanPackageBySHA1(_ context.Context, repoID, sha1 string) (*models.ConanPackage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.rows {
		if row.RepositoryID == repoID && row.SHA1 == sha1 && sha1 != "" {
			cp := *row
			return &cp, nil
		}
	}

	return nil, nil // not found is (nil, nil) in the Database contract
}

// SetConanPackageDigests records digests without moving the row's time, like
// the real UPDATE.
func (d *conanDB) SetConanPackageDigests(_ context.Context, id, md5, sha1 string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.rows {
		if row.ID == id {
			row.MD5, row.SHA1 = md5, sha1
		}
	}

	return nil
}

func (d *conanDB) DeleteConanPackage(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, row := range d.rows {
		if row.ID == id {
			d.rows = append(d.rows[:i], d.rows[i+1:]...)
			return nil
		}
	}

	return nil
}

// count is the number of stored rows.
func (d *conanDB) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return len(d.rows)
}
