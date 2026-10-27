// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func pacmanFixture(repoID, name, version, arch string, blob int) *models.PacmanPackage {
	sha := fmt.Sprintf("%064x", blob)

	return &models.PacmanPackage{
		RepositoryID: repoID, Name: name, Base: name, Version: version, Arch: arch, RepoArch: "x86_64",
		Filename: name + "-" + version + "-" + arch + ".pkg.tar.zst", MD5: fmt.Sprintf("%032x", blob),
		Description: "d", Depends: []string{"glibc", "zlib"}, Licenses: []string{"MIT"}, BuildDate: 1700000000, InstalledSize: 4096,
		SHA256: sha, BlobKey: sha, SizeBytes: 100,
	}
}

// A package round-trips, is unique per live file name and per live
// name/version/arch, and may be published again once deleted.
//
//nolint:gocyclo,cyclop // one scenario: create, uniqueness, list, delete, recreate
func TestPacmanCreateListDeleteUnique(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "arch", "pacman")

	pkg := pacmanFixture(repoID, "tool", "1:2.0-1", "x86_64", 1)
	if err := pg.CreatePacmanPackage(ctx, pkg); err != nil || pkg.ID == "" {
		t.Fatalf("create = %v (id %q)", err, pkg.ID)
	}

	// Same file name, other blob.
	if err := pg.CreatePacmanPackage(ctx, pacmanFixture(repoID, "tool", "1:2.0-1", "x86_64", 2)); !database.IsUniqueViolation(err) {
		t.Fatalf("duplicate = %v, want a unique violation", err)
	}

	// Same coordinates, other file name.
	other := pacmanFixture(repoID, "tool", "1:2.0-1", "x86_64", 3)
	other.Filename = "tool-1:2.0-1-x86_64.pkg.tar.xz"

	if err := pg.CreatePacmanPackage(ctx, other); !database.IsUniqueViolation(err) {
		t.Fatalf("same coordinates = %v, want a unique violation", err)
	}

	if err := pg.CreatePacmanPackage(ctx, pacmanFixture(repoID, "tool", "1:2.1-1", "x86_64", 4)); err != nil {
		t.Fatalf("second version: %v", err)
	}

	got, err := pg.GetPacmanPackageByFilename(ctx, repoID, pkg.Filename)
	if err != nil || got == nil {
		t.Fatalf("GetPacmanPackageByFilename = %v, %v", got, err)
	}

	if got.Name != "tool" || got.Version != "1:2.0-1" || got.Arch != "x86_64" || got.BuildDate != 1700000000 ||
		got.InstalledSize != 4096 || got.SHA256 != pkg.SHA256 || got.SizeBytes != 100 || len(got.Depends) != 2 || got.FileList != nil {
		t.Errorf("round-tripped row = %+v", got)
	}

	if missing, err := pg.GetPacmanPackageByFilename(ctx, repoID, "nope-1-1-any.pkg.tar.zst"); err != nil || missing != nil {
		t.Errorf("missing package = %v, %v", missing, err)
	}

	list, err := pg.ListPacmanPackages(ctx, repoID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListPacmanPackages = %d rows, %v; want 2", len(list), err)
	}

	if err := pg.DeletePacmanPackage(ctx, repoID, got.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if gone, _ := pg.GetPacmanPackageByFilename(ctx, repoID, pkg.Filename); gone != nil {
		t.Error("the deleted package is still live")
	}

	if err := pg.CreatePacmanPackage(ctx, pacmanFixture(repoID, "tool", "1:2.0-1", "x86_64", 5)); err != nil {
		t.Errorf("re-publishing a deleted package: %v", err)
	}
}

// The file list is filled lazily, once, and listed apart from the rows.
func TestPacmanFileListFill(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "arch-files", "pacman")

	pkg := pacmanFixture(repoID, "tool", "1.0-1", "any", 1)
	if err := pg.CreatePacmanPackage(ctx, pkg); err != nil {
		t.Fatal(err)
	}

	pending, err := pg.ListPacmanPackagesWithoutFileList(ctx, repoID)
	if err != nil || len(pending) != 1 || pending[0].ID != pkg.ID {
		t.Fatalf("without file list = %v, %v", pending, err)
	}

	if err := pg.SetPacmanFileList(ctx, repoID, pkg.ID, "usr/\nusr/bin/tool"); err != nil {
		t.Fatal(err)
	}

	if err := pg.SetPacmanFileList(ctx, repoID, pkg.ID, "other"); err != nil {
		t.Fatal(err)
	}

	lists, err := pg.ListPacmanFileLists(ctx, repoID)
	if err != nil || lists[pkg.ID] != "usr/\nusr/bin/tool" {
		t.Errorf("file lists = %v, %v; want the first one kept", lists, err)
	}

	if pending, err = pg.ListPacmanPackagesWithoutFileList(ctx, repoID); err != nil || len(pending) != 0 {
		t.Errorf("without file list after fill = %v, %v", pending, err)
	}

	list, err := pg.ListPacmanPackages(ctx, repoID)
	if err != nil || len(list) != 1 || list[0].FileList != nil {
		t.Errorf("ListPacmanPackages must not load file lists: %v, %v", list, err)
	}
}

// A signature is stored with its source and key and replaced as a whole.
func TestPacmanSetSignature(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "arch-sig", "pacman")

	pkg := pacmanFixture(repoID, "tool", "1.0-1", "any", 1)
	if err := pg.CreatePacmanPackage(ctx, pkg); err != nil {
		t.Fatal(err)
	}

	if err := pg.SetPacmanSignature(ctx, repoID, pkg.ID, "c2ln", models.PacmanSigServer, "ABCD"); err != nil {
		t.Fatal(err)
	}

	got, err := pg.GetPacmanPackageByFilename(ctx, repoID, pkg.Filename)
	if err != nil || got == nil || got.Signature != "c2ln" || got.SignatureSource != models.PacmanSigServer || got.SignatureKey != "ABCD" {
		t.Fatalf("after server signature = %+v, %v", got, err)
	}

	if err := pg.SetPacmanSignature(ctx, repoID, pkg.ID, "dXA=", models.PacmanSigUploader, ""); err != nil {
		t.Fatal(err)
	}

	if got, err = pg.GetPacmanPackageByFilename(ctx, repoID, pkg.Filename); err != nil || got.Signature != "dXA=" ||
		got.SignatureSource != models.PacmanSigUploader || got.SignatureKey != "" {
		t.Errorf("after uploader signature = %+v, %v", got, err)
	}
}
