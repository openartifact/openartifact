// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"crypto/md5"
	"crypto/sha1"
	"fmt"
)

// npmTrashView exposes npm_versions (which has no repository_id of its own) as
// a single-table updatable view keyed by repository, so the generic cases can
// count and soft-delete a repository's versions like those of any other table.
const npmTrashView = "npm_versions_by_repo"

//nolint:unqueryvet // test-only view exposing every npm_versions column
const npmTrashViewDDL = `CREATE OR REPLACE VIEW ` + npmTrashView + ` AS
	SELECT v.*, (SELECT p.repository_id FROM npm_packages p WHERE p.id = v.package_id) AS repository_id
	  FROM npm_versions v`

// npmSeedPackage gets or creates the @acme/shop package of repoID.
func npmSeedPackage(h *harness, repoID, latest string) string {
	h.t.Helper()

	h.exec(npmTrashViewDDL)

	return h.str(`INSERT INTO npm_packages (repository_id, scope, name, full_name, latest_version, packument_stale)
		VALUES ($1::uuid, '@acme', 'shop', '@acme/shop', $2, false)
		ON CONFLICT (repository_id, full_name) DO UPDATE SET full_name = EXCLUDED.full_name
		RETURNING id::text`, repoID, latest)
}

// npmSeedVersion adds a live version whose tarball is blobID.
func npmSeedVersion(h *harness, pkgID, blobID, version string) {
	h.t.Helper()

	manifest := fmt.Sprintf(`{"name":"@acme/shop","version":%q,"description":"a shop","license":"MIT","dist":{"tarball":"http://x/shop.tgz"}}`, version)

	h.exec(`INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id)
		VALUES ($1::uuid, $2, $3::jsonb, $4, $5, $6, $7::uuid)`,
		pkgID, version, manifest, "sha512-"+version, "sha1-"+version, "shop-"+version+".tgz", blobID)
}

// npmSeedTag points a dist-tag of the package at version.
func npmSeedTag(h *harness, pkgID, tag, version string) {
	h.t.Helper()

	h.exec(`INSERT INTO npm_dist_tags (package_id, tag, version) VALUES ($1::uuid, $2, $3)
		ON CONFLICT (package_id, tag) DO NOTHING`, pkgID, tag, version)
}

// npmFixture is a 1.2.0 version of @acme/shop with a "beta" dist-tag and no
// "latest" tag: the copy must add one.
func npmFixture() fixture {
	return fixture{
		format: "npm", version: "1.2.0", path: "shop-1.2.0.tgz",
		tables:  []string{npmTrashView},
		compare: []string{"npm_packages"},
		// packument_stale is true in the target (it needs a refresh), false in the seeded source.
		ignore: []string{"packument_stale"},
		seed: func(h *harness, repo, blob string) {
			pkg := npmSeedPackage(h, repo, "1.2.0")
			npmSeedVersion(h, pkg, blob, "1.2.0")
			npmSeedTag(h, pkg, "beta", "1.2.0")
		},
		verify: func(h *harness, src, dst string) {
			const ignored = `'{id,package_id,created_at,updated_at,deleted_at,deleted_by}'::text[]`

			if n := h.count(`SELECT COUNT(*) FROM npm_versions s
				JOIN npm_packages sp ON sp.id = s.package_id
				JOIN npm_versions d ON (to_jsonb(s) - `+ignored+`) = (to_jsonb(d) - `+ignored+`)
				JOIN npm_packages dp ON dp.id = d.package_id
				WHERE sp.repository_id = $1 AND dp.repository_id = $2`, src, dst); n != 1 {
				h.t.Errorf("npm_versions: %d source versions have an identical copy in the target, want 1", n)
			}

			if got := h.str(`SELECT packument_stale::text FROM npm_packages WHERE repository_id = $1`, dst); got != "true" {
				h.t.Errorf("packument_stale = %q, want true", got)
			}

			if got := h.str(`SELECT latest_version FROM npm_packages WHERE repository_id = $1`, dst); got != "1.2.0" {
				h.t.Errorf("latest_version = %q, want 1.2.0", got)
			}

			for tag, want := range map[string]int{"latest": 1, "beta": 1} {
				if n := h.count(`SELECT COUNT(*) FROM npm_dist_tags t JOIN npm_packages p ON p.id = t.package_id
					WHERE p.repository_id = $1 AND t.tag = $2 AND t.version = '1.2.0'`, dst, tag); n != want {
					h.t.Errorf("dist-tag %s -> 1.2.0: %d rows in the target, want %d", tag, n, want)
				}
			}
		},
	}
}

const (
	goFixtureModule  = "acme.test/shop"
	goFixtureModFile = "module acme.test/shop\n\ngo 1.22\n"
)

// goSeedVersion adds a module version whose zip is zipBlobID; every version
// shares the same go.mod blob.
func goSeedVersion(h *harness, repoID, zipBlobID, version string) string {
	h.t.Helper()

	modBlob, _ := h.blob(goFixtureModFile)

	return h.row("gomod_modules", repoID, "zip_blob_id", zipBlobID, map[string]any{
		"module_path": goFixtureModule, "version": version, "go_mod_content": goFixtureModFile, "mod_blob_id": modBlob,
	})
}

// goFixture is a v1.4.0 module version with a zip and a go.mod blob.
func goFixture() fixture {
	return fixture{
		format: "go", version: "v1.4.0", path: "v1.4.0.zip",
		tables: []string{"gomod_modules"}, compare: []string{"gomod_modules"},
		seed: func(h *harness, repo, blob string) { goSeedVersion(h, repo, blob, "v1.4.0") },
		verify: func(h *harness, src, dst string) {
			if n := h.count(`SELECT COUNT(*) FROM gomod_modules s
				JOIN gomod_modules d ON d.zip_blob_id = s.zip_blob_id AND d.mod_blob_id = s.mod_blob_id
				WHERE s.repository_id = $1 AND d.repository_id = $2 AND d.deleted_at IS NULL`, src, dst); n != 1 {
				h.t.Errorf("target rows referencing both of the source's blobs = %d, want 1", n)
			}
		},
	}
}

// conanSeedFile is one file row of a seeded recipe revision.
type conanSeedFile struct {
	packageID, packageRevision, filename string
	// content is the file's blob content; "" stores the row without a blob.
	content string
	// conanfile and conaninfo fill the legacy text columns.
	conanfile, conaninfo string
	// item stores the file as the revision's item blob.
	item bool
}

// conanFixtureFiles are the files of recipe revision rrev: the recipe (its
// manifest is the item blob, plus a metadata-only row) and a binary package
// revision beneath it.
func conanFixtureFiles(rrev string) []conanSeedFile {
	return []conanSeedFile{
		{filename: "conanfile.py", content: "class Shop " + rrev, conanfile: "class Shop: pass"},
		{filename: "conanmanifest.txt", item: true},
		{filename: "conandata.yml"},
		{packageID: "pkg1", packageRevision: "prev1", filename: "conaninfo.txt", content: "[settings] " + rrev, conaninfo: "[settings]\nos=Linux"},
		{packageID: "pkg1", packageRevision: "prev1", filename: "conanmanifest.txt", content: "package manifest " + rrev},
		{packageID: "pkg1", packageRevision: "prev1", filename: "conan_package.tgz", content: "package tgz " + rrev},
	}
}

// conanSeedFiles inserts files as live rows of recipe revision rrev of
// shop/1.0.0@acme/stable; the item file uses itemBlobID.
func conanSeedFiles(h *harness, repoID, rrev, itemBlobID string, files []conanSeedFile) {
	h.t.Helper()

	for _, f := range files {
		cols := map[string]any{
			"name": "shop", "version": "1.0.0", "user_channel": "acme/stable", "recipe_revision": rrev,
			"package_id": f.packageID, "package_revision": f.packageRevision, "filename": f.filename,
		}

		// The Conan 1 checksums (migration 057) must travel with the row.
		cols["md5"] = fmt.Sprintf("%x", md5.Sum([]byte(f.filename+rrev)))
		cols["sha1"] = fmt.Sprintf("%x", sha1.Sum([]byte(f.filename+rrev)))

		if f.conanfile != "" {
			cols["conanfile"] = f.conanfile
		}

		if f.conaninfo != "" {
			cols["conaninfo"] = f.conaninfo
		}

		blobCol, blobID := "", ""

		switch {
		case f.item:
			blobCol, blobID = "blob_id", itemBlobID
		case f.content != "":
			blobCol = "blob_id"
			blobID, _ = h.blob(f.content)
		}

		h.row("conan_packages", repoID, blobCol, blobID, cols)
	}
}

// conanLatestRevision is the recipe revision conan serves as latest in repoID:
// the one whose manifest row is the newest.
func conanLatestRevision(h *harness, repoID string) string {
	h.t.Helper()

	return h.str(`SELECT recipe_revision FROM conan_packages
		WHERE repository_id = $1 AND package_id = '' AND filename = 'conanmanifest.txt' AND deleted_at IS NULL
		ORDER BY updated_at DESC, recipe_revision DESC LIMIT 1`, repoID)
}

// conanFixture is one recipe revision with its binary package revision; the
// item is the recipe's conanmanifest.txt and the promotion copies every file.
func conanFixture() fixture {
	return fixture{
		format: "conan", version: "1.0.0", path: "rrev-new/conanmanifest.txt",
		tables: []string{"conan_packages"}, compare: []string{"conan_packages"},
		copies: len(conanFixtureFiles("rrev-new")),
		seed: func(h *harness, repo, blob string) {
			conanSeedFiles(h, repo, "rrev-new", blob, conanFixtureFiles("rrev-new"))
		},
		verify: func(h *harness, src, dst string) {
			if got := conanLatestRevision(h, dst); got != "rrev-new" {
				h.t.Errorf("latest revision in the target = %q, want the copied rrev-new", got)
			}
		},
	}
}
