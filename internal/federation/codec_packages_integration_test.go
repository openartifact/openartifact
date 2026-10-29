// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"database/sql"
	"slices"
	"testing"
)

const (
	npmScopedPkg  = "@acme/widget"
	npmPlainPkg   = "plain"
	npmVerKey     = npmScopedPkg + "@1.0.0"
	npmVerKey2    = npmScopedPkg + "@1.1.0"
	npmTagKey     = npmScopedPkg + "#latest"
	npmTagNext    = npmScopedPkg + "#next"
	npmPlainVer   = npmPlainPkg + "@2.0.0"
	npmPlainTag   = npmPlainPkg + "#latest"
	npmManifestV1 = `{"name": "@acme/widget", "version": "1.0.0", "description": "widget"}`
)

// pkgDB runs the statements of the generic-codec tests and fails the test on error.
type pkgDB struct {
	t  *testing.T
	db *sql.DB
}

func (p pkgDB) exec(query string, args ...any) {
	p.t.Helper()

	if _, err := p.db.ExecContext(context.Background(), query, args...); err != nil {
		p.t.Fatalf("exec: %v\n%s", err, query)
	}
}

func (p pkgDB) scalar(query string, args ...any) int64 {
	p.t.Helper()

	var n int64
	if err := p.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		p.t.Fatalf("scalar: %v\n%s", err, query)
	}

	return n
}

func (p pkgDB) text(query string, args ...any) string {
	p.t.Helper()

	var s sql.NullString
	if err := p.db.QueryRowContext(context.Background(), query, args...).Scan(&s); err != nil {
		p.t.Fatalf("text: %v\n%s", err, query)
	}

	return s.String
}

// seedPackage inserts a package, one version and its files; each file is
// {path, blob content, role, unit key ("" for none)}.
type seedFile struct {
	path, content, role, unitKey string
	listed                       bool
	attrs                        string
}

func (p pkgDB) seedVersion(repoID, format, name, nameKey, attrs, version, metadata string, files ...seedFile) {
	p.t.Helper()

	p.exec(`INSERT INTO packages (repository_id, format, name, name_key, attrs) VALUES ($1, $2, $3, $4, $5::jsonb)
	        ON CONFLICT (repository_id, name_key) DO NOTHING`, repoID, format, name, nameKey, attrs)
	p.exec(`INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata)
	        SELECT id, repository_id, $3, $3, $4::jsonb FROM packages WHERE repository_id = $1 AND name_key = $2`,
		repoID, nameKey, version, metadata)

	for _, f := range files {
		blobID, _ := seedBlob(p.t, p.db, f.content)

		p.exec(`INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, role, listed, attrs, unit_key)
		        SELECT v.id, $1, $3, $3, $4::uuid, $5, $6, $7::jsonb, NULLIF($8, '')
		          FROM package_versions v JOIN packages p ON p.id = v.package_id
		         WHERE p.repository_id = $1 AND p.name_key = $2 AND v.version_key = $9 AND v.deleted_at IS NULL`,
			repoID, nameKey, f.path, blobID, f.role, f.listed, f.attrs, f.unitKey, version)
	}
}

// seedNPMContent writes two packages (scoped with two versions and two tags,
// unscoped with one version and one tag) into repoID the way the npm store does:
// the version unit key is on the tarball, the tag unit key on package_tags.
func seedNPMContent(t *testing.T, db *sql.DB, repoID string) {
	t.Helper()

	p := pkgDB{t: t, db: db}
	attrs := `{"scope": "@acme", "latest_version": "1.0.0", "packument_stale": false}`

	file := func(pkg, version, content string) seedFile {
		return seedFile{
			path: pkg + "/-/" + version + ".tgz", content: content, role: "primary", unitKey: pkg + "@" + version, listed: true,
			attrs: `{"shasum": "sha1-` + version + `"}`,
		}
	}

	p.seedVersion(
		repoID,
		"npm",
		npmScopedPkg,
		npmScopedPkg,
		attrs,
		"1.0.0",
		npmManifestV1,
		file(npmScopedPkg, "1.0.0", "tarball widget 1.0.0"),
	)
	p.seedVersion(repoID, "npm", npmScopedPkg, npmScopedPkg, attrs, "1.1.0",
		`{"name": "@acme/widget", "version": "1.1.0"}`, file(npmScopedPkg, "1.1.0", "tarball widget 1.1.0"))
	p.seedVersion(repoID, "npm", npmPlainPkg, npmPlainPkg, `{"scope": ""}`, "2.0.0",
		`{"name": "plain", "version": "2.0.0"}`, file(npmPlainPkg, "2.0.0", "tarball plain 2.0.0"))

	tag := func(pkg, tag, version string) {
		p.exec(`INSERT INTO package_tags (package_id, tag, version, unit_key)
		        SELECT id, $3, $4, $2 || '#' || $3 FROM packages WHERE repository_id = $1 AND name_key = $2`, repoID, pkg, tag, version)
	}

	tag(npmScopedPkg, "latest", "1.0.0")
	tag(npmScopedPkg, "next", "1.1.0")
	tag(npmPlainPkg, "latest", "2.0.0")
}

// replicateUnits exports each key at src and imports it at dst in a transaction,
// returning the changes by key. A blob a unit names is created at dst.
func replicateUnits(t *testing.T, src, dst pkgDB, srcRepo, dstRepo string, codec Codec, keys ...string) map[string]*Change {
	t.Helper()

	ctx := context.Background()
	out := map[string]*Change{}

	for _, key := range keys {
		content, err := codec.Export(ctx, src.db, srcRepo, key)
		if err != nil || content == nil {
			t.Fatalf("export %s = %v, %v", key, content, err)
		}

		blobIDs := map[string]string{}

		for _, b := range content.Blobs {
			var id string

			err := dst.db.QueryRowContext(ctx,
				`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1::text, $2, 'blobs/' || $1::text)
				 ON CONFLICT (sha256) DO UPDATE SET size_bytes = EXCLUDED.size_bytes RETURNING id::text`, b.SHA256, b.Size).Scan(&id)
			if err != nil {
				t.Fatalf("create blob %s: %v", b.SHA256, err)
			}

			blobIDs[b.SHA256] = id
		}

		tx, err := dst.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}

		ch, err := codec.Import(ctx, tx, dstRepo, key, content, blobIDs)
		if err != nil {
			_ = tx.Rollback()

			t.Fatalf("import %s: %v", key, err)
		}

		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", key, err)
		}

		out[key] = ch
	}

	return out
}

func codecOf(t *testing.T, format string) Codec {
	t.Helper()

	registerFixtureFormats()

	c, err := CodecFor(format)
	if err != nil {
		t.Fatalf("CodecFor(%s): %v", format, err)
	}

	return c
}

func TestPackageCodecNPMReplicatesVersionsAndTags(t *testing.T) {
	src, dst := pkgDB{t: t, db: freshDB(t).GetDB()}, pkgDB{t: t, db: freshDB(t).GetDB()}
	srcRepo, dstRepo := seedRepo(t, src.db, "npm-a", "npm"), seedRepo(t, dst.db, "npm-b", "npm")
	codec := codecOf(t, "npm")

	seedNPMContent(t, src.db, srcRepo)

	// Backfill registers the three version units and the three tags.
	enableLog(t, src.db, srcRepo)

	tx, err := src.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	if err := codec.Backfill(context.Background(), tx, srcRepo); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit backfill: %v", err)
	}

	if n := src.scalar(`SELECT count(*) FROM federation_units WHERE repo_id = $1`, srcRepo); n != 6 {
		t.Fatalf("backfilled units = %d, want 6", n)
	}

	changes := replicateUnits(t, src, dst, srcRepo, dstRepo, codec, npmVerKey, npmVerKey2, npmPlainVer, npmTagKey, npmTagNext, npmPlainTag)

	if n := dst.scalar(`SELECT count(*) FROM package_versions WHERE repository_id = $1`, dstRepo); n != 3 {
		t.Fatalf("versions = %d, want 3", n)
	}

	if n := dst.scalar(`SELECT count(*) FROM package_files WHERE repository_id = $1 AND unit_key IS NOT NULL`, dstRepo); n != 3 {
		t.Fatalf("keyed files = %d, want 3", n)
	}

	if got := dst.text(`SELECT t.version FROM package_tags t JOIN packages p ON p.id = t.package_id
	                     WHERE p.repository_id = $1 AND t.unit_key = $2`, dstRepo, npmTagNext); got != "1.1.0" {
		t.Fatalf("next tag = %q, want 1.1.0", got)
	}

	// The instance-local attrs are not replicated; the stale marker is set so the packument is rebuilt.
	for attr, want := range map[string]string{"latest_version": "", "packument_stale": "true", "scope": "@acme"} {
		got := dst.text(`SELECT attrs->>$3 FROM packages WHERE repository_id = $1 AND name_key = $2`, dstRepo, npmScopedPkg, attr)
		if got != want {
			t.Fatalf("attrs.%s = %q, want %q", attr, got, want)
		}
	}

	ch := changes[npmVerKey]
	if ch.Replaced || len(ch.Regen) != 1 || ch.Regen[0].Format != "npm" || ch.Regen[0].RepoID != dstRepo || ch.Artifact == nil ||
		ch.Artifact.Name != npmScopedPkg || ch.Artifact.Version != "1.0.0" {
		t.Fatalf("version change = %+v artifact %+v", ch, ch.Artifact)
	}
}

func TestPackageCodecNPMReimportAndMetadataChange(t *testing.T) {
	src, dst := pkgDB{t: t, db: freshDB(t).GetDB()}, pkgDB{t: t, db: freshDB(t).GetDB()}
	srcRepo, dstRepo := seedRepo(t, src.db, "npm-a", "npm"), seedRepo(t, dst.db, "npm-b", "npm")
	codec := codecOf(t, "npm")

	seedNPMContent(t, src.db, srcRepo)
	replicateUnits(t, src, dst, srcRepo, dstRepo, codec, npmVerKey, npmVerKey2)

	// Re-importing the same unit replaces nothing that changed: no usage is released.
	again := replicateUnits(t, src, dst, srcRepo, dstRepo, codec, npmVerKey)[npmVerKey]
	if !again.Replaced || again.ReplacedSize != 0 || again.ReplacedCount != 0 || len(again.ReleasedBlobs) != 0 {
		t.Fatalf("re-import change = %+v", again)
	}

	// A deprecation at the source changes the metadata of the existing version at the peer.
	src.exec(`UPDATE package_versions v SET metadata = metadata || '{"deprecated": "x"}'::jsonb FROM packages p
	           WHERE p.id = v.package_id AND p.repository_id = $1 AND v.version = '1.1.0'`, srcRepo)
	replicateUnits(t, src, dst, srcRepo, dstRepo, codec, npmVerKey2)

	if n := dst.scalar(
		`SELECT count(*) FROM package_versions WHERE repository_id = $1 AND metadata->>'deprecated' = 'x'`,
		dstRepo,
	); n != 1 {
		t.Fatalf("deprecated versions = %d, want 1", n)
	}
}

func TestPackageCodecGoVersionUnitCarriesBothFiles(t *testing.T) {
	src, dst := pkgDB{t: t, db: freshDB(t).GetDB()}, pkgDB{t: t, db: freshDB(t).GetDB()}
	srcRepo, dstRepo := seedRepo(t, src.db, "go-a", "go"), seedRepo(t, dst.db, "go-b", "go")
	codec := codecOf(t, "go")
	key := "example.com/m@v1.0.0"

	src.seedVersion(
		srcRepo,
		"go",
		"example.com/m",
		"example.com/m",
		`{}`,
		"v1.0.0",
		`{}`,
		seedFile{path: "example.com/m/@v/v1.0.0.zip", content: "zip bytes", role: "primary", unitKey: key, listed: true, attrs: `{}`},
		seedFile{
			path:    "example.com/m/@v/v1.0.0.mod",
			content: "module example.com/m",
			role:    "mod",
			attrs:   `{"go_mod_content": "module example.com/m"}`,
		},
	)

	content, err := codec.Export(context.Background(), src.db, srcRepo, key)
	if err != nil || content == nil || len(content.Blobs) != 2 {
		t.Fatalf("export = %+v, %v", content, err)
	}

	usage, err := codec.Usage(context.Background(), dst.db, dstRepo, key, content)
	if err != nil ||
		!slices.Equal(usage, []Reservation{{Size: 9, Count: 1}, {Size: 20, Count: 0}}) &&
			!slices.Equal(usage, []Reservation{{Size: 20, Count: 0}, {Size: 9, Count: 1}}) {
		t.Fatalf("usage = %+v, %v", usage, err)
	}

	replicateUnits(t, src, dst, srcRepo, dstRepo, codec, key)

	if n := dst.scalar(`SELECT count(*) FROM package_files WHERE repository_id = $1`, dstRepo); n != 2 {
		t.Fatalf("files = %d, want 2", n)
	}

	if got := dst.text(`SELECT unit_key FROM package_files WHERE repository_id = $1 AND role = 'mod'`, dstRepo); got != "" {
		t.Fatalf("mod file carries unit key %q", got)
	}

	if got := dst.text(
		`SELECT attrs->>'go_mod_content' FROM package_files WHERE repository_id = $1 AND role = 'mod'`,
		dstRepo,
	); got != "module example.com/m" {
		t.Fatalf("go_mod_content = %q", got)
	}

	// Soft delete trashes both files and the version; the unit is gone for Export.
	tx, err := dst.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	del, err := codec.Delete(context.Background(), tx, dstRepo, key, true, "federation:test")
	if err != nil || del.Missing || del.RemovedCount != 1 || del.RemovedSize != 29 {
		t.Fatalf("soft delete = %+v, %v", del, err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if n := dst.scalar(`SELECT count(*) FROM package_files WHERE repository_id = $1 AND deleted_by = 'federation:test'`, dstRepo); n != 2 {
		t.Fatalf("trashed files = %d, want 2", n)
	}

	if n := dst.scalar(`SELECT count(*) FROM package_versions WHERE repository_id = $1 AND deleted_at IS NOT NULL`, dstRepo); n != 1 {
		t.Fatalf("trashed versions = %d, want 1", n)
	}

	if c, err := codec.Export(context.Background(), dst.db, dstRepo, key); err != nil || c != nil {
		t.Fatalf("export of a trashed unit = %+v, %v", c, err)
	}
}

func TestPackageCodecMavenFileUnitReplicatesSideRow(t *testing.T) {
	src, dst := pkgDB{t: t, db: freshDB(t).GetDB()}, pkgDB{t: t, db: freshDB(t).GetDB()}
	srcRepo, dstRepo := seedRepo(t, src.db, "mvn-a", "maven"), seedRepo(t, dst.db, "mvn-b", "maven")
	codec := codecOf(t, "maven")
	path := "org/acme/lib/1.0/lib-1.0.jar"

	src.seedVersion(srcRepo, "maven", "org.acme:lib", "org.acme:lib", `{"group_id": "org.acme", "artifact_id": "lib"}`, "1.0", `{}`,
		seedFile{path: path, content: "jar", role: "primary", unitKey: path, listed: true, attrs: `{}`})
	src.exec(
		`INSERT INTO maven_files (id, classifier, extension, sha1) SELECT id, '', 'jar', 'abc' FROM package_files WHERE repository_id = $1`,
		srcRepo,
	)

	changes := replicateUnits(t, src, dst, srcRepo, dstRepo, codec, path)

	if got := dst.text(
		`SELECT m.sha1 FROM maven_files m JOIN package_files f ON f.id = m.id WHERE f.repository_id = $1`,
		dstRepo,
	); got != "abc" {
		t.Fatalf("maven_files.sha1 = %q, want abc", got)
	}

	regen := changes[path].Regen
	if len(regen) != 1 || regen[0].Attr("group_id") != "org.acme" || regen[0].Attr("artifact_id") != "lib" {
		t.Fatalf("regen = %+v", regen)
	}

	// The replacement of the blob at the same path is reported for the quota and the blob release.
	src.exec(
		`UPDATE package_files SET blob_id = (SELECT id FROM blobs WHERE sha256 = $2) WHERE repository_id = $1`,
		srcRepo,
		mustSHA(t, src.db, "jar 2"),
	)
	changed := replicateUnits(t, src, dst, srcRepo, dstRepo, codec, path)[path]

	if !changed.Replaced || changed.ReplacedSize != 3 || changed.ReplacedCount != 1 || len(changed.ReleasedBlobs) != 1 {
		t.Fatalf("replace change = %+v", changed)
	}

	if n := dst.scalar(
		`SELECT count(*) FROM maven_files m JOIN package_files f ON f.id = m.id WHERE f.repository_id = $1`,
		dstRepo,
	); n != 1 {
		t.Fatalf("side rows after replace = %d, want 1", n)
	}

	// Without the blob a unit is refused.
	content, err := codec.Export(context.Background(), src.db, srcRepo, path)
	if err != nil || content == nil {
		t.Fatalf("export = %v, %v", content, err)
	}

	tx, err := dst.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := codec.Import(context.Background(), tx, dstRepo, path, content, map[string]string{}); err == nil {
		t.Fatal("import without its blob succeeded")
	}
}

// mustSHA seeds a blob and returns its sha256.
func mustSHA(t *testing.T, db *sql.DB, content string) string {
	t.Helper()

	_, sha := seedBlob(t, db, content)

	return sha
}

func TestPackageCodecDeleteOfUnknownUnitIsMissing(t *testing.T) {
	db := pkgDB{t: t, db: freshDB(t).GetDB()}
	repo := seedRepo(t, db.db, "raw-a", "raw")

	for _, format := range []string{"raw", "npm"} {
		codec := codecOf(t, format)

		tx, err := db.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}

		ch, err := codec.Delete(context.Background(), tx, repo, "nothing", true, "x")
		_ = tx.Rollback()

		if err != nil || !ch.Missing {
			t.Fatalf("%s: delete of an unknown unit = %+v, %v", format, ch, err)
		}
	}
}

func TestPackageCodecNPMDeleteRemovesTagsAndEmptyPackage(t *testing.T) {
	src, dst := pkgDB{t: t, db: freshDB(t).GetDB()}, pkgDB{t: t, db: freshDB(t).GetDB()}
	srcRepo, dstRepo := seedRepo(t, src.db, "npm-a", "npm"), seedRepo(t, dst.db, "npm-b", "npm")
	codec := codecOf(t, "npm")

	seedNPMContent(t, src.db, srcRepo)
	replicateUnits(t, src, dst, srcRepo, dstRepo, codec, npmPlainVer, npmPlainTag)

	// Deleting a version hard removes its tags and, when empty, the package.
	tx, err := dst.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	del, err := codec.Delete(context.Background(), tx, dstRepo, npmPlainVer, false, "")
	if err != nil || del.Missing || len(del.ReleasedBlobs) != 1 || del.RemovedCount != 1 {
		t.Fatalf("delete = %+v, %v", del, err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit delete: %v", err)
	}

	if n := dst.scalar(`SELECT count(*) FROM packages WHERE repository_id = $1 AND name_key = $2`, dstRepo, npmPlainPkg); n != 0 {
		t.Fatalf("empty package kept: %d", n)
	}

	if n := dst.scalar(`SELECT count(*) FROM package_tags WHERE unit_key = $1`, npmPlainTag); n != 0 {
		t.Fatalf("tag of the deleted version kept: %d", n)
	}
}

// Trashing the files of a unit never restamps a version that is already in the
// trash: its deleted_at starts the retention clock.
func TestPackageCodecTrashKeepsTheStampOfATrashedVersion(t *testing.T) {
	db := pkgDB{t: t, db: freshDB(t).GetDB()}
	repo := seedRepo(t, db.db, "npm-trash", "npm")

	db.seedVersion(repo, "npm", npmPlainPkg, npmPlainPkg, `{}`, "2.0.0", `{}`,
		seedFile{path: "plain/-/plain-2.0.0.tgz", content: "tgz", role: "primary", unitKey: npmPlainVer, listed: true, attrs: `{}`})
	db.exec(`UPDATE package_versions SET deleted_at = now() - interval '10 days', deleted_by = 'earlier' WHERE repository_id = $1`, repo)

	pc, ok := codecOf(t, "npm").(*packageCodec)
	if !ok {
		t.Fatal("npm codec is not a packageCodec")
	}

	tx, err := db.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	u := &unitRows{}
	u.ver.id = db.text(`SELECT id::text FROM package_versions WHERE repository_id = $1`, repo)
	fileID := db.text(`SELECT id::text FROM package_files WHERE repository_id = $1`, repo)

	if err := pc.trash(context.Background(), tx, u, []string{fileID}, "federation:test"); err != nil {
		t.Fatal(err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if got := db.text(`SELECT deleted_by FROM package_versions WHERE repository_id = $1`, repo); got != "earlier" {
		t.Fatalf("deleted_by = %q, want the earlier stamp kept", got)
	}
}
