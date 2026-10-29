// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestFilePromoterCopiesIdempotentlyAndForces drives the descriptor-driven
// promoter end to end with the maven descriptor (shared versions, a side
// table): the copy, its retry, the conflict on other content, a sibling file
// in the destination version that is no obstacle, and the forced replacement.
func TestFilePromoterCopiesIdempotentlyAndForces(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	p := promoterFor(t, "maven")
	src, dst := h.repo("maven", false), h.repo("maven", false)
	blobA, shaA := h.blob("jar A")
	blobB, _ := h.blob("jar B")
	blobPom, _ := h.blob("pom")

	seed := fileSeed{pkg: "com.acme:shop", version: "1.0.0", path: "com/acme/shop/1.0.0/shop-1.0.0.jar", unitKey: "com.acme:shop:1.0.0"}
	srcFile := h.file(src.ID, blobA, seed)
	h.exec(`INSERT INTO maven_files (id, classifier, extension, md5) VALUES ($1, 'sources', 'jar', 'm5')`, srcFile)

	// A sibling in the destination version does not stand in the way.
	h.file(dst.ID, blobPom, fileSeed{pkg: seed.pkg, version: seed.version, path: "com/acme/shop/1.0.0/shop-1.0.0.pom", role: "metadata"})

	req := Request{Src: src, Dst: dst, SHA256: shaA, Path: seed.path}

	res, err := h.promoteOnce(p, req)
	if err != nil || len(res.Copied) != 1 || res.Usage.Artifacts != 1 || res.Usage.Bytes != int64(len("jar A")) {
		t.Fatalf("promote = %+v, %v", res, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files f JOIN maven_files m ON m.id = f.id
		WHERE f.repository_id = $1 AND m.classifier = 'sources' AND m.md5 = 'm5' AND f.unit_key = $2`, dst.ID, seed.unitKey); n != 1 {
		t.Errorf("destination has %d copied file+side rows, want 1", n)
	}

	again, err := h.promoteOnce(p, req)
	if err != nil || len(again.Copied) != 0 || len(again.Existing) != 1 {
		t.Fatalf("retry = %+v, %v", again, err)
	}

	// Other content at the same path is a conflict, unless forced.
	other := h.repo("maven", false)
	h.file(other.ID, blobB, seed)

	if _, err := h.promoteOnce(p, Request{Src: src, Dst: other, SHA256: shaA, Path: seed.path}); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("err = %v, want a ConflictError", err)
	}

	forced, err := h.promoteOnce(p, Request{Src: src, Dst: other, SHA256: shaA, Path: seed.path, Force: true, Actor: "admin"})
	if err != nil || len(forced.Replaced) != 1 || len(forced.Copied) != 1 {
		t.Fatalf("forced = %+v, %v", forced, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files
		WHERE repository_id = $1 AND blob_id = $2 AND deleted_at IS NOT NULL`, other.ID, blobB); n != 1 {
		t.Errorf("%d replaced files in the trash, want 1", n)
	}
}

// promoteFiles runs a promotion of the item sha/path from src into dst.
func promoteFiles(t *testing.T, h *harness, src, dst *models.Repository, sha, path string, force bool) (*Result, error) {
	t.Helper()

	return h.promoteOnce(promoterFor(t, src.Format), Request{Src: src, Dst: dst, SHA256: sha, Path: path, Force: force, Actor: "admin"})
}

func tagsOf(h *harness, repoID, pkg string) string {
	h.t.Helper()

	return h.str(`SELECT COALESCE(string_agg(t.tag || '=' || t.version, ',' ORDER BY t.tag), '')
		FROM package_tags t JOIN packages p ON p.id = t.package_id WHERE p.repository_id = $1 AND p.name = $2`, repoID, pkg)
}

// TestPromoteNPMCopiesTagsAndMarksThePackumentStale: the dist-tags naming a
// copied version travel with it, a destination without a latest tag gets one,
// the source's worker state is not copied and a package refresh is requested.
func TestPromoteNPMCopiesTagsAndMarksThePackumentStale(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	src, dst := h.repo("npm", false), h.repo("npm", false)
	blob24, sha24 := h.blob("shop 2.4.0")
	blob30, sha30 := h.blob("shop 3.0.0")
	attrs := `{"scope": "acme", "latest_version": "3.0.0", "packument_stale": false}`

	for _, v := range []struct{ version, blob string }{{"2.4.0", blob24}, {"3.0.0", blob30}} {
		h.file(src.ID, v.blob, fileSeed{
			pkg: "@acme/shop", version: v.version, pkgAttrs: attrs, path: "@acme/shop/-/shop-" + v.version + ".tgz",
			unitKey: "@acme/shop@" + v.version,
		})
	}
	h.exec(`INSERT INTO package_tags (package_id, tag, version)
		SELECT id, t.tag, t.v FROM packages, (VALUES ('beta', '2.4.0'), ('next', '3.0.0'), ('latest', '3.0.0')) t(tag, v)
		WHERE repository_id = $1`, src.ID)

	res, err := promoteFiles(t, h, src, dst, sha24, "", false)
	if err != nil || len(res.Copied) != 1 {
		t.Fatalf("promote 2.4.0 = %+v, %v", res, err)
	}

	if got := tagsOf(h, dst.ID, "@acme/shop"); got != "beta=2.4.0,latest=2.4.0" {
		t.Errorf("destination tags = %q, want beta=2.4.0,latest=2.4.0 (next names a version that was not copied)", got)
	}

	if got := h.str(`SELECT attrs->>'latest_version' || '/' || (attrs->>'packument_stale') || '/' || (attrs->>'scope')
		FROM packages WHERE repository_id = $1`, dst.ID); got != "2.4.0/true/acme" {
		t.Errorf("destination attrs = %q, want latest_version 2.4.0, stale, the scope kept", got)
	}

	if len(res.Regen) != 1 || res.Regen[0].Format != "npm" || res.Regen[0].PackageID == "" {
		t.Errorf("regen = %+v, want one npm package refresh", res.Regen)
	}

	// The next version brings its tag; the destination keeps its latest.
	if _, err := promoteFiles(t, h, src, dst, sha30, "", false); err != nil {
		t.Fatalf("promote 3.0.0: %v", err)
	}

	if got := tagsOf(h, dst.ID, "@acme/shop"); got != "beta=2.4.0,latest=2.4.0,next=3.0.0" {
		t.Errorf("destination tags = %q, want latest kept at 2.4.0 and next added", got)
	}

	// A retry copies nothing and asks for no refresh.
	again, err := promoteFiles(t, h, src, dst, sha24, "", false)
	if err != nil || len(again.Copied) != 0 || len(again.Existing) != 1 || len(again.Regen) != 0 {
		t.Errorf("retry = %+v, %v", again, err)
	}

	// Another tarball in the destination version is a collision, not a sibling.
	other := h.repo("npm", false)
	h.file(other.ID, blob30, fileSeed{pkg: "@acme/shop", version: "2.4.0", path: "@acme/shop/-/other-2.4.0.tgz"})

	if _, err := promoteFiles(t, h, src, other, sha24, "", false); !errors.As(err, new(*ConflictError)) {
		t.Errorf("err = %v, want a ConflictError for the occupied version", err)
	}
}

// TestPromoteGoCopiesTheWholeVersion: a module is a zip and its go.mod; both
// are copied, only the zip is reported and charged, and a differing go.mod in
// the destination is a conflict even though the zip matches.
func TestPromoteGoCopiesTheWholeVersion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	src, dst := h.repo("go", false), h.repo("go", false)
	zip, zipSHA := h.blob("zip")
	mod, _ := h.blob("module example.com/shop")
	otherMod, _ := h.blob("module example.com/other")

	seed := fileSeed{pkg: "example.com/shop", version: "v1.2.0", path: "example.com/shop/@v/v1.2.0.zip"}
	modSeed := fileSeed{pkg: "example.com/shop", version: "v1.2.0", path: "example.com/shop/@v/v1.2.0.mod", role: "metadata"}
	h.file(src.ID, zip, seed)
	h.file(src.ID, mod, modSeed)

	res, err := promoteFiles(t, h, src, dst, zipSHA, "", false)
	wantBytes := int64(len("zip") + len("module example.com/shop"))

	if err != nil || len(res.Copied) != 1 || res.Usage.Artifacts != 1 || res.Usage.Bytes != wantBytes {
		t.Fatalf("promote = %+v, %v", res, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL`, dst.ID); n != 2 {
		t.Errorf("destination holds %d files, want the zip and the go.mod", n)
	}

	if again, err := promoteFiles(t, h, src, dst, zipSHA, "", false); err != nil || len(again.Copied) != 0 || len(again.Existing) != 1 {
		t.Errorf("retry = %+v, %v", again, err)
	}

	clash := h.repo("go", false)
	h.file(clash.ID, zip, seed)
	h.file(clash.ID, otherMod, modSeed)

	if _, err := promoteFiles(t, h, src, clash, zipSHA, "", false); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("err = %v, want a ConflictError for the differing go.mod", err)
	}

	forced, err := promoteFiles(t, h, src, clash, zipSHA, "", true)
	if err != nil || len(forced.Replaced) != 1 || len(forced.Existing) != 1 {
		t.Fatalf("forced = %+v, %v", forced, err)
	}

	if got := h.str(`SELECT b.sha256 FROM package_files f JOIN blobs b ON b.id = f.blob_id
		WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.path = $2`, clash.ID, modSeed.path); got != contentSHA("module example.com/shop") {
		t.Errorf("live go.mod digest = %q, want the source's", got)
	}
}

func contentSHA(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

// TestPromoteConanCopiesRecipeAndPackagesWithTheirSideRows: a conan version is
// all its files; the destination version may already hold other files.
func TestPromoteConanCopiesRecipeAndPackagesWithTheirSideRows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	src, dst := h.repo("conan", false), h.repo("conan", false)
	recipe, recipeSHA := h.blob("conanfile.py")
	bin, _ := h.blob("conan_package.tgz")
	extra, _ := h.blob("conanmanifest.txt")

	key := "1.0@_/_#rev1"
	rf := h.file(src.ID, recipe, fileSeed{pkg: "shop", version: "1.0", versionKey: key, path: "shop/1.0/_/_/rev1/export/conanfile.py"})
	bf := h.file(src.ID, bin, fileSeed{
		pkg: "shop", version: "1.0", versionKey: key, path: "shop/1.0/_/_/rev1/package/abc/prev1/conan_package.tgz", role: "binary",
	})
	h.exec(`INSERT INTO conan_files (id, package_id, package_revision) VALUES ($1, '', ''), ($2, 'abc', 'prev1')`, rf, bf)
	h.file(dst.ID, extra, fileSeed{
		pkg: "shop", version: "1.0", versionKey: key, path: "shop/1.0/_/_/rev1/export/conanmanifest.txt", role: "metadata",
	})

	res, err := promoteFiles(t, h, src, dst, recipeSHA, "", false)
	if err != nil || len(res.Copied) != 1 {
		t.Fatalf("promote = %+v, %v", res, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files f JOIN conan_files c ON c.id = f.id
		WHERE f.repository_id = $1 AND (c.package_id, c.package_revision) IN (('', ''), ('abc', 'prev1'))`, dst.ID); n != 2 {
		t.Errorf("destination holds %d copied files with side rows, want 2", n)
	}

	if n := h.count(`SELECT COUNT(DISTINCT version_id) FROM package_files WHERE repository_id = $1`, dst.ID); n != 1 {
		t.Errorf("destination files live in %d versions, want the one they joined", n)
	}

	if again, err := promoteFiles(t, h, src, dst, recipeSHA, "", false); err != nil || len(again.Copied) != 0 {
		t.Errorf("retry = %+v, %v", again, err)
	}
}

func seedRPM(h *harness, repoID, blobID, path string) string {
	id := h.file(repoID, blobID, fileSeed{
		pkg: "shop", version: "2.4.0", versionKey: "2.4.0-3.el9:2|x86_64", filename: "shop-2.4.0-3.el9.x86_64.rpm", path: path,
		arch: "x86_64", versionMeta: `{"license": "MIT"}`,
	})
	h.exec(`INSERT INTO rpm_files (id, header_start, header_end, rpm_extra) VALUES ($1, 280, 4096, '{"requires": ["libc.so.6"]}')`, id)

	return id
}

// TestPromoteRPMCollidesOnTheDownloadNameAndMarksRepodataStale: rpm resolves a
// download by file name, so another file with the name collides whatever its
// path; the copy carries rpm_extra and marks the destination's repodata stale.
func TestPromoteRPMCollidesOnTheDownloadNameAndMarksRepodataStale(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	src, dst := h.repo("rpm", false), h.repo("rpm", false)
	blob, sha := h.blob("rpm A")
	otherBlob, _ := h.blob("rpm B")
	seedRPM(h, src.ID, blob, "Packages/shop-2.4.0-3.el9.x86_64.rpm")

	// The destination has repodata that was fresh.
	h.exec(`INSERT INTO rpm_repos (repository_id, base_arch, metadata_stale) VALUES ($1, 'x86_64', false)`, dst.ID)

	res, err := promoteFiles(t, h, src, dst, sha, "", false)
	if err != nil || len(res.Copied) != 1 {
		t.Fatalf("promote = %+v, %v", res, err)
	}

	if len(res.Regen) != 1 || res.Regen[0].Format != "rpm" || res.Regen[0].RepoID != dst.ID {
		t.Errorf("regen = %+v, want one rpm repository refresh", res.Regen)
	}

	if n := h.count(`SELECT COUNT(*) FROM rpm_repos WHERE repository_id = $1 AND metadata_stale`, dst.ID); n != 1 {
		t.Errorf("destination has %d stale rpm_repos rows, want its one", n)
	}

	if n := h.count(`SELECT COUNT(*) FROM rpm_files r JOIN package_files f ON f.id = r.id
		WHERE f.repository_id = $1 AND r.header_end = 4096 AND r.rpm_extra @> '{"requires": ["libc.so.6"]}'`, dst.ID); n != 1 {
		t.Errorf("destination has %d rpm side rows copied as is, want 1", n)
	}

	// A repository without repodata row gets a stale one.
	fresh := h.repo("rpm", false)
	if _, err := promoteFiles(t, h, src, fresh, sha, "", false); err != nil {
		t.Fatalf("promote into a repository without repodata: %v", err)
	}

	if n := h.count(`SELECT COUNT(*) FROM rpm_repos WHERE repository_id = $1 AND metadata_stale`, fresh.ID); n != 1 {
		t.Errorf("repository without rpm_repos has %d stale rows after the copy, want 1", n)
	}

	// The same file name under another path with other content collides.
	clash := h.repo("rpm", false)
	seedRPM(h, clash.ID, otherBlob, "other/shop-2.4.0-3.el9.x86_64.rpm")

	if _, err := promoteFiles(t, h, src, clash, sha, "", false); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("err = %v, want a ConflictError for the file name", err)
	}

	forced, err := promoteFiles(t, h, src, clash, sha, "", true)
	if err != nil || len(forced.Replaced) != 1 || len(forced.Copied) != 1 {
		t.Fatalf("forced = %+v, %v", forced, err)
	}
}

// TestPromoteDebRebuildsTheDistributionGraphInTheDestination: the copy creates
// the destination's own distribution, component and architecture, and points
// the file, its version key, partition and side row at them.
func TestPromoteDebRebuildsTheDistributionGraphInTheDestination(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	src, dst := h.repo("deb", false), h.repo("deb", false)
	blob, sha := h.blob("deb A")

	distID := h.str(`INSERT INTO debian_distributions (repository_id, name, codename, description)
		VALUES ($1, 'bookworm', 'Bookworm', 'the stable distribution') RETURNING id::text`, src.ID)
	compID := h.str(`INSERT INTO debian_components (distribution_id, name, description)
		VALUES ($1, 'main', 'the main component') RETURNING id::text`, distID)
	h.exec(`INSERT INTO debian_architectures (distribution_id, name) VALUES ($1, 'amd64')`, distID)

	id := h.file(src.ID, blob, fileSeed{
		pkg: "libshop6", version: "1:2.4.0-1", versionKey: "1:2.4.0-1|amd64|" + compID,
		path: "pool/main/libs/libshop6/libshop6_2.4.0-1_amd64.deb", filename: "libshop6_2.4.0-1_amd64.deb",
		arch: "amd64", part: distID,
	})
	h.exec(`INSERT INTO deb_files (id, distribution_id, component_id, control_fields) VALUES ($1, $2, $3, '{"Depends": "libc6"}')`, id, distID, compID)

	res, err := promoteFiles(t, h, src, dst, sha, "", false)
	if err != nil || len(res.Copied) != 1 || len(res.Regen) != 1 || res.Regen[0].Format != "deb" {
		t.Fatalf("promote = %+v, %v", res, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM debian_distributions
		WHERE repository_id = $1 AND name = 'bookworm' AND codename = 'Bookworm' AND description = 'the stable distribution'`, dst.ID); n != 1 {
		t.Errorf("destination has %d matching distributions, want a copy of the source's", n)
	}

	if n := h.count(`SELECT COUNT(*) FROM debian_components c JOIN debian_distributions d ON d.id = c.distribution_id
		WHERE d.repository_id = $1 AND c.name = 'main' AND c.description = 'the main component'`, dst.ID); n != 1 {
		t.Errorf("destination has %d matching components, want 1", n)
	}

	if n := h.count(`SELECT COUNT(*) FROM debian_architectures a JOIN debian_distributions d ON d.id = a.distribution_id
		WHERE d.repository_id = $1 AND a.name = 'amd64'`, dst.ID); n != 1 {
		t.Errorf("destination has %d amd64 architectures, want 1", n)
	}

	// The file, its version key, partition and side row all use the destination's ids.
	if n := h.count(`SELECT COUNT(*) FROM package_files f
		JOIN package_versions v ON v.id = f.version_id
		JOIN deb_files x ON x.id = f.id
		JOIN debian_distributions d ON d.id = x.distribution_id AND d.repository_id = f.repository_id
		JOIN debian_components c ON c.id = x.component_id AND c.distribution_id = d.id
		WHERE f.repository_id = $1 AND f.part = d.id::text AND v.version_key = '1:2.4.0-1|amd64|' || c.id::text
		  AND x.control_fields->>'Depends' = 'libc6'`, dst.ID); n != 1 {
		t.Errorf("%d destination files reference the destination's own graph, want 1", n)
	}

	again, err := promoteFiles(t, h, src, dst, sha, "", false)
	if err != nil || len(again.Copied) != 0 || len(again.Existing) != 1 || len(again.Regen) != 0 {
		t.Errorf("retry = %+v, %v", again, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM debian_distributions WHERE repository_id = $1`, dst.ID); n != 1 {
		t.Errorf("the retry left %d distributions, want 1", n)
	}
}

// TestPromoteExtractsLicensesPerFormat: the licenses a version declares come
// out of its metadata whichever shape the format stores them in.
func TestPromoteExtractsLicensesPerFormat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		format, meta string
		want         []string
	}{
		{"pypi", `{"license": "MIT"}`, []string{"MIT"}},
		{"cargo", `{"license": "Apache-2.0"}`, []string{"Apache-2.0"}},
		{"cargo", `{"license": ""}`, nil},
		{"rubygems", `{"licenses": ["MIT", "BSD-3-Clause"]}`, []string{"MIT", "BSD-3-Clause"}},
		{"pacman", `{"licenses": ["GPL-3.0"]}`, []string{"GPL-3.0"}},
		{"apk", `{"license": "MPL-2.0"}`, []string{"MPL-2.0"}},
		{"composer", `{"composer_json": {"license": "MIT"}}`, []string{"MIT"}},
		{"composer", `{"composer_json": {"license": ["MIT", "GPL-2.0"]}}`, []string{"MIT", "GPL-2.0"}},
		{"composer", `{"composer_json": {}}`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.format+tc.meta, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			src, dst := h.repo(tc.format, false), h.repo(tc.format, false)
			blob, sha := h.blob("content " + tc.format + tc.meta)
			h.file(src.ID, blob, fileSeed{pkg: "shop", version: "1.0", path: "shop-1.0.pkg", versionMeta: tc.meta})

			res, err := promoteFiles(t, h, src, dst, sha, "", false)
			if err != nil {
				t.Fatalf("promote: %v", err)
			}

			if !slices.Equal(res.Licenses, tc.want) {
				t.Errorf("licenses = %v, want %v", res.Licenses, tc.want)
			}
		})
	}
}

// TestPromoteIgnoresOtherRepositoriesAndTrashedSources: content that lives in
// another repository, or only in the source's trash, is not promotable, and a
// trashed destination file is no obstacle.
func TestPromoteIgnoresOtherRepositoriesAndTrashedSources(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	holder, empty, dst := h.repo("maven", false), h.repo("maven", false), h.repo("maven", false)
	blob, sha := h.blob("jar")
	seed := fileSeed{pkg: "com.acme:shop", version: "1.0.0", path: "com/acme/shop/1.0.0/shop-1.0.0.jar"}
	h.file(holder.ID, blob, seed)

	if _, err := promoteFiles(t, h, empty, dst, sha, "", false); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("content of another repository: err = %v, want ErrItemNotFound", err)
	}

	// A trashed destination file at the path does not block the copy.
	other, _ := h.blob("old jar")
	h.file(dst.ID, other, seed)
	h.exec(`UPDATE package_files SET deleted_at = now() WHERE repository_id = $1`, dst.ID)
	h.exec(`UPDATE package_versions SET deleted_at = now() WHERE repository_id = $1`, dst.ID)

	if res, err := promoteFiles(t, h, holder, dst, sha, "", false); err != nil || len(res.Copied) != 1 {
		t.Fatalf("promote over a trashed file = %+v, %v", res, err)
	}

	h.exec(`UPDATE package_files SET deleted_at = now() WHERE repository_id = $1`, holder.ID)

	if _, err := promoteFiles(t, h, holder, dst, sha, "", false); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("trashed source: err = %v, want ErrItemNotFound", err)
	}
}
