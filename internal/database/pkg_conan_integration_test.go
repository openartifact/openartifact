// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func conanFile(repo, name, version, rrev, pid, prev, filename, content string) *models.ConanPackage {
	sum := sha256.Sum256([]byte(filename + content))
	sha := hex.EncodeToString(sum[:])

	return &models.ConanPackage{
		RepositoryID: repo, Name: name, Version: version, UserChannel: "_/_", RecipeRevision: rrev,
		PackageID: pid, PackageRevision: prev, Filename: filename,
		SHA256: sha, BlobKey: "blobs/" + sha[:2] + "/" + filename + content, SizeBytes: int64(len(content)),
	}
}

// TestConanStoreRoundTrip writes recipe and package files through the typed
// store and reads them back through every typed read and through the generic
// rows (spec: /tmp/oa-plug/schema/104_conan.sql).
//
//nolint:gocyclo,cyclop // one sequential scenario: every step reads the rows the previous ones wrote
func TestConanStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	repo := newQuotaRepo(t, pg, "conan-rt", 1<<30, 1000)
	other := newQuotaRepo(t, pg, "conan-rt-other", 0, 0)

	count := func(q string, args ...any) int {
		t.Helper()

		var n int
		if err := pg.GetDB().QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}

		return n
	}

	files := []*models.ConanPackage{
		conanFile(repo, "zlib", "1.3", "r1", "", "", "conanfile.py", "recipe"),
		conanFile(repo, "zlib", "1.3", "r1", "", "", "conanmanifest.txt", "manifest!"),
		conanFile(repo, "zlib", "1.3", "r1", "PID1", "prev1", "conan_package.tgz", "binary-one"),
		conanFile(repo, "zlib", "1.3", "r1", "PID1", "prev1", "conanmanifest.txt", "pkg-manifest"),
		conanFile(repo, "zlib", "1.3", "r1", "PID2", "prev2", "metadata/sign/signature.json", "sig"),
		conanFile(repo, "zlib", "1.3", "r2", "", "", "conanfile.py", "recipe-two"),
		// stored before 0.5.0: no filename, listed
		conanFile(repo, "old", "0.1", "0", "", "", "", "legacy"),
	}
	files[0].MD5, files[0].SHA1 = "d41d8cd98f00b204e9800998ecf8427e", "da39a3ee5e6b4b0d3255bfef95601890afd80709"

	var usedBytes int64

	for _, f := range files {
		// The upload path reserves (size, 1); the insert of the row consumes it.
		reserveN(t, pg, repo, f.SizeBytes, 1)

		usedBytes += f.SizeBytes

		if err := pg.CreateConanPackage(ctx, f); err != nil {
			t.Fatalf("CreateConanPackage(%s): %v", f.Filename, err)
		}

		if f.ID == "" || f.CreatedAt.IsZero() || f.UpdatedAt.IsZero() {
			t.Errorf("CreateConanPackage(%s) left id/timestamps unset: %+v", f.Filename, f)
		}
	}

	// Generic rows: P/V/F counts, keys, side table, listed, role.
	if n := count(`SELECT COUNT(*) FROM packages WHERE repository_id = $1 AND format = 'conan'`, repo); n != 2 {
		t.Errorf("packages = %d, want 2", n)
	}

	if n := count(`SELECT COUNT(*) FROM package_versions WHERE repository_id = $1`, repo); n != 3 {
		t.Errorf("versions = %d, want 3 (zlib r1, zlib r2, old)", n)
	}

	if n := count(`SELECT COUNT(*) FROM package_versions WHERE repository_id = $1 AND version_key = '1.3@_/_#r1'
		AND metadata = '{"user_channel":"_/_","recipe_revision":"r1"}'::jsonb`, repo); n != 1 {
		t.Errorf("version_key/metadata of zlib r1 mismatch")
	}

	if n := count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, repo); n != len(files) {
		t.Errorf("files = %d, want %d", n, len(files))
	}

	if n := count(
		`SELECT COUNT(*) FROM conan_files cf JOIN package_files f ON f.id = cf.id WHERE f.repository_id = $1`,
		repo,
	); n != len(
		files,
	) {
		t.Errorf("conan_files = %d, want %d", n, len(files))
	}

	// listed = recipe-level conanmanifest.txt and the pre-0.5.0 empty filename: 2 rows.
	if n := count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND listed`, repo); n != 2 {
		t.Errorf("listed files = %d, want 2", n)
	}

	var path, filename, attrsFile, release string

	var listed bool
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT path, filename, attrs->>'filename', attrs->>'release', listed FROM package_files WHERE id = $1`, files[2].ID,
	).Scan(&path, &filename, &attrsFile, &release, &listed); err != nil {
		t.Fatal(err)
	}

	if path != "zlib/1.3@_/_#r1/PID1#prev1/conan_package.tgz" || filename != "conan_package.tgz" ||
		attrsFile != "conan_package.tgz" || release != "r1" || listed {
		t.Errorf("package file = path %q filename %q attrs.filename %q release %q listed %v", path, filename, attrsFile, release, listed)
	}

	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT path, filename, attrs->>'filename', listed FROM package_files WHERE id = $1`, files[1].ID).
		Scan(&path, &filename, &attrsFile, &listed); err != nil {
		t.Fatal(err)
	}

	if path != "zlib/1.3@_/_#r1/conanmanifest.txt" || filename != "zlib/1.3@_/_#r1" || attrsFile != "conanmanifest.txt" || !listed {
		t.Errorf("listed file = path %q filename %q attrs.filename %q listed %v", path, filename, attrsFile, listed)
	}

	if err := pg.GetDB().QueryRowContext(ctx, `SELECT path FROM package_files WHERE id = $1`, files[6].ID).Scan(&path); err != nil {
		t.Fatal(err)
	}

	if path != "old/0.1@_/_#0/" {
		t.Errorf("pre-0.5.0 path = %q", path)
	}

	var sums string
	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT checksums::text FROM package_files WHERE id = $1`, files[0].ID).
		Scan(&sums); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(sums, `"md5": "d41d8cd98f00b204e9800998ecf8427e"`) ||
		!strings.Contains(sums, `"sha1": "da39a3ee5e6b4b0d3255bfef95601890afd80709"`) {
		t.Errorf("checksums = %s", sums)
	}

	if n := count(
		`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND (unit_key IS NOT NULL OR role <> 'primary')`,
		repo,
	); n != 0 {
		t.Errorf("%d files with unit_key or non-primary role", n)
	}

	requireUsage(t, pg, repo, usedBytes, int64(len(files)))

	if n := pendingEntries(t, pg, repo); n != 0 {
		t.Errorf("%d pending reservations left after the inserts, want 0", n)
	}

	// Typed reads.
	got, err := pg.GetConanPackage(ctx, repo, models.ConanFileKey{
		Name:            "zlib",
		Version:         "1.3",
		UserChannel:     "_/_",
		RecipeRevision:  "r1",
		PackageID:       "PID1",
		PackageRevision: "prev1",
		Filename:        "conan_package.tgz",
	})
	if err != nil || got == nil {
		t.Fatalf("GetConanPackage = %v, %v", got, err)
	}

	if got.ID != files[2].ID || got.Filename != "conan_package.tgz" || got.PackageID != "PID1" || got.PackageRevision != "prev1" ||
		got.RecipeRevision != "r1" || got.UserChannel != "_/_" || got.Version != "1.3" || got.Name != "zlib" ||
		got.BlobKey != files[2].BlobKey || got.SHA256 != files[2].SHA256 || got.SizeBytes != files[2].SizeBytes {
		t.Errorf("GetConanPackage = %+v, want %+v", got, files[2])
	}

	legacy, err := pg.GetConanPackage(ctx, repo, models.ConanFileKey{Name: "old", Version: "0.1", UserChannel: "_/_", RecipeRevision: "0"})
	if err != nil || legacy == nil || legacy.ID != files[6].ID || legacy.Filename != "" {
		t.Fatalf("GetConanPackage(pre-0.5.0) = %+v, %v", legacy, err)
	}

	if miss, err := pg.GetConanPackage(ctx, other, models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "r1", Filename: "conanfile.py",
	}); err != nil || miss != nil {
		t.Errorf("GetConanPackage(other repo) = %v, %v; want nil", miss, err)
	}

	// A recipe file does not answer for the package-file key and vice versa.
	if miss, err := pg.GetConanPackage(ctx, repo, models.ConanFileKey{
		Name:            "zlib",
		Version:         "1.3",
		UserChannel:     "_/_",
		RecipeRevision:  "r1",
		PackageID:       "PID1",
		PackageRevision: "prev1",
		Filename:        "conanfile.py",
	}); err != nil || miss != nil {
		t.Errorf("GetConanPackage(wrong coordinates) = %v, %v; want nil", miss, err)
	}

	recipe, err := pg.ListConanRecipeFiles(ctx, repo, "zlib", "1.3", "_/_")
	if err != nil || len(recipe) != 6 {
		t.Fatalf("ListConanRecipeFiles = %d files, %v; want 6", len(recipe), err)
	}

	for i, f := range recipe {
		if f.ID != files[i].ID {
			t.Errorf("ListConanRecipeFiles[%d] = %s, want %s (oldest first)", i, f.Filename, files[i].Filename)
		}
	}

	if none, err := pg.ListConanRecipeFiles(ctx, repo, "zlib", "1.3", "me/stable"); err != nil || len(none) != 0 {
		t.Errorf("ListConanRecipeFiles(other channel) = %d, %v", len(none), err)
	}

	recipes, err := pg.ListConanRecipes(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}

	// "old" only has a file with an empty filename, so it is no recipe.
	if len(recipes) != 1 || recipes[0] != (models.ConanRecipe{Name: "zlib", Version: "1.3", UserChannel: "_/_"}) {
		t.Errorf("ListConanRecipes = %+v", recipes)
	}

	// Digests: set, find by SHA-1 (repository scoped), clear.
	if found, err := pg.FindConanPackageBySHA1(ctx, repo, files[0].SHA1); err != nil || found == nil || found.ID != files[0].ID {
		t.Errorf("FindConanPackageBySHA1 = %+v, %v", found, err)
	}

	if found, err := pg.FindConanPackageBySHA1(ctx, other, files[0].SHA1); err != nil || found != nil {
		t.Errorf("FindConanPackageBySHA1(other repo) = %+v, %v; want nil", found, err)
	}

	if found, err := pg.FindConanPackageBySHA1(ctx, repo, ""); err != nil || found != nil {
		t.Errorf("FindConanPackageBySHA1(empty) = %+v, %v; want nil", found, err)
	}

	if err := pg.SetConanPackageDigests(
		ctx,
		files[2].ID,
		"00112233445566778899aabbccddeeff",
		"0123456789abcdef0123456789abcdef01234567",
	); err != nil {
		t.Fatal(err)
	}

	if found, err := pg.FindConanPackageBySHA1(ctx, repo, "0123456789abcdef0123456789abcdef01234567"); err != nil || found == nil ||
		found.ID != files[2].ID || found.MD5 != "00112233445566778899aabbccddeeff" {
		t.Errorf("FindConanPackageBySHA1(after set) = %+v, %v", found, err)
	}

	// Re-uploading replaces the blob and digests of the same file in place.
	again := conanFile(repo, "zlib", "1.3", "r1", "", "", "conanfile.py", "recipe-v2-longer")

	reserveN(t, pg, repo, again.SizeBytes, 1)

	if err := pg.CreateConanPackage(ctx, again); err != nil {
		t.Fatal(err)
	}

	if again.ID != files[0].ID || !again.CreatedAt.Equal(files[0].CreatedAt) {
		t.Errorf("re-upload changed identity: id %s -> %s, created %v -> %v", files[0].ID, again.ID, files[0].CreatedAt, again.CreatedAt)
	}

	if n := count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, repo); n != len(files) {
		t.Errorf("files after re-upload = %d, want %d", n, len(files))
	}

	if n := count(
		`SELECT COUNT(*) FROM conan_files cf JOIN package_files f ON f.id = cf.id WHERE f.repository_id = $1`,
		repo,
	); n != len(
		files,
	) {
		t.Errorf("conan_files after re-upload = %d, want %d", n, len(files))
	}

	re, err := pg.GetConanPackage(
		ctx,
		repo,
		models.ConanFileKey{Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "r1", Filename: "conanfile.py"},
	)
	if err != nil || re == nil || re.BlobKey != again.BlobKey || re.MD5 != "" || re.SHA1 != "" {
		t.Errorf("after re-upload = %+v, %v; want the new blob and no digests", re, err)
	}

	// A file without blob content (restored from an old backup) keeps no blob.
	bare := &models.ConanPackage{
		RepositoryID:   repo,
		Name:           "zlib",
		Version:        "1.3",
		UserChannel:    "_/_",
		RecipeRevision: "r1",
		Filename:       "conaninfo.txt",
	}
	reserveN(t, pg, repo, 0, 1)

	if err := pg.CreateConanPackage(ctx, bare); err != nil {
		t.Fatal(err)
	}

	if g, err := pg.GetConanPackage(
		ctx,
		repo,
		models.ConanFileKey{Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "r1", Filename: "conaninfo.txt"},
	); err != nil ||
		g == nil || g.BlobKey != "" ||
		g.SizeBytes != 0 {
		t.Errorf("blob-less file = %+v, %v", g, err)
	}

	// Deletes: the last file of a recipe revision takes its version, and the
	// last version its package, with it; the quota counters follow.
	pendingBefore := pendingEntries(t, pg, repo)

	if err := pg.DeleteConanPackage(ctx, files[5].ID); err != nil {
		t.Fatal(err)
	}

	if n := count(`SELECT COUNT(*) FROM package_versions WHERE repository_id = $1 AND version_key = '1.3@_/_#r2'`, repo); n != 0 {
		t.Errorf("version r2 survived the delete of its last file")
	}

	if n := count(`SELECT COUNT(*) FROM packages WHERE repository_id = $1 AND name_key = 'zlib'`, repo); n != 1 {
		t.Errorf("package zlib pruned while r1 is live")
	}

	if err := pg.DeleteConanPackage(ctx, files[5].ID); err != nil {
		t.Errorf("deleting a missing file = %v, want nil", err)
	}

	if err := pg.DeleteConanPackage(ctx, files[6].ID); err != nil {
		t.Fatal(err)
	}

	if n := count(`SELECT COUNT(*) FROM packages WHERE repository_id = $1 AND name_key = 'old'`, repo); n != 0 {
		t.Errorf("package old survived the delete of its last file")
	}

	// Each delete hands its usage back as one pending release entry.
	if n := pendingEntries(t, pg, repo); n != pendingBefore+2 {
		t.Errorf("pending release entries after two deletes = %d, want %d", n, pendingBefore+2)
	}

	if n := count(`SELECT COUNT(*) FROM package_files WHERE id = ANY($1::uuid[])`, "{"+files[5].ID+","+files[6].ID+"}"); n != 0 {
		t.Errorf("deleted files still present")
	}
}
