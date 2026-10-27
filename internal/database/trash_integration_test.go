// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestTrashSpecsCoverEveryDeletedAtTable keeps the kind registry in step with
// the schema: every table with a deleted_at column (but repositories) needs a
// spec, and every spec's SQL fragments must be valid against the schema.
func TestTrashSpecsCoverEveryDeletedAtTable(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	rows, err := pg.GetDB().QueryContext(ctx,
		`SELECT table_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND column_name = 'deleted_at' AND table_name <> 'repositories'`)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var withColumn []string

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}

		withColumn = append(withColumn, name)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	var inSpecs []string
	for _, s := range trashSpecs {
		inSpecs = append(inSpecs, s.table)
	}

	slices.Sort(withColumn)
	slices.Sort(inSpecs)

	if !slices.Equal(withColumn, inSpecs) {
		t.Fatalf("tables with deleted_at = %v, trashSpecs tables = %v", withColumn, inSpecs)
	}

	for _, s := range trashSpecs {
		if _, _, err := pg.ListTrash(ctx, models.TrashFilter{Kind: s.kind}); err != nil {
			t.Errorf("ListTrash(%s): %v", s.kind, err)
		}

		if _, err := pg.GetTrashItem(ctx, s.kind, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrTrashNotFound) {
			t.Errorf("GetTrashItem(%s) = %v, want ErrTrashNotFound", s.kind, err)
		}

		if _, err := pg.ListExpiredTrash(ctx, 10); err != nil {
			t.Errorf("ListExpiredTrash: %v", err)
		}
	}

	if _, _, err := pg.ListTrash(ctx, models.TrashFilter{IncludeRepositories: true}); err != nil {
		t.Fatalf("global ListTrash: %v", err)
	}
}

func mavenArtifactID(t *testing.T, s *quotaSeeder, path string) string {
	t.Helper()

	return s.queryID(`SELECT id FROM maven_artifacts WHERE repository_id = $1 AND repo_path = $2 AND deleted_at IS NULL`, s.repo, path)
}

func TestTrashRowsLifecycle(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-maven", "maven")
	s.maven(s.blob(100))

	path := "g/a1/1.0/a-1.0.jar"
	id := mavenArtifactID(t, s, path)

	soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "alice")
	if err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v; want soft delete", soft, err)
	}

	// Trashing twice finds no live row.
	if _, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "alice"); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("second TrashRows = %v, want ErrTrashNotFound", err)
	}

	items, total, err := pg.ListTrash(ctx, models.TrashFilter{RepositoryID: s.repo})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("ListTrash = %d items, total %d, err %v; want 1", len(items), total, err)
	}

	it := items[0]
	if it.Kind != models.TrashKindMaven || it.ID != id || it.Name != path || it.Version != "1.0" ||
		it.SizeBytes != 100 || it.DeletedBy != "alice" || it.RepositoryName != "trash-maven" {
		t.Fatalf("unexpected item %+v", it)
	}

	if d := it.PurgeAt.Sub(it.DeletedAt).Hours(); d < 7*24-1 || d > 7*24+1 {
		t.Fatalf("purge_at - deleted_at = %vh, want 168h", d)
	}
}

func TestTrashRestoreConflictsWithReusedPath(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-conflict", "maven")
	s.maven(s.blob(100))

	path := "g/a1/1.0/a-1.0.jar"
	id := mavenArtifactID(t, s, path)

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "alice"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	// Reusing the path while the old row sits in the trash is allowed...
	s.exec(`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id)
	        VALUES ($1, 'g', 'a1', '1.0', 'jar', $2, $3)`, s.repo, path, s.blob(5))

	// ...and makes the restore a conflict.
	if _, err := pg.RestoreTrash(ctx, models.TrashKindMaven, id); !errors.Is(err, ErrTrashConflict) {
		t.Fatalf("RestoreTrash with reused path = %v, want ErrTrashConflict", err)
	}

	s.exec(`DELETE FROM maven_artifacts WHERE repository_id = $1 AND repo_path = $2 AND deleted_at IS NULL`, s.repo, path)

	got, err := pg.RestoreTrash(ctx, models.TrashKindMaven, id)
	if err != nil || got.RepositoryID != s.repo {
		t.Fatalf("RestoreTrash = %+v, %v", got, err)
	}

	if mavenArtifactID(t, s, path) != id {
		t.Fatal("restored row is not the original")
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindMaven, id); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("restoring a live row = %v, want ErrTrashNotFound", err)
	}
}

func TestTrashPurgeReportsBlobsAndQuota(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-purge", "maven")
	blob := s.blob(250)
	s.maven(blob)

	id := mavenArtifactID(t, s, "g/a1/1.0/a-1.0.jar")

	if _, err := pg.PurgeTrash(ctx, models.TrashKindMaven, id); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("purging a live row = %v, want ErrTrashNotFound", err)
	}

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "bob"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	res, err := pg.PurgeTrash(ctx, models.TrashKindMaven, id)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.BlobSHA256) != 1 || res.SizeBytes != 250 || res.QuotaArtifacts != 1 || res.QuotaBytes != 250 {
		t.Fatalf("purge result = %+v", res)
	}

	var n int
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM maven_artifacts WHERE id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("row still present after purge: %d, %v", n, err)
	}
}

func TestTrashRetentionResolution(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-retention", "maven")

	s.maven(s.blob(1))
	id := mavenArtifactID(t, s, "g/a1/1.0/a-1.0.jar")

	repo, err := pg.GetRepositoryByNameUncached(ctx, "trash-retention")
	if err != nil || repo == nil {
		t.Fatalf("GetRepositoryByNameUncached = %v, %v", repo, err)
	}

	// Retention 0 on the repository: no soft delete.
	repo.TrashRetentionDays = new(0)
	if err := pg.UpdateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "x"); err != nil || soft {
		t.Fatalf("retention 0: TrashRows = %v, %v; want hard-delete signal", soft, err)
	}

	// Server default 0 with a NULL repository value: no soft delete either.
	repo.TrashRetentionDays = nil
	if err := pg.UpdateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	pg.SetTrashRetentionDays(0)

	if soft, _ := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "x"); soft {
		t.Fatal("default 0 must not soft delete")
	}

	// A repository value overrides the default; a 1 day window expires.
	repo.TrashRetentionDays = new(1)
	if err := pg.UpdateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindMaven, []string{id}, "x"); err != nil || !soft {
		t.Fatalf("retention 1: TrashRows = %v, %v", soft, err)
	}

	refs, err := pg.ListExpiredTrash(ctx, 10)
	if err != nil || len(refs) != 0 {
		t.Fatalf("fresh item expired early: %v, %v", refs, err)
	}

	s.exec(`UPDATE maven_artifacts SET deleted_at = now() - interval '25 hours' WHERE id = $1`, id)

	refs, err = pg.ListExpiredTrash(ctx, 10)
	if err != nil || len(refs) != 1 || refs[0].ID != id || refs[0].Kind != models.TrashKindMaven {
		t.Fatalf("ListExpiredTrash = %v, %v; want the maven row", refs, err)
	}
}

func TestTrashRepositoryHidesAndRestores(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-repo", "maven")
	s.maven(s.blob(10))

	soft, err := pg.TrashRepository(ctx, s.repo, "carol")
	if err != nil || !soft {
		t.Fatalf("TrashRepository = %v, %v", soft, err)
	}

	if r, err := pg.GetRepositoryByName(ctx, "trash-repo"); err != nil || r != nil {
		t.Fatalf("trashed repository still resolves: %v, %v", r, err)
	}

	if r, err := pg.GetRepositoryByID(ctx, s.repo); err != nil || r != nil {
		t.Fatalf("trashed repository still resolves by id: %v, %v", r, err)
	}

	// The name stays reserved until the purge.
	dup := &models.Repository{Name: "trash-repo", Format: "maven", RepoType: "hosted", ProjectID: new(systemOrgID)}
	if err := pg.CreateRepository(ctx, dup); !IsUniqueViolation(err) {
		t.Fatalf("re-creating a trashed name = %v, want unique violation", err)
	}

	items, _, err := pg.ListTrash(ctx, models.TrashFilter{IncludeRepositories: true})
	if err != nil || len(items) != 1 || items[0].Kind != models.TrashKindRepository || items[0].DeletedBy != "carol" {
		t.Fatalf("global ListTrash = %+v, %v", items, err)
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindRepository, s.repo); err != nil {
		t.Fatal(err)
	}

	if r, err := pg.GetRepositoryByName(ctx, "trash-repo"); err != nil || r == nil {
		t.Fatalf("restored repository missing: %v, %v", r, err)
	}

	// Purge only takes trashed repositories.
	if _, err := pg.PurgeTrash(ctx, models.TrashKindRepository, s.repo); !errors.Is(err, ErrTrashNotFound) {
		t.Fatalf("purging a live repository = %v, want ErrTrashNotFound", err)
	}

	if _, err := pg.TrashRepository(ctx, s.repo, "carol"); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.PurgeTrash(ctx, models.TrashKindRepository, s.repo); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT COUNT(*) FROM maven_artifacts WHERE repository_id = $1`, s.repo).
		Scan(&n); err != nil ||
		n != 0 {
		t.Fatalf("repository contents survived the purge: %d, %v", n, err)
	}
}

func TestPruneMavenMetadataKeepsNeighbours(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "prune-maven", "maven")

	const sha = "0000000000000000000000000000000000000000000000000000000000000000"

	paths := []string{
		"com/example/a/maven-metadata.xml",
		"com/example/a/1.0-SNAPSHOT/maven-metadata.xml",
		"com/example/a/2.0-SNAPSHOT/maven-metadata.xml",
		// A different artifact whose coordinates live under the same directory.
		"com/example/a/b/maven-metadata.xml",
		"com/example/other/maven-metadata.xml",
	}
	for _, p := range paths {
		s.exec(`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, $2, $3, 1)`, s.repo, p, sha)
	}

	keep := []string{"com/example/a/1.0-SNAPSHOT/maven-metadata.xml"}
	if _, err := pg.PruneMavenMetadata(ctx, s.repo, "com/example/a/", keep); err != nil {
		t.Fatalf("PruneMavenMetadata: %v", err)
	}

	want := map[string]bool{
		"com/example/a/maven-metadata.xml":              false,
		"com/example/a/1.0-SNAPSHOT/maven-metadata.xml": true,
		"com/example/a/2.0-SNAPSHOT/maven-metadata.xml": false,
		"com/example/a/b/maven-metadata.xml":            true,
		"com/example/other/maven-metadata.xml":          true,
	}
	for p, present := range want {
		meta, err := pg.GetRepoMetadataByPath(ctx, s.repo, p)
		if err != nil {
			t.Fatal(err)
		}

		if (meta != nil) != present {
			t.Errorf("%s present = %v, want %v", p, meta != nil, present)
		}
	}
}
