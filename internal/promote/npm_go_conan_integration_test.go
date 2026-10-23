// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// npmDistTags returns the dist-tags of the package as tag -> version.
func npmDistTags(h *harness, pkgID string) map[string]string {
	h.t.Helper()

	rows, err := h.pg.GetDB().QueryContext(context.Background(), `SELECT tag, version FROM npm_dist_tags WHERE package_id = $1::uuid`, pkgID)
	if err != nil {
		h.t.Fatalf("list dist-tags: %v", err)
	}

	defer func() { _ = rows.Close() }()

	tags := map[string]string{}

	for rows.Next() {
		var tag, version string
		if err := rows.Scan(&tag, &version); err != nil {
			h.t.Fatalf("scan dist-tag: %v", err)
		}

		tags[tag] = version
	}

	if err := rows.Err(); err != nil {
		h.t.Fatalf("dist-tags: %v", err)
	}

	return tags
}

func assertTags(t *testing.T, got, want map[string]string) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("dist-tags = %v, want %v", got, want)

		return
	}

	for tag, version := range want {
		if got[tag] != version {
			t.Errorf("dist-tags = %v, want %v", got, want)

			return
		}
	}
}

func hasNPMRegen(res *Result, pkgID string) bool {
	return slices.Contains(res.Regen, Regen{Kind: RegenNPM, PackageID: pkgID})
}

// A second version promoted into an existing package leaves the package's own
// dist-tags alone, adds the tags only the source had and asks for a refresh.
func TestNpmGoConanNPMSecondVersionKeepsDistTags(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "npm")
	src, dst := h.repo("npm", false), h.repo("npm", false)

	old, _ := h.blob("shop 1.0.0")
	fresh, freshSHA := h.blob("shop 1.1.0")

	dstPkg := npmSeedPackage(h, dst.ID, "1.0.0")
	npmSeedVersion(h, dstPkg, old, "1.0.0")
	npmSeedTag(h, dstPkg, "latest", "1.0.0")
	npmSeedTag(h, dstPkg, "beta", "1.0.0")

	srcPkg := npmSeedPackage(h, src.ID, "1.1.0")
	npmSeedVersion(h, srcPkg, fresh, "1.1.0")
	npmSeedTag(h, srcPkg, "latest", "1.1.0")
	npmSeedTag(h, srcPkg, "next", "1.1.0")
	npmSeedTag(h, srcPkg, "beta", "1.1.0")

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: freshSHA, Path: "shop-1.1.0.tgz"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	if len(res.Copied) != 1 || res.Copied[0].Name != "@acme/shop" || res.Copied[0].Version != "1.1.0" || res.Copied[0].Path != "shop-1.1.0.tgz" {
		t.Errorf("copied = %+v, want @acme/shop 1.1.0", res.Copied)
	}

	if want := int64(len("shop 1.1.0")); res.Usage.Bytes != want || res.Usage.Artifacts != 1 {
		t.Errorf("usage = %+v, want %d bytes and 1 artifact", res.Usage, want)
	}

	if !slices.Equal(res.Licenses, []string{"MIT"}) {
		t.Errorf("licenses = %v, want [MIT]", res.Licenses)
	}

	if !hasNPMRegen(res, dstPkg) {
		t.Errorf("regen = %+v, want an npm refresh of package %s", res.Regen, dstPkg)
	}

	assertTags(t, npmDistTags(h, dstPkg), map[string]string{"latest": "1.0.0", "beta": "1.0.0", "next": "1.1.0"})

	if n := h.count(`SELECT COUNT(*) FROM npm_versions WHERE package_id = $1::uuid AND deleted_at IS NULL`, dstPkg); n != 2 {
		t.Errorf("live versions in the target = %d, want 2", n)
	}

	if got := h.str(`SELECT latest_version FROM npm_packages WHERE id = $1::uuid`, dstPkg); got != "1.0.0" {
		t.Errorf("latest_version = %q, want the target's own 1.0.0", got)
	}

	if got := h.str(`SELECT packument_stale::text FROM npm_packages WHERE id = $1::uuid`, dstPkg); got != "true" {
		t.Errorf("packument_stale = %q, want true", got)
	}
}

// A package left without a latest tag gets one on the copied version, and its
// latest_version follows.
func TestNpmGoConanNPMLatestFallback(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "npm")
	src, dst := h.repo("npm", false), h.repo("npm", false)

	old, _ := h.blob("shop 1.0.0")
	fresh, freshSHA := h.blob("shop 1.1.0")

	dstPkg := npmSeedPackage(h, dst.ID, "1.0.0")
	npmSeedVersion(h, dstPkg, old, "1.0.0")
	npmSeedTag(h, dstPkg, "beta", "1.0.0")

	srcPkg := npmSeedPackage(h, src.ID, "1.1.0")
	npmSeedVersion(h, srcPkg, fresh, "1.1.0")

	if _, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: freshSHA}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	assertTags(t, npmDistTags(h, dstPkg), map[string]string{"beta": "1.0.0", "latest": "1.1.0"})

	if got := h.str(`SELECT latest_version FROM npm_packages WHERE id = $1::uuid`, dstPkg); got != "1.1.0" {
		t.Errorf("latest_version = %q, want 1.1.0 like the latest tag", got)
	}
}

// A go item may be the go.mod blob: the whole module version is copied, and a
// path narrows it when several versions share the go.mod.
func TestNpmGoConanGoMatchedByModBlob(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "go")
	src, dst := h.repo("go", false), h.repo("go", false)

	zipA, _ := h.blob("zip v1.4.0")
	zipB, _ := h.blob("zip v1.5.0")
	goSeedVersion(h, src.ID, zipA, "v1.4.0")
	goSeedVersion(h, src.ID, zipB, "v1.5.0")

	_, modSHA := h.blob(goFixtureModFile)

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: modSHA, Path: "v1.5.0.mod"})
	if err != nil {
		t.Fatalf("promote by go.mod and path: %v", err)
	}

	if len(res.Copied) != 1 || res.Copied[0].Name != goFixtureModule || res.Copied[0].Version != "v1.5.0" || res.Copied[0].Path != "v1.5.0.zip" {
		t.Fatalf("copied = %+v, want %s v1.5.0", res.Copied, goFixtureModule)
	}

	if want := int64(len("zip v1.5.0") + len(goFixtureModFile)); res.Usage.Bytes != want || res.Usage.Artifacts != 1 {
		t.Errorf("usage = %+v, want zip and go.mod bytes (%d) and 1 artifact", res.Usage, want)
	}

	if n := h.count(`SELECT COUNT(*) FROM gomod_modules WHERE repository_id = $1 AND version = 'v1.5.0' AND zip_blob_id = $2::uuid AND mod_blob_id IS NOT NULL`, dst.ID, zipB); n != 1 {
		t.Errorf("v1.5.0 rows with its zip and go.mod in the target = %d, want 1", n)
	}

	// Without a path every version sharing the go.mod is promoted; the one
	// already there is only reported.
	res, err = h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: modSHA})
	if err != nil {
		t.Fatalf("promote by go.mod: %v", err)
	}

	if len(res.Copied) != 1 || res.Copied[0].Version != "v1.4.0" || len(res.Existing) != 1 || res.Existing[0].Version != "v1.5.0" {
		t.Errorf("copied %+v existing %+v, want v1.4.0 copied and v1.5.0 existing", res.Copied, res.Existing)
	}

	if n := h.count(`SELECT COUNT(*) FROM gomod_modules WHERE repository_id = $1 AND deleted_at IS NULL`, dst.ID); n != 2 {
		t.Errorf("target module versions = %d, want 2", n)
	}
}

// A destination that already holds part of a recipe revision is completed with
// the missing files.
func TestNpmGoConanConanCompletesPartialRevision(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "conan")
	src, dst := h.repo("conan", false), h.repo("conan", false)

	manifest, manifestSHA := h.blob("recipe manifest")
	files := conanFixtureFiles("rrev-a")
	conanSeedFiles(h, src.ID, "rrev-a", manifest, files)
	conanSeedFiles(h, dst.ID, "rrev-a", manifest, files[:2])

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: manifestSHA})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	if len(res.Copied) != len(files)-2 || len(res.Existing) != 2 || res.Usage.Artifacts != len(files)-2 {
		t.Errorf("copied %d existing %d usage %+v, want %d copied, 2 existing", len(res.Copied), len(res.Existing), res.Usage, len(files)-2)
	}

	if n := h.count(`SELECT COUNT(*) FROM conan_packages WHERE repository_id = $1 AND deleted_at IS NULL`, dst.ID); n != len(files) {
		t.Errorf("target files = %d, want the whole revision (%d)", n, len(files))
	}

	if got := conanLatestRevision(h, dst.ID); got != "rrev-a" {
		t.Errorf("latest revision = %q, want rrev-a", got)
	}
}

// One differing file in the destination refuses the whole revision.
func TestNpmGoConanConanConflictsOnDifferingFile(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "conan")
	src, dst := h.repo("conan", false), h.repo("conan", false)

	manifest, manifestSHA := h.blob("recipe manifest")
	files := conanFixtureFiles("rrev-a")
	conanSeedFiles(h, src.ID, "rrev-a", manifest, files)

	differing := []conanSeedFile{{filename: "conanfile.py", content: "class Another"}}
	conanSeedFiles(h, dst.ID, "rrev-a", manifest, differing)

	_, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: manifestSHA})

	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Format != "conan" || conflict.Name != "shop" || conflict.Version != "1.0.0" {
		t.Fatalf("err = %v, want a conan ConflictError for shop 1.0.0", err)
	}

	if n := h.count(`SELECT COUNT(*) FROM conan_packages WHERE repository_id = $1`, dst.ID); n != 1 {
		t.Errorf("target files = %d after the refused promotion, want its own 1", n)
	}
}

// The promoted revision becomes the latest one of the recipe in the
// destination, and revisions promoted together keep their source order.
func TestNpmGoConanConanLatestRevisionIsPromoted(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "conan")
	src, dst := h.repo("conan", false), h.repo("conan", false)

	older, _ := h.blob("older recipe manifest")
	conanSeedFiles(h, dst.ID, "rrev-z-old", older, conanFixtureFiles("rrev-z-old"))

	// Two source revisions share the item blob.
	manifest, manifestSHA := h.blob("recipe manifest")
	conanSeedFiles(h, src.ID, "rrev-b", manifest, conanFixtureFiles("rrev-b"))
	conanSeedFiles(h, src.ID, "rrev-a", manifest, conanFixtureFiles("rrev-a"))

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: manifestSHA, Path: "rrev-b/conanmanifest.txt"})
	if err != nil {
		t.Fatalf("promote one revision: %v", err)
	}

	if want := len(conanFixtureFiles("rrev-b")); len(res.Copied) != want {
		t.Errorf("copied %d, want the %d files of rrev-b only", len(res.Copied), want)
	}

	if got := conanLatestRevision(h, dst.ID); got != "rrev-b" {
		t.Errorf("latest revision = %q, want the promoted rrev-b over the older rrev-z-old", got)
	}

	// Without a path both revisions go; rrev-a was stored last in the source
	// so it is promoted last and ends up latest.
	res, err = h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: manifestSHA})
	if err != nil {
		t.Fatalf("promote both revisions: %v", err)
	}

	want := len(conanFixtureFiles("rrev-a"))
	if len(res.Copied) != want || len(res.Existing) != want {
		t.Errorf("copied %d existing %d, want %d of each (rrev-a new, rrev-b there)", len(res.Copied), len(res.Existing), want)
	}

	if got := conanLatestRevision(h, dst.ID); got != "rrev-a" {
		t.Errorf("latest revision = %q, want rrev-a promoted last", got)
	}
}
