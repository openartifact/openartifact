// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func cranTestFile(repo, name, version, tree, ext, title string) *models.CRANFile {
	filename := name + "_" + version + ext
	sum := sha256.Sum256([]byte(tree + filename + title))
	sha := hex.EncodeToString(sum[:])

	return &models.CRANFile{
		RepositoryID: repo, Package: name, Version: version, Path: tree + "/" + filename, Filename: filename, Tree: tree,
		Fields: map[string]string{"Depends": "R (>= 4.0)", "License": "MIT"}, MD5: sha[:32],
		Title: title, License: "MIT", Digest: sha, BlobKey: "blobs/" + sha[:2] + "/" + sha, SizeBytes: int64(len(filename)),
	}
}

func cranPaths(files []*models.CRANFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}

	return out
}

// TestCRANStoreRoundTrip writes source and binary files of several versions
// through the typed store and reads them back through every typed read.
//
//nolint:gocyclo,cyclop // one sequential scenario: every step reads the rows the previous ones wrote
func TestCRANStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	repo := newQuotaRepo(t, pg, "cran-rt", 1<<30, 1000)
	other := newQuotaRepo(t, pg, "cran-rt-other", 0, 0)

	const win = "bin/windows/contrib/4.5"

	files := []*models.CRANFile{
		cranTestFile(repo, "alpha", "1.0.0", "src/contrib", ".tar.gz", "Alpha one"),
		cranTestFile(repo, "alpha", "1.1.0", "src/contrib", ".tar.gz", "Alpha one-one"),
		cranTestFile(repo, "alpha", "1.1.0", win, ".zip", "binary title"),
		cranTestFile(repo, "beta", "0.1", "src/contrib", ".tar.gz", "Beta"),
	}

	stampBefore, err := pg.CRANIndexStamp(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		reserveN(t, pg, repo, f.SizeBytes, 1)

		// Only a source file replaces the version's descriptive metadata.
		if err := pg.CreateCRANFile(ctx, f, f.Tree == "src/contrib"); err != nil {
			t.Fatalf("CreateCRANFile(%s): %v", f.Path, err)
		}

		if f.ID == "" || f.CreatedAt.IsZero() || f.UpdatedAt.IsZero() {
			t.Errorf("CreateCRANFile(%s) left id/timestamps unset: %+v", f.Path, f)
		}
	}

	stamp, err := pg.CRANIndexStamp(ctx, repo)
	if err != nil || stamp == stampBefore {
		t.Errorf("stamp after four uploads = %q (before %q), %v; want it changed", stamp, stampBefore, err)
	}

	// A path that is live is a duplicate and leaves the repository unchanged.
	reserveN(t, pg, repo, 1, 1)

	dup := cranTestFile(repo, "alpha", "1.0.0", "src/contrib", ".tar.gz", "Alpha again")
	if err := pg.CreateCRANFile(ctx, dup, true); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate CreateCRANFile = %v, want ErrDuplicate", err)
	}

	releaseN(t, pg, repo, 1, 1)

	if again, _ := pg.CRANIndexStamp(ctx, repo); again != stamp {
		t.Errorf("a rejected duplicate changed the stamp: %q -> %q", stamp, again)
	}

	got, err := pg.GetCRANFile(ctx, repo, "src/contrib/alpha_1.1.0.tar.gz")
	if err != nil || got == nil {
		t.Fatalf("GetCRANFile = %v, %v", got, err)
	}

	if got.ID != files[1].ID || got.Package != "alpha" || got.Version != "1.1.0" || got.Tree != "src/contrib" || got.MD5 != files[1].MD5 ||
		got.Fields["Depends"] != "R (>= 4.0)" || got.Digest != files[1].Digest || got.BlobKey != files[1].BlobKey || got.SizeBytes != files[1].SizeBytes {
		t.Errorf("GetCRANFile = %+v, want the row written as %+v", got, files[1])
	}

	// The binary joined the source's version and did not replace its metadata.
	if got.Title != "Alpha one-one" {
		t.Errorf("version title = %q, want the source's", got.Title)
	}

	if bin, _ := pg.GetCRANFile(ctx, repo, win+"/alpha_1.1.0.zip"); bin == nil || bin.Tree != win || bin.Title != "Alpha one-one" {
		t.Errorf("binary file = %+v", bin)
	}

	if missing, err := pg.GetCRANFile(ctx, repo, "src/contrib/alpha_9.tar.gz"); err != nil || missing != nil {
		t.Errorf("GetCRANFile of a missing path = %v, %v; want nil, nil", missing, err)
	}

	if elsewhere, _ := pg.GetCRANFile(ctx, other, "src/contrib/alpha_1.1.0.tar.gz"); elsewhere != nil {
		t.Error("another repository sees the file")
	}

	src, err := pg.ListCRANTreeFiles(ctx, repo, "src/contrib")
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{
		"src/contrib/alpha_1.0.0.tar.gz",
		"src/contrib/alpha_1.1.0.tar.gz",
		"src/contrib/beta_0.1.tar.gz",
	}; !slices.Equal(
		cranPaths(src),
		want,
	) {
		t.Errorf("source tree = %v, want %v", cranPaths(src), want)
	}

	if bin, _ := pg.ListCRANTreeFiles(ctx, repo, win); len(bin) != 1 || bin[0].Package != "alpha" {
		t.Errorf("binary tree = %v", cranPaths(bin))
	}

	if none, err := pg.ListCRANTreeFiles(ctx, repo, "bin/windows/contrib/3.0"); err != nil || len(none) != 0 {
		t.Errorf("empty tree = %v, %v", cranPaths(none), err)
	}

	pkgFiles, err := pg.ListCRANPackageFiles(ctx, repo, "alpha")
	if err != nil || len(pkgFiles) != 3 {
		t.Errorf("ListCRANPackageFiles(alpha) = %v, %v; want three files across two trees", cranPaths(pkgFiles), err)
	}

	if names, err := pg.ListCRANPackageNames(ctx, repo); err != nil || !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Errorf("ListCRANPackageNames = %v, %v", names, err)
	}

	if names, _ := pg.ListCRANPackageNames(ctx, other); len(names) != 0 {
		t.Errorf("another repository lists %v", names)
	}

	// Deleting one file keeps the package and its other files.
	if err := pg.DeleteCRANFile(ctx, repo, files[1].ID); err != nil {
		t.Fatal(err)
	}

	if gone, _ := pg.GetCRANFile(ctx, repo, files[1].Path); gone != nil {
		t.Error("the deleted file is still served")
	}

	if rest, _ := pg.ListCRANPackageFiles(ctx, repo, "alpha"); len(rest) != 2 {
		t.Errorf("after deleting one file alpha has %v", cranPaths(rest))
	}

	afterDelete, _ := pg.CRANIndexStamp(ctx, repo)
	if afterDelete == stamp {
		t.Error("deleting a file did not change the stamp")
	}

	// Deleting the last files of a package removes it from the listings.
	for _, f := range []*models.CRANFile{files[0], files[2]} {
		if err := pg.DeleteCRANFile(ctx, repo, f.ID); err != nil {
			t.Fatal(err)
		}
	}

	if names, _ := pg.ListCRANPackageNames(ctx, repo); !slices.Equal(names, []string{"beta"}) {
		t.Errorf("after deleting all of alpha: %v", names)
	}

	if rest, _ := pg.ListCRANPackageFiles(ctx, repo, "alpha"); len(rest) != 0 {
		t.Errorf("alpha still lists %v", cranPaths(rest))
	}

	// A deleted path can be written again.
	reserveN(t, pg, repo, files[1].SizeBytes, 1)

	again := cranTestFile(repo, "alpha", "1.1.0", "src/contrib", ".tar.gz", "Alpha rewritten")
	if err := pg.CreateCRANFile(ctx, again, true); err != nil {
		t.Errorf("re-creating a deleted path: %v", err)
	}
}
