// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func npmCount(t *testing.T, pg *PostgresDB, query string, args ...any) int {
	t.Helper()

	var n int
	if err := pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}

	return n
}

func npmUpdatedAt(t *testing.T, pg *PostgresDB, pkgID string) time.Time {
	t.Helper()

	var at time.Time
	if err := pg.GetDB().QueryRowContext(context.Background(),
		`SELECT updated_at FROM packages WHERE id = $1`, pkgID).Scan(&at); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}

	return at
}

// TestNPMStoreRoundTrip writes through the typed store, reads back through every
// typed read and checks the generic rows the mapping promises.
func TestNPMStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	org := systemOrgID

	repo := &models.Repository{Name: "npm-rt", Format: "npm", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	pkg, err := pg.CreateNPMPackage(ctx, &models.NPMPackage{
		RepositoryID: repo.ID, Scope: "@acme", Name: "lib", FullName: "@acme/lib", LatestVersion: "1.0.0", PackumentStale: true,
	})
	if err != nil {
		t.Fatalf("CreateNPMPackage: %v", err)
	}

	// A package without a live version does not exist for readers.
	if got, err := pg.GetNPMPackageByName(ctx, repo.ID, "@acme/lib"); err != nil || got != nil {
		t.Fatalf("package without versions = %v, %v; want nil, nil", got, err)
	}

	blobID, err := pg.UpsertBlob(ctx, "00000000000000000000000000000000000000000000000000000000000000aa", "k", 10)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	etag := npmUpdatedAt(t, pg, pkg.ID)

	// The handler reserves the tarball first; the file row consumes the
	// reservation (quota_pending) and its removal records a release.
	if ok, err := pg.ReserveStorageUsage(ctx, repo.ID, 10, 1); err != nil || !ok {
		t.Fatalf("ReserveStorageUsage = %v, %v", ok, err)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM quota_pending WHERE repository_id = $1`, repo.ID); n != 1 {
		t.Fatalf("pending before the write = %d, want 1", n)
	}

	v, err := pg.CreateNPMVersion(ctx, &models.NPMVersion{
		PackageID: pkg.ID, Version: "1.0.0", ManifestJSON: []byte(`{"name":"@acme/lib"}`),
		Integrity: "sha512-x", Shasum: "abc", BlobID: blobID, TarballFilename: "lib-1.0.0.tgz",
	})
	if err != nil {
		t.Fatalf("CreateNPMVersion: %v", err)
	}

	if !npmUpdatedAt(t, pg, pkg.ID).After(etag) {
		t.Error("version insert did not move the packument ETag")
	}

	// Generic rows.
	if n := npmCount(t, pg, `SELECT count(*) FROM package_versions WHERE package_id = $1 AND version_key = '1.0.0'`, pkg.ID); n != 1 {
		t.Errorf("package_versions = %d, want 1", n)
	}

	var path, filename, role, unit, shasum, integrity string

	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT path, filename, role, unit_key, checksums->>'shasum', checksums->>'integrity' FROM package_files WHERE id = $1`, v.ID,
	).Scan(&path, &filename, &role, &unit, &shasum, &integrity); err != nil {
		t.Fatalf("read file row: %v", err)
	}

	if path != "@acme/lib/-/lib-1.0.0.tgz" || filename != "lib-1.0.0.tgz" || role != FileRolePrimary ||
		unit != "@acme/lib@1.0.0" || shasum != "abc" || integrity != "sha512-x" {
		t.Errorf("file row = %q %q %q %q %q %q", path, filename, role, unit, shasum, integrity)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM quota_pending WHERE repository_id = $1`, repo.ID); n != 0 {
		t.Errorf("pending after the write = %d, want 0 (the primary file consumes the reservation)", n)
	}

	// Typed reads.
	got, err := pg.GetNPMPackageByName(ctx, repo.ID, "@acme/lib")
	if err != nil || got == nil {
		t.Fatalf("GetNPMPackageByName = %v, %v", got, err)
	}

	if got.ID != pkg.ID || got.Scope != "@acme" || got.Name != "lib" || got.FullName != "@acme/lib" ||
		got.LatestVersion != "1.0.0" || !got.PackumentStale {
		t.Errorf("package = %+v", got)
	}

	gv, err := pg.GetNPMVersion(ctx, pkg.ID, "1.0.0")
	if err != nil || gv == nil {
		t.Fatalf("GetNPMVersion = %v, %v", gv, err)
	}

	if gv.ID != v.ID || gv.PackageID != pkg.ID || gv.Integrity != "sha512-x" || gv.Shasum != "abc" ||
		gv.BlobID != blobID || gv.TarballFilename != "lib-1.0.0.tgz" || gv.BlobSizeBytes != 10 ||
		gv.BlobSHA256 != "00000000000000000000000000000000000000000000000000000000000000aa" {
		t.Errorf("version = %+v", gv)
	}

	if string(gv.ManifestJSON) != `{"name": "@acme/lib"}` {
		t.Errorf("manifest = %s", gv.ManifestJSON)
	}

	all, err := pg.GetNPMVersionsByPackage(ctx, pkg.ID)
	if err != nil || len(all) != 1 || all[0].ID != v.ID || all[0].BlobSHA256 != gv.BlobSHA256 {
		t.Fatalf("GetNPMVersionsByPackage = %v, %v", all, err)
	}

	// A duplicate version is ErrDuplicate.
	if _, err := pg.CreateNPMVersion(ctx, &models.NPMVersion{
		PackageID: pkg.ID, Version: "1.0.0", ManifestJSON: []byte(`{}`), BlobID: blobID, TarballFilename: "other.tgz",
	}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate version err = %v, want ErrDuplicate", err)
	}

	// Dist-tags.
	etag = npmUpdatedAt(t, pg, pkg.ID)

	tag, err := pg.UpsertNPMDistTag(ctx, &models.NPMDistTag{PackageID: pkg.ID, Tag: "latest", Version: "1.0.0"})
	if err != nil || tag.ID == "" {
		t.Fatalf("UpsertNPMDistTag = %v, %v", tag, err)
	}

	if !npmUpdatedAt(t, pg, pkg.ID).After(etag) {
		t.Error("tag upsert did not move the packument ETag")
	}

	if _, err := pg.UpsertNPMDistTag(ctx, &models.NPMDistTag{PackageID: pkg.ID, Tag: "latest", Version: "1.0.0"}); err != nil {
		t.Fatalf("second UpsertNPMDistTag: %v", err)
	}

	if _, err := pg.UpsertNPMDistTag(ctx, &models.NPMDistTag{PackageID: pkg.ID, Tag: "beta", Version: "9.9.9"}); err != nil {
		t.Fatalf("UpsertNPMDistTag beta: %v", err)
	}

	tags, err := pg.GetNPMDistTags(ctx, pkg.ID)
	if err != nil || len(tags) != 1 || tags["latest"] != "1.0.0" {
		t.Fatalf("GetNPMDistTags = %v, %v (a tag of an unknown version is hidden)", tags, err)
	}

	var tagUnit string
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT unit_key FROM package_tags WHERE package_id = $1 AND tag = 'latest'`, pkg.ID,
	).Scan(&tagUnit); err != nil || tagUnit != "@acme/lib#latest" {
		t.Errorf("tag unit_key = %q, %v", tagUnit, err)
	}

	// Deprecate moves the ETag and the version states.
	etag = npmUpdatedAt(t, pg, pkg.ID)

	if err := pg.UpdateNPMVersionManifest(ctx, pkg.ID, "1.0.0", []byte(`{"deprecated":"old"}`)); err != nil {
		t.Fatalf("UpdateNPMVersionManifest: %v", err)
	}

	if !npmUpdatedAt(t, pg, pkg.ID).After(etag) {
		t.Error("deprecate did not move the packument ETag")
	}

	states, err := pg.GetNPMVersionStates(ctx, pkg.ID)
	if err != nil || len(states) != 1 || !states[0].Deprecated {
		t.Fatalf("GetNPMVersionStates = %v, %v", states, err)
	}

	// Worker state.
	if err := pg.UpdateNPMPackageLatestVersion(ctx, pkg.ID, "1.0.0"); err != nil {
		t.Fatalf("UpdateNPMPackageLatestVersion: %v", err)
	}

	if got, _ = pg.GetNPMPackageByName(ctx, repo.ID, "@acme/lib"); got.PackumentStale {
		t.Error("UpdateNPMPackageLatestVersion left the packument stale")
	}

	if err := pg.MarkPackumentStale(ctx, pkg.ID); err != nil {
		t.Fatalf("MarkPackumentStale: %v", err)
	}

	if got, _ = pg.GetNPMPackageByName(ctx, repo.ID, "@acme/lib"); !got.PackumentStale || got.LatestVersion != "1.0.0" {
		t.Errorf("after MarkPackumentStale: %+v", got)
	}

	// Tag delete, then hard delete of the version removes its tags and file.
	if err := pg.DeleteNPMDistTag(ctx, pkg.ID, "beta"); err != nil {
		t.Fatalf("DeleteNPMDistTag: %v", err)
	}

	// The package is not removed while a version row exists.
	if err := pg.DeleteNPMPackage(ctx, pkg.ID); err != nil {
		t.Fatalf("DeleteNPMPackage: %v", err)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM packages WHERE id = $1`, pkg.ID); n != 1 {
		t.Fatalf("package with versions was deleted")
	}

	etag = npmUpdatedAt(t, pg, pkg.ID)

	if err := pg.DeleteNPMVersion(ctx, pkg.ID, "1.0.0"); err != nil {
		t.Fatalf("DeleteNPMVersion: %v", err)
	}

	if !npmUpdatedAt(t, pg, pkg.ID).After(etag) {
		t.Error("version delete did not move the packument ETag")
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM package_versions WHERE package_id = $1`, pkg.ID); n != 0 {
		t.Errorf("package_versions left = %d", n)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM package_files WHERE id = $1`, v.ID); n != 0 {
		t.Errorf("package_files left = %d", n)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM package_tags WHERE package_id = $1`, pkg.ID); n != 0 {
		t.Errorf("package_tags left = %d, want 0", n)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM quota_pending WHERE repository_id = $1 AND size_bytes = 10 AND artifact_count = 1`, repo.ID); n != 1 {
		t.Errorf("release entries after delete = %d, want 1", n)
	}

	if err := pg.DeleteNPMPackage(ctx, pkg.ID); err != nil {
		t.Fatalf("DeleteNPMPackage (empty): %v", err)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM packages WHERE id = $1`, pkg.ID); n != 0 {
		t.Error("empty package was not deleted")
	}
}

// A trashed version hides its tag; a republish of the same number is a new
// version row; the tag follows it.
func TestNPMTrashedVersionHidesTag(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	org := systemOrgID

	repo := &models.Repository{Name: "npm-trash", Format: "npm", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	pkg, err := pg.CreateNPMPackage(ctx, &models.NPMPackage{RepositoryID: repo.ID, Name: "lib", FullName: "lib"})
	if err != nil {
		t.Fatalf("CreateNPMPackage: %v", err)
	}

	blobID, err := pg.UpsertBlob(ctx, "00000000000000000000000000000000000000000000000000000000000000bb", "k", 1)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	mk := func(file string) *models.NPMVersion {
		v, err := pg.CreateNPMVersion(ctx, &models.NPMVersion{
			PackageID: pkg.ID, Version: "1.0.0", ManifestJSON: []byte(`{}`), BlobID: blobID, TarballFilename: file,
		})
		if err != nil {
			t.Fatalf("CreateNPMVersion: %v", err)
		}

		return v
	}

	v := mk("lib-1.0.0.tgz")

	if _, err := pg.UpsertNPMDistTag(ctx, &models.NPMDistTag{PackageID: pkg.ID, Tag: "latest", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}

	// Trash the file and its version the way the trash engine stamps them.
	exec := func(q string, args ...any) {
		if _, err := pg.GetDB().ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	exec(`UPDATE package_files SET deleted_at = now(), deleted_by = 'x' WHERE id = $1`, v.ID)
	exec(`UPDATE package_versions SET deleted_at = now(), deleted_by = 'x' WHERE package_id = $1`, pkg.ID)

	if tags, _ := pg.GetNPMDistTags(ctx, pkg.ID); len(tags) != 0 {
		t.Errorf("tags of a trashed version = %v, want none", tags)
	}

	if got, _ := pg.GetNPMPackageByName(ctx, repo.ID, "lib"); got != nil {
		t.Error("package with only trashed versions is visible")
	}

	// A republish does not collide with the trashed incarnation and the tag
	// (kept by the store) names it again.
	mk("lib-1.0.0-r.tgz")

	if tags, _ := pg.GetNPMDistTags(ctx, pkg.ID); tags["latest"] != "1.0.0" {
		t.Errorf("tags after republish = %v", tags)
	}

	// Hard-deleting the live incarnation keeps the tag for the trashed one.
	if err := pg.DeleteNPMVersion(ctx, pkg.ID, "1.0.0"); err != nil {
		t.Fatalf("DeleteNPMVersion: %v", err)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM package_tags WHERE package_id = $1`, pkg.ID); n != 1 {
		t.Errorf("tags kept for the trashed incarnation = %d, want 1", n)
	}

	// The package is not deleted while a trashed version row remains.
	if err := pg.DeleteNPMPackage(ctx, pkg.ID); err != nil {
		t.Fatal(err)
	}

	if n := npmCount(t, pg, `SELECT count(*) FROM packages WHERE id = $1`, pkg.ID); n != 1 {
		t.Error("package with a trashed version was deleted")
	}
}
