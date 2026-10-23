// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"
)

const (
	npmScopedPkg = "@acme/widget"
	npmPlainPkg  = "plain"
)

const (
	npmVerKey     = npmScopedPkg + "@1.0.0"
	npmVerKey2    = npmScopedPkg + "@1.1.0"
	npmTagKey     = npmScopedPkg + "#latest"
	npmTagNext    = npmScopedPkg + "#next"
	npmPlainVer   = npmPlainPkg + "@2.0.0"
	npmPlainTag   = npmPlainPkg + "#latest"
	npmManifestV1 = `{"name": "@acme/widget", "version": "1.0.0", "description": "widget"}`
)

// seedNPMContent writes two packages (scoped with two versions and two tags,
// unscoped with one version and one tag) into repoID, the way the publish
// handler does.
func seedNPMContent(t *testing.T, db *sql.DB, repoID string) {
	t.Helper()

	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()

		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed npm: %v\n%s", err, query)
		}
	}

	exec(`INSERT INTO npm_packages (repository_id, scope, name, full_name, latest_version) VALUES ($1, '@acme', 'widget', $2, '1.0.0')`, repoID, npmScopedPkg)
	exec(`INSERT INTO npm_packages (repository_id, scope, name, full_name, latest_version) VALUES ($1, '', 'plain', $2, '2.0.0')`, repoID, npmPlainPkg)

	blobA, _ := seedBlob(t, db, "tarball widget 1.0.0")
	blobB, _ := seedBlob(t, db, "tarball widget 1.1.0")
	blobC, _ := seedBlob(t, db, "tarball plain 2.0.0")

	insertVersion := func(pkg, version, blob, manifest string) {
		t.Helper()
		exec(`INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id)
		      SELECT p.id, $3, $5::jsonb, 'sha512-abc' || $3, 'sha1-' || $3, $2 || '-' || $3 || '.tgz', $4::uuid
		        FROM npm_packages p WHERE p.repository_id = $1 AND p.full_name = $2`,
			repoID, pkg, version, blob, manifest)
	}

	insertVersion(npmScopedPkg, "1.0.0", blobA, npmManifestV1)
	insertVersion(npmScopedPkg, "1.1.0", blobB, `{"name": "@acme/widget", "version": "1.1.0"}`)
	insertVersion(npmPlainPkg, "2.0.0", blobC, `{"name": "plain", "version": "2.0.0"}`)

	insertTag := func(pkg, tag, version string) {
		t.Helper()
		exec(`INSERT INTO npm_dist_tags (package_id, tag, version)
		      SELECT p.id, $3, $4 FROM npm_packages p WHERE p.repository_id = $1 AND p.full_name = $2`,
			repoID, pkg, tag, version)
	}

	insertTag(npmScopedPkg, "latest", "1.0.0")
	insertTag(npmScopedPkg, "next", "1.1.0")
	insertTag(npmPlainPkg, "latest", "2.0.0")
}

func npmUnitKeys() []string {
	return []string{npmVerKey, npmVerKey2, npmPlainVer, npmTagKey, npmTagNext, npmPlainTag}
}

// inApplyTx runs fn in a transaction that carries the federation apply guard,
// then commits.
func inApplyTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx)) {
	t.Helper()

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SET LOCAL oa.federation_apply = 'on'`); err != nil {
		t.Fatalf("set apply guard: %v", err)
	}

	fn(tx)

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// blobIDMap resolves the blobs of content to local blobs.id values.
func blobIDMap(t *testing.T, db *sql.DB, c *Content) map[string]string {
	t.Helper()

	out := map[string]string{}

	for _, b := range c.Blobs {
		var id string
		if err := db.QueryRowContext(context.Background(), `SELECT id::text FROM blobs WHERE sha256 = $1`, b.SHA256).Scan(&id); err != nil {
			t.Fatalf("blob %s: %v", b.SHA256, err)
		}

		out[b.SHA256] = id
	}

	return out
}

func mustExport(t *testing.T, db *sql.DB, c Codec, repoID, key string) *Content {
	t.Helper()

	content, err := c.Export(context.Background(), db, repoID, key)
	if err != nil {
		t.Fatalf("Export(%s): %v", key, err)
	}

	if content == nil {
		t.Fatalf("Export(%s): no content", key)
	}

	return content
}

// npmState dumps the replicated rows of a repository in a comparable form.
func npmState(t *testing.T, db *sql.DB, repoID string) map[string]string {
	t.Helper()

	ctx := context.Background()
	out := map[string]string{}

	rows, err := db.QueryContext(ctx,
		`SELECT p.full_name || '@' || v.version,
		        concat_ws('|', p.scope, p.name, v.manifest_json::text, v.integrity, v.shasum, v.tarball_filename, v.blob_id::text, v.created_at::text, coalesce(v.deleted_at::text, 'live'))
		   FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id WHERE p.repository_id = $1
		 UNION ALL
		 SELECT p.full_name || '#' || d.tag, d.version
		   FROM npm_dist_tags d JOIN npm_packages p ON p.id = d.package_id WHERE p.repository_id = $1`, repoID)
	if err != nil {
		t.Fatalf("npm state: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan: %v", err)
		}

		out[k] = v
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("npm state: %v", err)
	}

	return out
}

type npmPkgRow struct {
	id, scope, name, latest string
	stale                   bool
}

func npmPackageRow(t *testing.T, db *sql.DB, repoID, fullName string) npmPkgRow {
	t.Helper()

	var r npmPkgRow

	err := db.QueryRowContext(context.Background(),
		`SELECT id::text, scope, name, latest_version, packument_stale FROM npm_packages WHERE repository_id = $1 AND full_name = $2`,
		repoID, fullName).Scan(&r.id, &r.scope, &r.name, &r.latest, &r.stale)
	if err != nil {
		t.Fatalf("package %s: %v", fullName, err)
	}

	return r
}

func TestNPMCodecTriggersLogUnitKeys(t *testing.T) {
	db := freshDB(t).GetDB()
	repoA := seedRepo(t, db, "npm-a", "npm")
	enableLog(t, db, repoA)
	seedNPMContent(t, db, repoA)

	got := loggedUnits(t, db, repoA)
	want := map[string]string{
		npmVerKey:   "artifact_added/false",
		npmVerKey2:  "artifact_added/false",
		npmPlainVer: "artifact_added/false",
		npmTagKey:   "ref_moved/false",
		npmTagNext:  "ref_moved/false",
		npmPlainTag: "ref_moved/false",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logged units = %v, want %v", got, want)
	}
}

func npmTestCodec(t *testing.T) Codec {
	t.Helper()

	c, err := CodecFor("npm")
	if err != nil {
		t.Fatal(err)
	}

	return c
}

// npmSeededRepo returns a database with one npm repository holding the seeded
// content (federation not enabled).
func npmSeededRepo(t *testing.T) (*sql.DB, string) {
	t.Helper()

	db := freshDB(t).GetDB()
	repoA := seedRepo(t, db, "npm-a", "npm")
	seedNPMContent(t, db, repoA)

	return db, repoA
}

func TestNPMCodecExportVersion(t *testing.T) {
	db, repoA := npmSeededRepo(t)
	c := npmTestCodec(t)

	ver := mustExport(t, db, c, repoA, npmVerKey)
	if ver.Kind != KindArtifact || len(ver.Blobs) != 1 || ver.ContentRef != ver.Blobs[0].SHA256 || len(ver.Deps) != 0 {
		t.Fatalf("version content = %+v", ver)
	}

	var vd npmVersionData
	if err := json.Unmarshal(ver.Data, &vd); err != nil {
		t.Fatal(err)
	}

	if vd.FullName != npmScopedPkg || vd.Version != "1.0.0" || vd.BlobID != ver.Blobs[0].SHA256 || vd.TarballFilename != "@acme/widget-1.0.0.tgz" {
		t.Fatalf("version data = %+v", vd)
	}

	size, count, err := c.Usage(context.Background(), db, repoA, npmVerKey, ver)
	if err != nil || size != ver.Blobs[0].Size || count != 1 {
		t.Fatalf("version usage = %d, %d, %v", size, count, err)
	}
}

func TestNPMCodecExportTag(t *testing.T) {
	db, repoA := npmSeededRepo(t)
	c := npmTestCodec(t)

	tag := mustExport(t, db, c, repoA, npmTagKey)
	if tag.Kind != KindRef || tag.ContentRef != "1.0.0" || !slices.Equal(tag.Deps, []string{npmVerKey}) || len(tag.Blobs) != 0 {
		t.Fatalf("tag content = %+v", tag)
	}

	size, count, err := c.Usage(context.Background(), db, repoA, npmTagKey, tag)
	if err != nil || size != 0 || count != 0 {
		t.Fatalf("tag usage = %d, %d, %v", size, count, err)
	}
}

func TestNPMCodecExportMissing(t *testing.T) {
	db, repoA := npmSeededRepo(t)
	c := npmTestCodec(t)

	for _, key := range []string{npmScopedPkg + "@9.9.9", npmScopedPkg + "#nope", "ghost@1.0.0"} {
		content, err := c.Export(context.Background(), db, repoA, key)
		if err != nil || content != nil {
			t.Errorf("Export(%s) = %v, %v; want nil, nil", key, content, err)
		}
	}

	if _, err := c.Export(context.Background(), db, repoA, "nonsense"); err == nil {
		t.Error("Export of a malformed key succeeded")
	}
}

func TestNPMCodecImportReplicatesAndStaysSilent(t *testing.T) {
	db := freshDB(t).GetDB()
	repoA := seedRepo(t, db, "npm-a", "npm")
	repoB := seedRepo(t, db, "npm-b", "npm")
	enableLog(t, db, repoA)
	enableLog(t, db, repoB)
	seedNPMContent(t, db, repoA)

	c, err := CodecFor("npm")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Versions first, then the tags that depend on them.
	keys := []string{npmVerKey, npmVerKey2, npmPlainVer, npmTagKey, npmTagNext, npmPlainTag}
	changes := map[string]*Change{}

	for _, key := range keys {
		content := mustExport(t, db, c, repoA, key)

		inApplyTx(t, db, func(tx *sql.Tx) {
			ch, err := c.Import(ctx, tx, repoB, key, content, blobIDMap(t, db, content))
			if err != nil {
				t.Fatalf("Import(%s): %v", key, err)
			}

			changes[key] = ch
		})
	}

	if got, want := npmState(t, db, repoB), npmState(t, db, repoA); !reflect.DeepEqual(got, want) {
		t.Fatalf("B state differs from A\n got: %v\nwant: %v", got, want)
	}

	if log := loggedUnits(t, db, repoB); len(log) != 0 {
		t.Fatalf("applying logged %v, want nothing", log)
	}

	assertNPMImportChanges(t, db, repoB, changes)
}

func assertNPMImportChanges(t *testing.T, db *sql.DB, repoB string, changes map[string]*Change) {
	t.Helper()

	scoped := npmPackageRow(t, db, repoB, npmScopedPkg)
	id := scoped.id

	if scoped.scope != "@acme" || scoped.name != "widget" || scoped.latest != "1.0.0" || !scoped.stale {
		t.Fatalf("scoped package row = %q %q %q %v", scoped.scope, scoped.name, scoped.latest, scoped.stale)
	}

	plain := npmPackageRow(t, db, repoB, npmPlainPkg)
	if plain.scope != "" || plain.name != "plain" {
		t.Fatalf("plain package row scope=%q name=%q", plain.scope, plain.name)
	}

	ch := changes[npmVerKey]
	if ch.Replaced || len(ch.Regen) != 1 || ch.Regen[0] != (Regen{Kind: "npm", RepoID: repoB, PackageID: id}) {
		t.Fatalf("version change = %+v", ch)
	}

	if ch.Artifact == nil || ch.Artifact.Name != npmScopedPkg || ch.Artifact.Version != "1.0.0" ||
		ch.Artifact.Path != "@acme/widget-1.0.0.tgz" || len(ch.Artifact.SHA256) != 64 || ch.Artifact.Size == 0 {
		t.Fatalf("version artifact = %+v", ch.Artifact)
	}

	if tc := changes[npmTagKey]; tc.Artifact != nil || len(tc.Regen) != 1 || tc.Regen[0].PackageID != id {
		t.Fatalf("tag change = %+v", tc)
	}
}

func TestNPMCodecImportTagCreatesParentAndMovesTag(t *testing.T) {
	db := freshDB(t).GetDB()
	repoA := seedRepo(t, db, "npm-a", "npm")
	repoB := seedRepo(t, db, "npm-b", "npm")
	seedNPMContent(t, db, repoA)

	c, err := CodecFor("npm")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	tag := mustExport(t, db, c, repoA, npmTagKey)

	// A tag imported before any version still finds a parent package.
	inApplyTx(t, db, func(tx *sql.Tx) {
		if _, err := c.Import(ctx, tx, repoB, npmTagKey, tag, nil); err != nil {
			t.Fatalf("Import tag: %v", err)
		}
	})

	parent := npmPackageRow(t, db, repoB, npmScopedPkg)
	if parent.scope != "@acme" || parent.name != "widget" || parent.latest != "" || !parent.stale {
		t.Fatalf("parent = %q %q %q %v", parent.scope, parent.name, parent.latest, parent.stale)
	}

	// Moving the tag updates the row in place.
	if _, err := db.ExecContext(ctx, `UPDATE npm_dist_tags SET version = '1.1.0' WHERE tag = 'latest' AND package_id = (SELECT id FROM npm_packages WHERE repository_id = $1 AND full_name = $2)`, repoA, npmScopedPkg); err != nil {
		t.Fatal(err)
	}

	moved := mustExport(t, db, c, repoA, npmTagKey)

	inApplyTx(t, db, func(tx *sql.Tx) {
		if _, err := c.Import(ctx, tx, repoB, npmTagKey, moved, nil); err != nil {
			t.Fatalf("Import moved tag: %v", err)
		}
	})

	if got := npmState(t, db, repoB)[npmTagKey]; got != "1.1.0" {
		t.Fatalf("tag latest = %q, want 1.1.0", got)
	}

	var n int

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM npm_dist_tags d JOIN npm_packages p ON p.id = d.package_id WHERE p.repository_id = $1`, repoB).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tag rows = %d, %v; want 1", n, err)
	}
}

func TestNPMCodecImportReplacesVersionInPlace(t *testing.T) {
	db := freshDB(t).GetDB()
	repoA := seedRepo(t, db, "npm-a", "npm")
	repoB := seedRepo(t, db, "npm-b", "npm")
	seedNPMContent(t, db, repoA)

	c, err := CodecFor("npm")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	content := mustExport(t, db, c, repoA, npmVerKey)

	inApplyTx(t, db, func(tx *sql.Tx) {
		if _, err := c.Import(ctx, tx, repoB, npmVerKey, content, blobIDMap(t, db, content)); err != nil {
			t.Fatal(err)
		}
	})

	var firstID string
	if err := db.QueryRowContext(ctx, `SELECT v.id::text FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id WHERE p.repository_id = $1`, repoB).Scan(&firstID); err != nil {
		t.Fatal(err)
	}

	// Same version republished at the origin with another tarball and manifest.
	newBlobID, newSha := seedBlob(t, db, "tarball widget 1.0.0 rebuilt")
	if _, err := db.ExecContext(ctx,
		`UPDATE npm_versions SET blob_id = $2::uuid, manifest_json = '{"name": "@acme/widget", "version": "1.0.0", "deprecated": "no"}'::jsonb
		  WHERE package_id = (SELECT id FROM npm_packages WHERE repository_id = $1 AND full_name = $3) AND version = '1.0.0'`,
		repoA, newBlobID, npmScopedPkg); err != nil {
		t.Fatal(err)
	}

	oldSha := content.Blobs[0].SHA256
	oldSize := content.Blobs[0].Size
	content = mustExport(t, db, c, repoA, npmVerKey)

	if content.ContentRef != newSha {
		t.Fatalf("content ref = %s, want %s", content.ContentRef, newSha)
	}

	var ch *Change

	inApplyTx(t, db, func(tx *sql.Tx) {
		var err error

		ch, err = c.Import(ctx, tx, repoB, npmVerKey, content, blobIDMap(t, db, content))
		if err != nil {
			t.Fatal(err)
		}
	})

	if !ch.Replaced || ch.ReplacedSize != oldSize || ch.ReplacedCount != 1 || !slices.Equal(ch.ReleasedBlobs, []string{oldSha}) {
		t.Fatalf("change = %+v", ch)
	}

	var (
		secondID, blobID string
		n                int
	)

	if err := db.QueryRowContext(ctx,
		`SELECT v.id::text, v.blob_id::text, (SELECT count(*) FROM npm_versions x JOIN npm_packages xp ON xp.id = x.package_id WHERE xp.repository_id = $1)
		   FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id WHERE p.repository_id = $1`, repoB).Scan(&secondID, &blobID, &n); err != nil {
		t.Fatal(err)
	}

	if secondID != firstID || blobID != newBlobID || n != 1 {
		t.Fatalf("row id %s -> %s, blob %s (want %s), total rows %d", firstID, secondID, blobID, newBlobID, n)
	}

	if got, want := npmState(t, db, repoB)[npmVerKey], npmState(t, db, repoA)[npmVerKey]; got != want {
		t.Fatalf("replaced row differs\n got: %s\nwant: %s", got, want)
	}

	// A blob the engine failed to provide is rejected.
	inApplyTx(t, db, func(tx *sql.Tx) {
		if _, err := c.Import(ctx, tx, repoB, npmVerKey, content, map[string]string{}); err == nil {
			t.Fatal("Import without blob ids succeeded")
		}
	})
}

// npmDelete runs Codec.Delete inside an apply transaction.
func npmDelete(t *testing.T, db *sql.DB, repoID, key string, soft bool, by string) *Change {
	t.Helper()

	var ch *Change

	inApplyTx(t, db, func(tx *sql.Tx) {
		var err error

		ch, err = npmTestCodec(t).Delete(context.Background(), tx, repoID, key, soft, by)
		if err != nil {
			t.Fatalf("Delete(%s): %v", key, err)
		}
	})

	return ch
}

func TestNPMCodecDeleteSoft(t *testing.T) {
	db, repoA := npmSeededRepo(t)
	c := npmTestCodec(t)
	ctx := context.Background()
	pkgID := npmPackageRow(t, db, repoA, npmScopedPkg).id

	ch := npmDelete(t, db, repoA, npmVerKey, true, "alice")
	if ch.Missing || len(ch.ReleasedBlobs) != 0 || len(ch.Regen) != 1 || ch.Regen[0].PackageID != pkgID ||
		ch.Artifact == nil || ch.Artifact.Version != "1.0.0" || ch.RemovedCount != 1 || ch.RemovedSize == 0 {
		t.Fatalf("soft change = %+v", ch)
	}

	var deletedBy string
	if err := db.QueryRowContext(ctx, `SELECT deleted_by FROM npm_versions WHERE package_id = $1 AND version = '1.0.0' AND deleted_at IS NOT NULL`, pkgID).Scan(&deletedBy); err != nil || deletedBy != "alice" {
		t.Fatalf("deleted_by = %q, %v", deletedBy, err)
	}

	if content, err := c.Export(ctx, db, repoA, npmVerKey); err != nil || content != nil {
		t.Fatalf("Export of trashed version = %v, %v", content, err)
	}

	if _, ok := npmState(t, db, repoA)[npmTagKey]; !ok {
		t.Fatal("soft delete removed the dist-tag")
	}

	// The tag of a trashed version still exports, without a dependency.
	if tag := mustExport(t, db, c, repoA, npmTagKey); len(tag.Deps) != 0 {
		t.Fatalf("tag of trashed version deps = %v", tag.Deps)
	}

	// Deleting again finds nothing live.
	if again := npmDelete(t, db, repoA, npmVerKey, true, "alice"); !again.Missing {
		t.Fatalf("second delete = %+v", again)
	}
}

func TestNPMCodecDeleteHard(t *testing.T) {
	db, repoA := npmSeededRepo(t)

	ch := npmDelete(t, db, repoA, npmVerKey2, false, "")
	if len(ch.ReleasedBlobs) != 1 || ch.RemovedCount != 1 || ch.RemovedSize == 0 || len(ch.Regen) != 1 {
		t.Fatalf("hard change = %+v", ch)
	}

	state := npmState(t, db, repoA)
	if _, ok := state[npmVerKey2]; ok {
		t.Fatal("hard delete left the version row")
	}

	if _, ok := state[npmTagNext]; ok {
		t.Fatal("hard delete left the dist-tag of the removed version")
	}

	if _, ok := state[npmTagKey]; !ok {
		t.Fatal("hard delete removed an unrelated dist-tag")
	}

	if !npmPackageRow(t, db, repoA, npmScopedPkg).stale {
		t.Fatal("package not marked stale")
	}

	// The last version of a package going hard removes the package row too.
	npmDelete(t, db, repoA, npmPlainVer, false, "")

	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM npm_packages WHERE repository_id = $1 AND full_name = $2`, repoA, npmPlainPkg).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plain package rows = %d, %v; want 0", n, err)
	}
}

func TestNPMCodecDeleteTagIsAlwaysHard(t *testing.T) {
	db, repoA := npmSeededRepo(t)

	ch := npmDelete(t, db, repoA, npmTagKey, true, "alice")
	if ch.Missing || len(ch.Regen) != 1 || ch.Artifact != nil || len(ch.ReleasedBlobs) != 0 {
		t.Fatalf("tag change = %+v", ch)
	}

	if _, ok := npmState(t, db, repoA)[npmTagKey]; ok {
		t.Fatal("tag row still present")
	}

	if again := npmDelete(t, db, repoA, npmTagKey, false, ""); !again.Missing {
		t.Fatalf("second tag delete = %+v", again)
	}
}

func TestNPMCodecBackfillMatchesTriggerKeys(t *testing.T) {
	db := freshDB(t).GetDB()
	repoA := seedRepo(t, db, "npm-a", "npm")
	repoD := seedRepo(t, db, "npm-d", "npm")

	// A is federated while the rows are written: the triggers log them.
	enableLog(t, db, repoA)
	seedNPMContent(t, db, repoA)

	// D has the same content from before federation was enabled.
	seedNPMContent(t, db, repoD)
	enableLog(t, db, repoD)

	if log := loggedUnits(t, db, repoD); len(log) != 0 {
		t.Fatalf("D logged before backfill: %v", log)
	}

	c, err := CodecFor("npm")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := c.Backfill(context.Background(), tx, repoD); err != nil {
		t.Fatalf("Backfill: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, want := loggedUnits(t, db, repoD), loggedUnits(t, db, repoA)
	if !maps.Equal(got, want) {
		t.Fatalf("backfill keys %v differ from trigger keys %v", got, want)
	}

	if len(got) != len(npmUnitKeys()) {
		t.Fatalf("logged %d units, want %d", len(got), len(npmUnitKeys()))
	}

	// content_ref follows migration 071: blob sha for versions, version for tags.
	refs := func(repoID string) map[string]string {
		rows, err := db.QueryContext(context.Background(), `SELECT unit_key, content_ref FROM federation_units WHERE repo_id = $1`, repoID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()

		out := map[string]string{}

		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}

			out[k] = v
		}

		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}

		return out
	}

	if gotRefs, wantRefs := refs(repoD), refs(repoA); !maps.Equal(gotRefs, wantRefs) {
		t.Fatalf("backfill content refs %v differ from trigger refs %v", gotRefs, wantRefs)
	}
}

// TestNPMVersionColumnsPinned fails when npm_versions or npm_dist_tags gains a
// column, forcing a decision on whether the codec replicates it.
func TestNPMVersionColumnsPinned(t *testing.T) {
	db := freshDB(t).GetDB()
	ctx := context.Background()

	versions, err := tableColumns(ctx, db, "npm_versions")
	if err != nil {
		t.Fatal(err)
	}

	wantVersions := []string{"id", "package_id", "version", "manifest_json", "integrity", "shasum", "tarball_filename", "created_at", "blob_id", "updated_at", "deleted_at", "deleted_by"}
	if !sameSet(versions, wantVersions) {
		t.Fatalf("npm_versions columns = %v; update codec_npm.go and this list", versions)
	}

	tags, err := tableColumns(ctx, db, "npm_dist_tags")
	if err != nil {
		t.Fatal(err)
	}

	if !sameSet(tags, []string{"id", "package_id", "tag", "version", "updated_at"}) {
		t.Fatalf("npm_dist_tags columns = %v; update codec_npm.go and this list", tags)
	}
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)

	return slices.Equal(x, y)
}
