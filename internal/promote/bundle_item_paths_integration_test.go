// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// itemFormatCase is one format's stored file and the spellings of its path the
// clients send: the stored path, the leading slash, the listed file name (the
// web item picker and `oa bundle add --from-build`) and the native repository
// URL path (`oa bundle add --path`) when it differs from the stored one.
type itemFormatCase struct {
	name, format, pkg, version string
	// path and filename are package_files.path and filename of the item's file.
	path, filename string
	// native are further spellings that name that file.
	native []string
}

// itemFormatCases covers every format with a storage descriptor (the test below
// fails when one is missing) with the paths its store writes.
//
//nolint:gochecknoglobals // immutable table
var itemFormatCases = []itemFormatCase{
	{"ansible", "ansible", "acme.web", "1.0.0", "acme/web/1.0.0/acme-web-1.0.0.tar.gz", "acme-web-1.0.0.tar.gz", nil},
	{"apk", "apk", "foo", "1.0-r0", "v3.19/main/x86_64/foo-1.0-r0.apk", "foo-1.0-r0.apk", nil},
	{"cargo", "cargo", "serde", "1.0.0", "serde/1.0.0/serde-1.0.0.crate", "serde-1.0.0.crate", nil},
	{"composer", "composer", "acme/lib", "1.0.0", "acme/lib/1.0.0/lib-1.0.0.zip", "lib-1.0.0.zip", nil},
	{"conan", "conan", "zlib", "1.3", "zlib/1.3/_/_/rev1/export/conan_sources.tgz", "conan_sources.tgz", nil},
	{"conda", "conda", "numpy", "1.0", "linux-64/numpy-1.0-py39_0.conda", "numpy-1.0-py39_0.conda", nil},
	{"deb", "deb", "foo", "1.0", "pool/main/f/foo/foo_1.0_amd64.deb", "foo_1.0_amd64.deb", nil},
	{"go", "go", "example.com/mod", "v1.0.0", "example.com/mod/@v/v1.0.0.zip", "v1.0.0.zip", nil},
	{"helm", "helm", "chart", "1.0.0", "chart/1.0.0/chart-1.0.0.tgz", "chart-1.0.0.tgz", nil},
	{
		"ivy with a multi-segment group (CLI form)", "ivy", "com.example:lib", "1.0",
		"com.example/lib/1.0/lib-1.0.jar", "lib-1.0.jar",
		[]string{"com/example/lib/1.0/lib-1.0.jar", "/com/example/lib/1.0/lib-1.0.jar", "repos/%repo%/com/example/lib/1.0/lib-1.0.jar"},
	},
	{
		"ivy with a type directory", "ivy", "acme:lib", "1.0",
		"acme/lib/1.0/jars/lib-1.0.jar", "jars/lib-1.0.jar",
		[]string{"acme/lib/1.0/jars/lib-1.0.jar"},
	},
	{"maven", "maven", "g:a", "1.0", "g/a/1.0/a-1.0.jar", "g/a/1.0/a-1.0.jar", nil},
	{"nix", "nix", "hello", "1.0", "abc123.narinfo", "abc123.narinfo", nil},
	{"npm", "npm", "@scope/name", "1.0.0", "@scope/name/-/name-1.0.0.tgz", "name-1.0.0.tgz", []string{"@scope%2Fname/-/name-1.0.0.tgz"}},
	{"nuget", "nuget", "demo", "1.0.0", "demo/1.0.0/demo.1.0.0.nupkg", "demo.1.0.0.nupkg", nil},
	{"pacman", "pacman", "foo", "1.0-1", "foo-1.0-1-x86_64.pkg.tar.zst", "foo-1.0-1-x86_64.pkg.tar.zst", nil},
	{
		"pypi",
		"pypi",
		"demo",
		"1.0",
		"demo/1.0/demo-1.0-py3-none-any.whl",
		"demo-1.0-py3-none-any.whl",
		[]string{"packages/demo/demo-1.0-py3-none-any.whl"},
	},
	{"raw", "raw", "dir/file.bin", "1", "dir/file.bin", "dir/file.bin", nil},
	{"rpm", "rpm", "foo", "1.0", "foo-1.0-1.x86_64.rpm", "foo-1.0-1.x86_64.rpm", nil},
	{"rubygems", "rubygems", "rails", "7.0.0", "rails/7.0.0/rails-7.0.0.gem", "rails-7.0.0.gem", nil},
	{
		"sbt with a multi-segment group (CLI form)", "sbt", "org.scala:lib_2.13", "1.0",
		"org.scala/lib_2.13/1.0/lib_2.13-1.0.jar", "lib_2.13-1.0.jar",
		[]string{"org/scala/lib_2.13/1.0/lib_2.13-1.0.jar", "/org/scala/lib_2.13/1.0/lib_2.13-1.0.jar"},
	},
	{"terraform", "terraform", "ns/name/aws", "1.0.0", "ns/name/aws/1.0.0/name-1.0.0.tar.gz", "name-1.0.0.tar.gz", nil},
}

// TestBundleItemPathsResolveAlikeWhenAddedAndPromoted pins, for every format, that
// each spelling of an item path a client sends is accepted when the item is added
// and then selects, when the item is promoted, exactly the file it was accepted
// for (not its sibling with the same content); and that no spelling reaches a file
// of another digest, of another repository, or a file under another format.
func TestBundleItemPathsResolveAlikeWhenAddedAndPromoted(t *testing.T) {
	covered := map[string]bool{}

	for _, tc := range itemFormatCases {
		covered[tc.format] = true

		t.Run(tc.name, func(t *testing.T) { checkItemFormatCase(t, tc) })
	}

	for _, f := range storage.Names() {
		if !covered[f] {
			t.Errorf("format %s has a storage descriptor but no bundle item path case", f)
		}
	}
}

// itemSpellings are the spellings of the item path of tc that name its file.
func itemSpellings(tc itemFormatCase, repo, fileID string) []string {
	spellings := []string{
		tc.path,
		"/" + tc.path,
		tc.filename,
		"repos/" + repo + "/" + tc.path,
		fileID,
	}

	for _, n := range tc.native {
		spellings = append(spellings, strings.ReplaceAll(n, "%repo%", repo))
	}

	return spellings
}

func checkItemFormatCase(t *testing.T, tc itemFormatCase) {
	t.Helper()

	h := newHarness(t)
	ctx := context.Background()

	src, empty := h.repo(tc.format, false), h.repo(tc.format, false)
	blobID, sha := h.blob("content of " + tc.name)
	otherBlob, otherSHA := h.blob("another content of " + tc.name)

	// The item's file, a sibling holding the same content under another name, and
	// a file holding other content under the item's directory.
	file := h.file(src.ID, blobID, fileSeed{pkg: tc.pkg, version: tc.version, path: tc.path, filename: tc.filename})
	sibling := h.file(src.ID, blobID, fileSeed{
		pkg: tc.pkg, version: tc.version, path: siblingOf(tc.path), filename: siblingOf(tc.filename),
	})
	h.file(src.ID, otherBlob, fileSeed{pkg: tc.pkg, version: tc.version, path: tc.path + ".other", filename: tc.filename + ".other"})

	for _, spelling := range itemSpellings(tc, src.Name, file) {
		stored, err := h.pg.BundleItemStored(ctx, src.ID, src.Name, tc.format, sha, spelling)
		if err != nil || !stored {
			t.Errorf("adding %q: stored = %v, %v; want it accepted", spelling, stored, err)
		}

		if got := promotedFiles(t, h, src, tc.format, sha, spelling); !slices.Equal(got, []string{file}) {
			t.Errorf("promoting %q selects files %v, want only %s (sibling %s)", spelling, got, file, sibling)
		}
	}

	// The sibling's own path names the sibling, not the item's file.
	if got := promotedFiles(t, h, src, tc.format, sha, siblingOf(tc.path)); !slices.Equal(got, []string{sibling}) {
		t.Errorf("promoting the sibling's path selects %v, want %s", got, sibling)
	}

	rejected := []struct {
		what, repo, name, format, sha, path string
	}{
		{"the digest of other content", src.ID, src.Name, tc.format, otherSHA, tc.path},
		{"an unknown digest", src.ID, src.Name, tc.format, strings.Repeat("0", 64), tc.path},
		{"a repository that does not hold the file", empty.ID, empty.Name, tc.format, sha, tc.path},
		{"a path naming another file", src.ID, src.Name, tc.format, sha, tc.path + ".nothing"},
		{"the URL of another repository", src.ID, src.Name, tc.format, sha, "repos/" + empty.Name + "/" + tc.path},
		{"another format", src.ID, src.Name, "docker", sha, tc.path},
	}

	for _, r := range rejected {
		stored, err := h.pg.BundleItemStored(ctx, r.repo, r.name, r.format, r.sha, r.path)
		if err != nil || stored {
			t.Errorf("%s: stored = %v, %v; want it rejected", r.what, stored, err)
		}
	}
}

// siblingOf names a file next to name: the same directory, another file name.
func siblingOf(name string) string {
	dir, base := "", name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		dir, base = name[:i+1], name[i+1:]
	}

	return dir + "alt-" + base
}

// promotedFiles returns the ids of the source files the promotion of the item
// copies (the ones holding its content, not the siblings of their versions).
func promotedFiles(t *testing.T, h *harness, src *models.Repository, format, sha, path string) []string {
	t.Helper()

	ctx := context.Background()

	d, ok := storage.Lookup(format)
	if !ok {
		t.Fatalf("no storage descriptor for %s", format)
	}

	fp, err := newFilePromoter(format, d)
	if err != nil {
		t.Fatalf("newFilePromoter: %v", err)
	}

	tx, err := h.pg.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	files, err := fp.sourceFiles(ctx, tx, Request{Src: src, SHA256: sha, Path: path})
	if err != nil {
		t.Fatalf("sourceFiles(%q): %v", path, err)
	}

	var ids []string

	for _, f := range files {
		if f.matched {
			ids = append(ids, f.id)
		}
	}

	return ids
}

// TestDockerBundleItemPathsResolveAlikeWhenAddedAndPromoted is the docker side:
// the spellings of a manifest accepted when an item is added select that
// manifest, not the one with the same content under another image, when the
// item is promoted.
func TestDockerBundleItemPathsResolveAlikeWhenAddedAndPromoted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	src, empty := h.repo("docker", false), h.repo("docker", false)
	blobID, sha := h.blob("manifest bytes")
	app := seedDockerManifest(h, src.ID, blobID, dockerSpec{image: "app", blobs: []string{"layer"}, tags: []string{"v1"}})
	seedDockerManifest(h, src.ID, blobID, dockerSpec{image: "tools/cli", blobs: []string{"layer"}, tags: []string{"v2"}})

	for _, spelling := range []string{
		app.digest, "sha256:" + sha, app.id, "app", "app:v1", src.Name + "/app", src.Name + "/app:v1",
		src.Name + "/app@" + app.digest, "/v2/" + src.Name + "/app/manifests/v1", "v2/" + src.Name + "/app/manifests/" + app.digest,
	} {
		stored, err := h.pg.BundleItemStored(ctx, src.ID, src.Name, "docker", sha, spelling)
		if err != nil || !stored {
			t.Errorf("adding %q: stored = %v, %v; want it accepted", spelling, stored, err)
		}

		if spelling == app.digest || spelling == "sha256:"+sha || spelling == app.id {
			continue // the content digest names both manifests; the row id is checked below
		}

		if got := promotedManifests(t, h, src, sha, spelling); !slices.Equal(got, []string{app.id}) {
			t.Errorf("promoting %q selects manifests %v, want only %s", spelling, got, app.id)
		}
	}

	if got := promotedManifests(t, h, src, sha, app.id); !slices.Equal(got, []string{app.id}) {
		t.Errorf("promoting the manifest id selects %v, want only %s", got, app.id)
	}

	for _, r := range []struct{ what, repo, name, path string }{
		{"a name that is no manifest's", src.ID, src.Name, "evil:latest"},
		{"a repository that does not hold the manifest", empty.ID, empty.Name, "app:v1"},
		{"another repository's distribution name", src.ID, src.Name, empty.Name + "/app:v1"},
	} {
		if stored, err := h.pg.BundleItemStored(ctx, r.repo, r.name, "docker", sha, r.path); err != nil || stored {
			t.Errorf("%s: stored = %v, %v; want it rejected", r.what, stored, err)
		}
	}

	_, otherSHA := h.blob("other manifest bytes")
	if stored, err := h.pg.BundleItemStored(ctx, src.ID, src.Name, "docker", otherSHA, "app:v1"); err != nil || stored {
		t.Errorf("the digest of other content: stored = %v, %v; want it rejected", stored, err)
	}
}

func promotedManifests(t *testing.T, h *harness, src *models.Repository, sha, path string) []string {
	t.Helper()

	ctx := context.Background()

	tx, err := h.pg.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	roots, err := dockerRoots(ctx, tx, Request{Src: src, SHA256: sha, Path: path})
	if err != nil {
		t.Fatalf("dockerRoots(%q): %v", path, err)
	}

	ids := make([]string, len(roots))
	for i, r := range roots {
		ids[i] = r.id
	}

	return ids
}
