// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestNPMRegenerateComputesLatestFromLiveVersions: the stored latest follows
// the live, non-deprecated stable versions; trashed versions never count and
// the pointer is cleared when no live version remains.
func TestNPMRegenerateComputesLatestFromLiveVersions(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	org := trashTestOrgID

	repo := &models.Repository{Name: "npm-latest", Format: "npm", RepoType: "hosted", ProjectID: &org}
	if err := s.pg.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	var pkgID string
	if err := s.pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO packages (repository_id, format, name, name_key, attrs) VALUES ($1, 'npm', 'lib', 'lib', '{"scope": "", "name": "lib"}') RETURNING id`, repo.ID).
		Scan(&pkgID); err != nil {
		t.Fatal(err)
	}

	add := func(version, manifest string) string {
		s.seq++

		blobID, _ := s.blob(fmt.Sprintf("npm %d", s.seq), time.Hour)

		var id string
		if err := s.pg.GetDB().QueryRowContext(ctx,
			`WITH v AS (INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata)
			            VALUES ($1, $2, $3::text, $3::text, jsonb_build_object('manifest_json', $4::jsonb)) RETURNING id)
			 , f AS (INSERT INTO package_files (version_id, repository_id, path, filename, blob_id)
			         SELECT v.id, $2, 'lib/-/lib-' || $3::text || '.tgz', 'lib-' || $3::text || '.tgz', $5 FROM v)
			 SELECT id FROM v`, pkgID, repo.ID, version, manifest, blobID).Scan(&id); err != nil {
			t.Fatal(err)
		}

		return id
	}

	stable := add("1.0.0", `{}`)
	deprecated := add("2.0.0", `{"deprecated":"use 3.x"}`)
	pre := add("3.0.0-beta.1", `{}`)
	trashed := add("2.1.0", `{}`)

	s.exec(`UPDATE package_versions SET deleted_at = now() WHERE id = $1`, trashed)

	w := NewNPMMetadataWorker(s.pg, testLogger(t), s.pg.GetDB(), nil)
	defer w.Stop()

	latest := func() string {
		t.Helper()

		if err := w.regenerate(ctx, pkgID); err != nil {
			t.Fatalf("regenerate: %v", err)
		}

		var v string
		if err := s.pg.GetDB().
			QueryRowContext(ctx, `SELECT COALESCE(attrs->>'latest_version', '') FROM packages WHERE id = $1`, pkgID).
			Scan(&v); err != nil {
			t.Fatal(err)
		}

		return v
	}

	steps := []struct {
		name   string
		mutate func()
		want   string
	}{
		{"a stable release beats a newer prerelease and a deprecated one", func() {}, "1.0.0"},
		{
			"a prerelease beats a deprecated stable release",
			func() { s.exec(`UPDATE package_versions SET deleted_at = now() WHERE id = $1`, stable) },
			"3.0.0-beta.1",
		},
		{"deprecated is the last resort", func() { s.exec(`UPDATE package_versions SET deleted_at = now() WHERE id = $1`, pre) }, "2.0.0"},
		{"an empty deprecation message is not a deprecation", func() {
			s.exec(
				`UPDATE package_versions SET metadata = jsonb_build_object('manifest_json', '{"deprecated":""}'::jsonb) WHERE id = $1`,
				deprecated,
			)
			s.exec(`UPDATE package_versions SET deleted_at = NULL WHERE id = $1`, stable)
		}, "2.0.0"},
		{
			"a restored version counts again",
			func() { s.exec(`UPDATE package_versions SET deleted_at = NULL WHERE id = $1`, trashed) },
			"2.1.0",
		},
		{
			"no live version clears latest",
			func() { s.exec(`UPDATE package_versions SET deleted_at = now() WHERE package_id = $1`, pkgID) },
			"",
		},
	}

	for _, st := range steps {
		st.mutate()

		if got := latest(); got != st.want {
			t.Fatalf("%s: latest = %q, want %q", st.name, got, st.want)
		}
	}
}
