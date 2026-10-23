// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// seedManifest inserts a live manifest of image with the given blob refs and,
// when indexed, the marker the registry GC pass writes once it has recorded a
// manifest's references. It returns the manifest id.
func seedManifest(s *quotaSeeder, image, digest string, indexed bool, blobs ...string) string {
	s.tb.Helper()

	id := s.queryID(`INSERT INTO docker_manifests (repository_id, image, digest, media_type, blob_id)
	        VALUES ($1, $2, $3, 'application/vnd.oci.image.manifest.v1+json', $4) RETURNING id`,
		s.repo, image, digest, s.blob(10))

	for _, b := range blobs {
		s.exec(`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest) VALUES ($1, $2, $3, 'blob', $4)`,
			id, s.repo, image, b)
	}

	if indexed {
		s.exec(`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest) VALUES ($1, $2, $3, 'indexed', '')`,
			id, s.repo, image)
	}

	return id
}

func TestDockerBlobInTrash(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "blob-trash", "docker")

	const (
		own, shared, onlyTrashed, orphan = "sha256:own", "sha256:shared", "sha256:onlytrashed", "sha256:orphan"
	)

	live := seedManifest(s, "app", "sha256:m-live", true, own, shared)
	gone := seedManifest(s, "other", "sha256:m-gone", true, shared, onlyTrashed)

	check := func(step string, want map[string]bool) {
		t.Helper()

		for digest, exp := range want {
			got, err := pg.DockerBlobInTrash(ctx, s.repo, digest)
			if err != nil {
				t.Fatal(err)
			}

			if got != exp {
				t.Errorf("%s: DockerBlobInTrash(%s) = %v, want %v", step, digest, got, exp)
			}
		}
	}

	check("nothing trashed", map[string]bool{own: false, shared: false, onlyTrashed: false, orphan: false})

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindDockerManifest, []string{gone}, "bob"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	check("one manifest trashed", map[string]bool{own: false, shared: false, onlyTrashed: true, orphan: false})

	// A blob no manifest references yet (an upload awaiting its manifest) is never hidden.
	// Another repository's trash does not hide this repository's blobs.
	other := newQuotaSeeder(t, pg, "blob-trash-other", "docker")

	otherID := seedManifest(other, "app", "sha256:m-other", true, onlyTrashed)
	if _, err := pg.TrashRows(ctx, other.repo, models.TrashKindDockerManifest, []string{otherID}, "bob"); err != nil {
		t.Fatal(err)
	}

	if got, err := pg.DockerBlobInTrash(ctx, s.repo, orphan); err != nil || got {
		t.Fatalf("unreferenced blob in trash = %v, %v, want false", got, err)
	}

	// A live manifest whose references are not recorded yet may reference
	// anything: nothing is hidden until the GC pass indexes it.
	unindexed := seedManifest(s, "legacy", "sha256:m-legacy", false)
	check("unindexed live manifest", map[string]bool{onlyTrashed: false})

	s.exec(`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest) VALUES ($1, $2, 'legacy', 'indexed', '')`, unindexed, s.repo)
	check("indexed live manifest", map[string]bool{onlyTrashed: true})

	if _, err := pg.RestoreTrash(ctx, models.TrashKindDockerManifest, gone); err != nil {
		t.Fatal(err)
	}

	check("restored", map[string]bool{own: false, shared: false, onlyTrashed: false})

	// Trashing the other manifest too hides what only they held.
	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindDockerManifest, []string{live, gone}, "bob"); err != nil {
		t.Fatal(err)
	}

	check("everything trashed except the legacy manifest", map[string]bool{own: true, shared: true, onlyTrashed: true})
}

func TestListDockerCatalog(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	org := systemOrgID

	newRepo := func(name, repoType string) *models.Repository {
		r := &models.Repository{Name: name, Format: "docker", RepoType: repoType, ProjectID: &org}
		if repoType == "proxy" {
			r.UpstreamURL = "https://registry.example.com"
		}

		if err := pg.CreateRepository(ctx, r); err != nil {
			t.Fatalf("CreateRepository(%s): %v", name, err)
		}

		return r
	}

	hosted := &quotaSeeder{tb: t, pg: pg, repo: newRepo("alpha", "hosted").ID}
	seedManifest(hosted, "", "sha256:d0", true)
	seedManifest(hosted, "team/api", "sha256:a1", true)
	seedManifest(hosted, "team/api", "sha256:a2", true) // one name per image, however many manifests
	web := seedManifest(hosted, "web", "sha256:w1", true)

	newRepo("empty", "hosted") // no manifests: nothing to list

	second := &quotaSeeder{tb: t, pg: pg, repo: newRepo("beta", "hosted").ID}
	seedManifest(second, "lib/db", "sha256:b1", true)

	proxy := newRepo("hub", "proxy")
	seedProxyCache(t, pg, proxy.ID)

	// A group lists its members' images (not their default image) under its own name.
	group := newRepo("all", "group")
	pgExec(t, pg, `INSERT INTO repository_group_members (group_repo_id, member_repo_id) SELECT $1, id FROM repositories WHERE name IN ('alpha', 'hub')`, group.ID)

	// Blocked repositories are not listed.
	blocked := &quotaSeeder{tb: t, pg: pg, repo: newRepo("blocked", "hosted").ID}
	seedManifest(blocked, "x", "sha256:x1", true)
	pgExec(t, pg, `UPDATE repositories SET blocked = true WHERE id = $1`, blocked.repo)

	names := func(after string, limit int) []string { return catalogNames(t, pg, after, limit) }

	want := []string{
		"all/library/alpine", "all/library/nginx", "all/team/api", "all/web",
		"alpha", "alpha/team/api", "alpha/web",
		"beta/lib/db",
		"hub/library/alpine", "hub/library/nginx",
	}
	if got := names("", 100); !slices.Equal(got, want) {
		t.Fatalf("catalog = %v\nwant      %v", got, want)
	}

	// Pages resume strictly after the given name, whatever its position.
	var paged []string

	for after := ""; ; {
		page := names(after, 3)
		paged = append(paged, page...)

		if len(page) < 3 {
			break
		}

		after = page[len(page)-1]
	}

	if !slices.Equal(paged, want) {
		t.Fatalf("paged catalog = %v, want %v", paged, want)
	}

	// Sources: a group name lists the members that hold the image.
	entries, err := pg.ListDockerCatalog(ctx, "all/team", 1)
	if err != nil || len(entries) != 1 || entries[0].Name != "all/team/api" {
		t.Fatalf("group entry = %+v, %v", entries, err)
	}

	e := entries[0]
	if e.Repository.Name != "all" || e.Image != "team/api" || len(e.Sources) != 1 || e.Sources[0].Name != "alpha" || e.Sources[0].ProjectID == nil {
		t.Fatalf("group entry fields = repo %q image %q sources %+v", e.Repository.Name, e.Image, e.Sources)
	}

	// A trashed image drops out; so does the whole name when its last live manifest does.
	if _, err := pg.TrashRows(ctx, hosted.repo, models.TrashKindDockerManifest, []string{web}, "bob"); err != nil {
		t.Fatal(err)
	}

	if got := names("alpha/team/api", 2); !slices.Equal(got, []string{"beta/lib/db", "hub/library/alpine"}) {
		t.Fatalf("catalog after trashing alpha/web = %v, want it skipped", got)
	}

	if got := names("all/team/api", 1); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("catalog after trashing alpha/web = %v, want the group's all/web skipped too", got)
	}

	// A deleted repository is gone.
	pgExec(t, pg, `UPDATE repositories SET deleted_at = now() WHERE name = 'beta'`)

	if got := names("alpha/team/api", 5); !slices.Equal(got, []string{"hub/library/alpine", "hub/library/nginx"}) {
		t.Fatalf("catalog after deleting beta = %v", got)
	}
}

// seedProxyCache caches what a proxy lists the images of: manifests with a
// body, but not negative entries or blobs.
func seedProxyCache(t *testing.T, pg *PostgresDB, repoID string) {
	t.Helper()

	for key, status := range map[string]int{
		"library/alpine/manifests/3.20":   200,
		"library/alpine/blobs/sha256:aaa": 200,
		"library/ghost/manifests/latest":  404,
		"library/nginx/manifests/latest":  200,
	} {
		var blob any = "k-" + key
		if status != 200 {
			blob = nil
		}

		pgExec(t, pg, `INSERT INTO proxy_cache_entries (repository_id, cache_key, blob_key, status_code, expires_at)
		        VALUES ($1, $2, $3, $4, now() + interval '1 hour')`, repoID, key, blob, status)
	}
}

// catalogNames lists the catalog names after the given one.
func catalogNames(t *testing.T, pg *PostgresDB, after string, limit int) []string {
	t.Helper()

	entries, err := pg.ListDockerCatalog(context.Background(), after, limit)
	if err != nil {
		t.Fatal(err)
	}

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}

	return out
}

func pgExec(t *testing.T, pg *PostgresDB, query string, args ...any) {
	t.Helper()

	if _, err := pg.GetDB().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// Migration 063 gives the open-ended docker digest entries a finite expiry and
// leaves everything else alone.
func TestMigrationProxyDockerDigestExpiry(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	org := systemOrgID

	mk := func(name, format string) string {
		r := &models.Repository{Name: name, Format: format, RepoType: "proxy", UpstreamURL: "https://up.example.com", ProjectID: &org}
		if err := pg.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}

		return r.ID
	}

	dockerRepo, npmRepo := mk("hub", "docker"), mk("npmjs", "npm")
	digest := "sha256:a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

	rows := []struct{ repo, key, blob string }{
		{dockerRepo, "library/alpine/manifests/" + digest, "b1"}, // migrated
		{dockerRepo, "library/alpine/blobs/" + digest, "b2"},     // migrated
		{dockerRepo, "library/alpine/manifests/3.20", "b3"},      // a tag: untouched
		{dockerRepo, "library/alpine/blobs/" + digest + "x", "b4"},
		{npmRepo, "pkg/-/pkg-1.0.0.tgz/manifests/" + digest, "b5"}, // other format: untouched
	}
	for _, r := range rows {
		pgExec(t, pg, `INSERT INTO proxy_cache_entries (repository_id, cache_key, blob_key, expires_at)
		        VALUES ($1, $2, $3, now() + interval '100 years')`, r.repo, r.key, r.blob)
	}

	// A digest entry that already has a short life keeps it.
	pgExec(t, pg, `INSERT INTO proxy_cache_entries (repository_id, cache_key, blob_key, expires_at)
	        VALUES ($1, $2, 'b6', now() + interval '1 day')`, dockerRepo, "library/busybox/manifests/"+digest)

	script, err := os.ReadFile("../../migrations/063_proxy_docker_digest_expiry.sql")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pg.GetDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tx.ExecContext(ctx, string(script)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("migration 063: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		blob       string
		long       bool
		minD, maxD string
	}{
		{"b1", false, "29 days", "31 days"},
		{"b2", false, "29 days", "31 days"},
		{"b3", true, "", ""},
		{"b4", true, "", ""},
		{"b5", true, "", ""},
		{"b6", false, "0 days", "2 days"},
	} {
		var ok bool

		err := pg.GetDB().QueryRowContext(ctx, `SELECT CASE WHEN $2::bool THEN expires_at > now() + interval '50 years'
		        ELSE expires_at BETWEEN now() + $3::interval AND now() + $4::interval END
		        FROM proxy_cache_entries WHERE blob_key = $1`, tc.blob, tc.long, orDefault(tc.minD), orDefault(tc.maxD)).Scan(&ok)
		if err != nil || !ok {
			t.Errorf("entry %s: expiry as expected = %v (%v)", tc.blob, ok, err)
		}
	}
}

func orDefault(s string) string {
	if s == "" {
		return "0 days"
	}

	return s
}
