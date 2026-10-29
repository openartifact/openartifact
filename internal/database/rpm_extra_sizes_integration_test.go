// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// TestUpdateRpmPackageExtraCorrectsSizes: rows stored before the header size
// tags were read hold 0 in installed_size/archive_size. Persisting extra data
// that carries sizes (what the backfill writes) fixes the columns, and extra
// data without sizes (the pre-sizes layout) leaves them alone.
func TestUpdateRpmPackageExtraCorrectsSizes(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	repoID := newOSRepo(t, pg, "rpm-sizes", "rpm")

	const sha = "00000000000000000000000000000000000000000000000000000000000000aa"

	blobID, err := pg.UpsertBlob(ctx, sha, sha, 1)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	if err := pg.CreateRpmPackage(ctx, &models.Component{
		RepositoryID: repoID, ArtifactID: blobID, Name: "oa", Version: "1", Release: "1", Arch: "x86_64",
		Filename: "oa-1-1.x86_64.rpm",
	}); err != nil {
		t.Fatalf("CreateRpmPackage: %v", err)
	}

	sizes := func() (int64, int64) {
		var installed, archive int64
		if err := pg.GetDB().QueryRowContext(ctx,
			`SELECT COALESCE((attrs ->> 'installed_size')::bigint, 0), COALESCE((attrs ->> 'archive_size')::bigint, 0) FROM package_files WHERE repository_id = $1`, repoID).
			Scan(&installed, &archive); err != nil {
			t.Fatalf("read sizes: %v", err)
		}

		return installed, archive
	}

	legacy := []byte(`{"weak_deps":true}`)
	if err := pg.UpdateRpmPackageExtra(ctx, repoID, sha, legacy); err != nil {
		t.Fatalf("UpdateRpmPackageExtra(legacy): %v", err)
	}

	if installed, archive := sizes(); installed != 0 || archive != 0 {
		t.Fatalf("sizes after sizeless extra = %d/%d, want the columns untouched (0/0)", installed, archive)
	}

	extra, err := (&pkgrpm.Package{InstalledSize: 100012, ArchiveSize: 100696}).MarshalExtra()
	if err != nil {
		t.Fatal(err)
	}

	if err := pg.UpdateRpmPackageExtra(ctx, repoID, sha, extra); err != nil {
		t.Fatalf("UpdateRpmPackageExtra: %v", err)
	}

	if installed, archive := sizes(); installed != 100012 || archive != 100696 {
		t.Fatalf("sizes after backfill = %d/%d, want 100012/100696", installed, archive)
	}

	// The read path returns the same values and marks the row as backfilled.
	pkgs, err := pg.ListRpmPackagesForMetadata(ctx, repoID)
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("ListRpmPackagesForMetadata = %d packages, err %v", len(pkgs), err)
	}

	if !pkgs[0].SizesParsed || pkgs[0].InstalledSize != 100012 || pkgs[0].ArchiveSize != 100696 {
		t.Errorf("read back installed=%d archive=%d parsed=%v", pkgs[0].InstalledSize, pkgs[0].ArchiveSize, pkgs[0].SizesParsed)
	}
}
