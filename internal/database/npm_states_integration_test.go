// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"maps"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// GetNPMVersionStates must agree with the rule the worker used to apply to the
// full manifests: a version is deprecated only when its manifest has a
// non-empty *string* "deprecated". Soft-deleted versions are not returned.
func TestGetNPMVersionStates(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	org := systemOrgID

	repo := &models.Repository{Name: "npm-states", Format: "npm", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	pkg, err := pg.CreateNPMPackage(ctx, &models.NPMPackage{RepositoryID: repo.ID, Name: "lib", FullName: "lib"})
	if err != nil {
		t.Fatalf("CreateNPMPackage: %v", err)
	}

	var blobID string
	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES (repeat('a', 64), 1, repeat('a', 64)) RETURNING id`,
	).Scan(&blobID); err != nil {
		t.Fatalf("insert blob: %v", err)
	}

	manifests := map[string]string{
		"1.0.0": `{"name":"lib"}`,
		"1.1.0": `{"deprecated":"use 2.x"}`,
		"1.2.0": `{"deprecated":""}`,
		"1.3.0": `{"deprecated":true}`,
		"1.4.0": `{"deprecated":null}`,
		"1.5.0": `{"deprecated":"gone"}`,
	}

	for v, manifest := range manifests {
		if _, err := pg.CreateNPMVersion(ctx, &models.NPMVersion{
			PackageID: pkg.ID, Version: v, ManifestJSON: []byte(manifest), BlobID: blobID,
			TarballFilename: "lib-" + v + ".tgz", Integrity: "sha512-x", Shasum: "x",
		}); err != nil {
			t.Fatalf("CreateNPMVersion %s: %v", v, err)
		}
	}

	if err := pg.DeleteNPMVersion(ctx, pkg.ID, "1.5.0"); err != nil {
		t.Fatalf("DeleteNPMVersion: %v", err)
	}

	states, err := pg.GetNPMVersionStates(ctx, pkg.ID)
	if err != nil {
		t.Fatalf("GetNPMVersionStates: %v", err)
	}

	got := make(map[string]bool, len(states))
	for _, s := range states {
		got[s.Version] = s.Deprecated
	}

	want := map[string]bool{"1.0.0": false, "1.1.0": true, "1.2.0": false, "1.3.0": false, "1.4.0": false}
	if !maps.Equal(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
}

// BenchmarkNPMRegenerateRead compares what the npm worker read per
// regeneration before (every manifest) and after (version + flag) on a package
// with 1000 versions of ~20 KB manifests (a popular package with a README).
func BenchmarkNPMRegenerateRead(b *testing.B) {
	pg := newQuotaTestDB(b, 4)
	ctx := context.Background()
	org := systemOrgID

	repo := &models.Repository{Name: "npm-bench", Format: "npm", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		b.Fatalf("CreateRepository: %v", err)
	}

	pkg, err := pg.CreateNPMPackage(ctx, &models.NPMPackage{RepositoryID: repo.ID, Name: "big", FullName: "big"})
	if err != nil {
		b.Fatalf("CreateNPMPackage: %v", err)
	}

	var blobID string
	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES (repeat('b', 64), 1, repeat('b', 64)) RETURNING id`,
	).Scan(&blobID); err != nil {
		b.Fatalf("insert blob: %v", err)
	}

	if _, err := pg.GetDB().ExecContext(ctx, `
		INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id)
		SELECT $1, '1.0.' || v,
		       jsonb_build_object('name', 'big', 'version', '1.0.' || v,
		           'readme', (SELECT string_agg(md5(g::text || v::text), '') FROM generate_series(1, 600) g)),
		       'sha512-x', 'x', 'big-1.0.' || v || '.tgz', $2
		FROM generate_series(1, 1000) v`, pkg.ID, blobID); err != nil {
		b.Fatalf("seed versions: %v", err)
	}

	b.Run("manifests", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := pg.GetNPMVersionsByPackage(ctx, pkg.ID); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("states", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := pg.GetNPMVersionStates(ctx, pkg.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
}
