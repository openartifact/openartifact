// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
)

// fedcFormat describes one simple table codec: how a row of its table is
// inserted the way the upload path does, and the unit key the capture trigger
// must log for it.
type fedcFormat struct {
	format string
	table  string
	key    string
	// insert takes the repository id ($1) and one blob id per blob ($2, $3).
	insert string
	blobs  []string // payloads of the blobs, in blobCols order
	// localCol is a statistics column set to a non-default value on the origin
	// that must NOT travel.
	localCol string
}

func fedcFormats() []fedcFormat {
	return []fedcFormat{
		{
			format: "raw", table: "raw_artifacts", key: "docs/readme.txt",
			insert: `INSERT INTO raw_artifacts (repository_id, path, content_type, metadata, blob_id)
			         VALUES ($1, 'docs/readme.txt', 'text/plain', '{"owner": "alice", "n": 3}', $2)`,
			blobs: []string{"raw readme payload"},
		},
		{
			format: "maven", table: "maven_artifacts", key: "org/acme/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-3-sources.jar",
			insert: `INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, classifier, extension,
			             snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path, blob_id, md5, sha1, sha512)
			         VALUES ($1, 'org.acme', 'lib', '1.0-SNAPSHOT', 'sources', 'jar', '20240101.120000', 3, true,
			             'org/acme/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-3-sources.jar', $2, 'md5x', 'sha1x', 'sha512x')`,
			blobs: []string{"maven sources jar"},
		},
		{
			format: "pypi", table: "pypi_packages", key: "demo-pkg/1.2.3/demo_pkg-1.2.3.tar.gz",
			insert: `INSERT INTO pypi_packages (repository_id, name, version, filename, package_type, python_version, requires_python,
			             summary, author, author_email, license, keywords, classifiers, blob_id)
			         VALUES ($1, 'demo-pkg', '1.2.3', 'demo_pkg-1.2.3.tar.gz', 'sdist', 'source', '>=3.9',
			             'a demo', 'Alice', 'alice@example.com', 'MIT', 'demo test', ARRAY['Topic :: Utilities', 'License :: OSI Approved'], $2)`,
			blobs: []string{"pypi sdist payload"},
		},
		{
			format: "helm", table: "helm_charts", key: "mychart@0.3.1",
			insert: `INSERT INTO helm_charts (repository_id, name, version, app_version, description, keywords, home, sources,
			             maintainers, icon, api_version, type, blob_id)
			         VALUES ($1, 'mychart', '0.3.1', '2.0', 'a chart', ARRAY['web', 'demo'], 'https://example.com',
			             ARRAY['https://example.com/src'], '[{"name": "alice", "email": "alice@example.com"}]', 'https://example.com/i.png',
			             'v2', 'library', $2)`,
			blobs: []string{"helm chart tgz"},
		},
		{
			format: "cargo", table: "cargo_crates", key: "serde-demo@1.0.9",
			insert: `INSERT INTO cargo_crates (repository_id, name, version, description, homepage, documentation, repository, license,
			             keywords, categories, authors, readme, metadata_json, yanked, download_count, blob_id)
			         VALUES ($1, 'serde-demo', '1.0.9', 'a crate', 'https://example.com', 'https://docs.example.com', 'https://git.example.com',
			             'MIT OR Apache-2.0', ARRAY['serde', 'demo'], ARRAY['encoding'], ARRAY['Alice <a@example.com>'], '# readme',
			             '{"deps": [{"name": "x"}]}', true, 41, $2)`,
			blobs:    []string{"cargo crate payload"},
			localCol: "download_count",
		},
		{
			format: "go", table: "gomod_modules", key: "example.com/acme/mod@v1.4.0",
			insert: `INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id, mod_blob_id)
			         VALUES ($1, 'example.com/acme/mod', 'v1.4.0', 'module example.com/acme/mod' || chr(10) || 'go 1.22', $2, $3)`,
			blobs: []string{"go module zip", "go module mod file"},
		},
		{
			format: "nuget", table: "nuget_packages", key: "Acme.Lib@2.1.0-beta",
			insert: `INSERT INTO nuget_packages (repository_id, package_id, version, title, description, summary, authors, owners, tags,
			             project_url, license_url, icon_url, require_license_acceptance, is_prerelease, min_client_version,
			             download_count, dependency_groups, blob_id)
			         VALUES ($1, 'Acme.Lib', '2.1.0-beta', 'Acme Lib', 'desc', 'sum', 'Alice', 'Acme', 'a b',
			             'https://example.com', 'https://example.com/l', 'https://example.com/i', true, 't'::boolean, '5.0',
			             17, '[{"targetFramework": "net8.0", "dependencies": []}]', $2)`,
			blobs:    []string{"nuget nupkg payload"},
			localCol: "download_count",
		},
	}
}

// fedcInstance is one OpenArtifact database with a federated repository.
type fedcInstance struct {
	pg     *database.PostgresDB
	db     *sql.DB
	repoID string
}

func fedcNewInstance(t *testing.T, format string) *fedcInstance {
	t.Helper()

	pg := freshDB(t)
	db := pg.GetDB()
	repoID := seedRepo(t, db, "fedc-"+format, format)

	enableLog(t, db, repoID)

	return &fedcInstance{pg: pg, db: db, repoID: repoID}
}

func fedcTableCodec(t *testing.T, format string) *tableCodec {
	t.Helper()

	c, err := CodecFor(format)
	if err != nil {
		t.Fatalf("CodecFor(%s): %v", format, err)
	}

	tc, ok := c.(*tableCodec)
	if !ok {
		t.Fatalf("codec of %s is %T, want *tableCodec", format, c)
	}

	return tc
}

// fedcSeed inserts the blobs and the row of f into the instance and returns
// the sha256 digests of the blobs.
func fedcSeed(t *testing.T, in *fedcInstance, f fedcFormat) []string {
	t.Helper()

	args := []any{in.repoID}
	shas := make([]string, 0, len(f.blobs))

	for _, payload := range f.blobs {
		id, sha := seedBlob(t, in.db, payload)

		args = append(args, id)
		shas = append(shas, sha)
	}

	if _, err := in.db.ExecContext(context.Background(), f.insert, args...); err != nil {
		t.Fatalf("seed %s row: %v", f.format, err)
	}

	return shas
}

// fedcApply runs fn in a transaction carrying the federation apply guard (as
// the engine's apply path does), then commits.
func fedcApply(t *testing.T, db *sql.DB, fn func(tx *sql.Tx)) {
	t.Helper()

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT set_config('oa.federation_apply', 'on', true)`); err != nil {
		t.Fatalf("set apply guard: %v", err)
	}

	fn(tx)

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// fedcEnsureBlobs creates the blobs of content in the instance's blobs table,
// as the engine does after fetching them, and returns sha256 -> blobs.id.
func fedcEnsureBlobs(t *testing.T, in *fedcInstance, c *Content) map[string]string {
	t.Helper()

	ids := map[string]string{}

	for _, b := range c.Blobs {
		id, err := in.pg.EnsureBlob(context.Background(), b.SHA256, b.MediaType, b.Size)
		if err != nil {
			t.Fatalf("EnsureBlob(%s): %v", b.SHA256, err)
		}

		ids[b.SHA256] = id
	}

	return ids
}

// fedcLiveRow returns the replicated columns of the live row of key as
// decoded JSON: local columns, statistics and blob ids are dropped (blobs are
// compared by digest through Export).
func fedcLiveRow(t *testing.T, in *fedcInstance, tc *tableCodec, key string) map[string]any {
	t.Helper()

	clause, args, ok := tc.spec.where(key)
	if !ok {
		t.Fatalf("malformed key %q", key)
	}

	drop := slices.Concat(alwaysLocal, tc.spec.exclude, tc.spec.blobCols, []string{"search_tsv"})

	var raw []byte

	err := in.db.QueryRowContext(context.Background(),
		`SELECT to_jsonb(t) - $`+strconv.Itoa(len(args)+2)+`::text[] FROM `+tc.spec.table+` t
		  WHERE t.repository_id = $1 AND t.deleted_at IS NULL AND `+clause,
		append(append([]any{in.repoID}, args...), drop)...).Scan(&raw)
	if err != nil {
		t.Fatalf("read %s row %q: %v", tc.spec.table, key, err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode row: %v", err)
	}

	return out
}

func fedcRowCounts(t *testing.T, in *fedcInstance, table string) (int, int) {
	t.Helper()

	var live, trashed int

	err := in.db.QueryRowContext(context.Background(),
		`SELECT count(*) FILTER (WHERE deleted_at IS NULL), count(*) FILTER (WHERE deleted_at IS NOT NULL)
		   FROM `+table+` WHERE repository_id = $1`, in.repoID).Scan(&live, &trashed)
	if err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}

	return live, trashed
}

func fedcUnitLog(t *testing.T, in *fedcInstance) (map[string]string, map[string]string) {
	t.Helper()

	refs := map[string]string{}

	rows, err := in.db.QueryContext(context.Background(),
		`SELECT unit_key, content_ref FROM federation_units WHERE repo_id = $1`, in.repoID)
	if err != nil {
		t.Fatalf("list content refs: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var k, ref string
		if err := rows.Scan(&k, &ref); err != nil {
			t.Fatalf("scan: %v", err)
		}

		refs[k] = ref
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("list content refs: %v", err)
	}

	return loggedUnits(t, in.db, in.repoID), refs
}

func TestSimpleCodecsReplicateBetweenDatabases(t *testing.T) {
	t.Parallel()

	for _, f := range fedcFormats() {
		t.Run(f.format, func(t *testing.T) {
			t.Parallel()
			fedcReplicate(t, f)
		})
	}
}

func fedcReplicate(t *testing.T, f fedcFormat) {
	t.Helper()

	ctx := context.Background()
	tc := fedcTableCodec(t, f.format)
	src := fedcNewInstance(t, f.format)
	dst := fedcNewInstance(t, f.format)

	shas := fedcSeed(t, src, f)

	// The capture trigger logged the row under its documented key.
	log, refs := fedcUnitLog(t, src)
	if want := map[string]string{f.key: "artifact_added/false"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("origin log = %v, want %v", log, want)
	}

	if refs[f.key] != shas[0] {
		t.Fatalf("content_ref = %q, want the sha256 of the first blob %q", refs[f.key], shas[0])
	}

	// Export carries the blobs as digests, no local columns and no statistics.
	content, err := tc.Export(ctx, src.db, src.repoID, f.key)
	if err != nil || content == nil {
		t.Fatalf("Export(%s) = %v, %v", f.key, content, err)
	}

	fedcCheckExport(t, tc, f, content, shas)

	// Import into the second database.
	ids := fedcEnsureBlobs(t, dst, content)

	var change *Change

	fedcApply(t, dst.db, func(tx *sql.Tx) {
		var err error

		change, err = tc.Import(ctx, tx, dst.repoID, f.key, content, ids)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
	})

	if change.Replaced || change.Artifact == nil {
		t.Fatalf("Import change = %+v, want a fresh insert described for webhooks", change)
	}

	// Replicated columns are identical on both sides, statistics stay local.
	if got, want := fedcLiveRow(t, dst, tc, f.key), fedcLiveRow(t, src, tc, f.key); !reflect.DeepEqual(got, want) {
		t.Fatalf("replicated row differs\n dst: %v\n src: %v", got, want)
	}

	if got, err := tc.Export(ctx, dst.db, dst.repoID, f.key); err != nil || got == nil ||
		string(got.Data) != string(content.Data) || !reflect.DeepEqual(got.Blobs, content.Blobs) || got.ContentRef != content.ContentRef {
		t.Fatalf("re-export at destination differs (err %v)\n got: %+v\nwant: %+v", err, got, content)
	}

	if f.localCol != "" {
		fedcCheckLocalColumn(t, dst, tc, f)
	}

	fedcImportAgain(t, dst, tc, f, content, ids)

	// Applying must not log: only federation_apply_unit does.
	if log := loggedUnits(t, dst.db, dst.repoID); len(log) != 0 {
		t.Fatalf("Import under the apply guard logged %v", log)
	}

	fedcDeleteAndVerify(t, dst, tc, f, content, shas)

	if log := loggedUnits(t, dst.db, dst.repoID); len(log) != 0 {
		t.Fatalf("Delete under the apply guard logged %v", log)
	}
}

// fedcCheckExport verifies the shape of an exported unit.
func fedcCheckExport(t *testing.T, tc *tableCodec, f fedcFormat, c *Content, shas []string) {
	t.Helper()

	if c.Kind != KindArtifact || c.ContentRef != shas[0] || len(c.Blobs) != len(shas) {
		t.Fatalf("content = kind %q ref %q blobs %d, want artifact %q with %d blobs", c.Kind, c.ContentRef, len(c.Blobs), shas[0], len(shas))
	}

	for i, b := range c.Blobs {
		if b.SHA256 != shas[i] || b.Size != int64(len(f.blobs[i])) {
			t.Fatalf("blob %d = %+v, want sha %s size %d", i, b, shas[i], len(f.blobs[i]))
		}
	}

	var data Row
	if err := json.Unmarshal(c.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}

	for _, col := range slices.Concat(alwaysLocal, tc.spec.exclude) {
		if _, present := data[col]; present {
			t.Fatalf("exported data carries local column %q", col)
		}
	}

	for i, col := range tc.spec.blobCols {
		if got := data.Str(col); got != shas[i] {
			t.Fatalf("data[%s] = %q, want the digest %q", col, got, shas[i])
		}
	}

	size, count, err := tc.Usage(context.Background(), nil, "", f.key, c)
	if err != nil || count != 1 {
		t.Fatalf("Usage = %d, %d, %v", size, count, err)
	}
}

// fedcCheckLocalColumn checks that a statistics column of the origin did not
// travel: the destination keeps its own default.
func fedcCheckLocalColumn(t *testing.T, dst *fedcInstance, tc *tableCodec, f fedcFormat) {
	t.Helper()

	clause, args, _ := tc.spec.where(f.key)

	var got int64

	err := dst.db.QueryRowContext(context.Background(),
		`SELECT t.`+f.localCol+` FROM `+tc.spec.table+` t WHERE t.repository_id = $1 AND `+clause,
		append([]any{dst.repoID}, args...)...).Scan(&got)
	if err != nil || got != 0 {
		t.Fatalf("destination %s = %d, %v, want the local default 0", f.localCol, got, err)
	}
}

// fedcDeleteAndVerify soft deletes, re-imports and hard deletes the unit at
// the destination.
func fedcDeleteAndVerify(t *testing.T, dst *fedcInstance, tc *tableCodec, f fedcFormat, content *Content, shas []string) {
	t.Helper()

	ctx := context.Background()

	var totalSize int64
	for _, p := range f.blobs {
		totalSize += int64(len(p))
	}

	// Soft delete: the row stays in the trash with its blobs.
	soft := fedcDelete(t, dst, tc, f.key, true, "bob")
	if soft.Missing || soft.RemovedCount != 1 || soft.RemovedSize != totalSize || len(soft.ReleasedBlobs) != 0 || soft.Artifact == nil {
		t.Fatalf("soft delete change = %+v", soft)
	}

	if live, trashed := fedcRowCounts(t, dst, tc.spec.table); live != 0 || trashed != 1 {
		t.Fatalf("after soft delete: %d live, %d trashed rows", live, trashed)
	}

	var by string

	clause, args, _ := tc.spec.where(f.key)

	err := dst.db.QueryRowContext(ctx,
		`SELECT t.deleted_by FROM `+tc.spec.table+` t WHERE t.repository_id = $1 AND t.deleted_at IS NOT NULL AND `+clause,
		append([]any{dst.repoID}, args...)...).Scan(&by)
	if err != nil || by != "bob" {
		t.Fatalf("deleted_by = %q, %v, want bob", by, err)
	}

	fedcCheckTrashedUnit(t, dst, tc, f.key)

	// The unit comes back as a live row next to the trashed one.
	fedcReimport(t, dst, tc, content, f.key)

	if live, trashed := fedcRowCounts(t, dst, tc.spec.table); live != 1 || trashed != 1 {
		t.Fatalf("after re-import: %d live, %d trashed rows", live, trashed)
	}

	fedcHardDeleteAndVerify(t, dst, tc, f, shas, totalSize)
}

// fedcCheckTrashedUnit checks that a trashed unit is not exported and that
// soft deleting it again reports it missing.
func fedcCheckTrashedUnit(t *testing.T, dst *fedcInstance, tc *tableCodec, key string) {
	t.Helper()

	if got, err := tc.Export(context.Background(), dst.db, dst.repoID, key); err != nil || got != nil {
		t.Fatalf("Export of a trashed unit = %v, %v, want nil", got, err)
	}

	if again := fedcDelete(t, dst, tc, key, true, "bob"); !again.Missing {
		t.Fatalf("second soft delete = %+v, want Missing", again)
	}
}

// fedcHardDeleteAndVerify hard deletes the re-imported unit.
func fedcHardDeleteAndVerify(t *testing.T, dst *fedcInstance, tc *tableCodec, f fedcFormat, shas []string, totalSize int64) {
	t.Helper()

	// Hard delete removes the live row and releases its blobs.
	hard := fedcDelete(t, dst, tc, f.key, false, "bob")

	released := slices.Clone(hard.ReleasedBlobs)
	want := slices.Clone(shas)

	sort.Strings(released)
	sort.Strings(want)

	if hard.Missing || hard.RemovedCount != 1 || hard.RemovedSize != totalSize || !slices.Equal(released, want) {
		t.Fatalf("hard delete change = %+v, want released %v", hard, want)
	}

	if live, trashed := fedcRowCounts(t, dst, tc.spec.table); live != 0 || trashed != 1 {
		t.Fatalf("after hard delete: %d live, %d trashed rows (the trashed row is untouched)", live, trashed)
	}

	if gone := fedcDelete(t, dst, tc, f.key, false, "bob"); !gone.Missing {
		t.Fatalf("hard delete of a missing unit = %+v, want Missing", gone)
	}
}

// fedcReimport imports the unit again from its exported payload.
func fedcReimport(t *testing.T, dst *fedcInstance, tc *tableCodec, content *Content, key string) {
	t.Helper()

	ctx := context.Background()
	ids := fedcEnsureBlobs(t, dst, content)

	fedcApply(t, dst.db, func(tx *sql.Tx) {
		change, err := tc.Import(ctx, tx, dst.repoID, key, content, ids)
		if err != nil {
			t.Fatalf("re-Import: %v", err)
		}

		if change.Replaced {
			t.Fatalf("re-Import replaced a row, want a new live row beside the trashed one: %+v", change)
		}
	})
}

func fedcDelete(t *testing.T, in *fedcInstance, tc *tableCodec, key string, soft bool, by string) *Change {
	t.Helper()

	var ch *Change

	fedcApply(t, in.db, func(tx *sql.Tx) {
		var err error

		ch, err = tc.Delete(context.Background(), tx, in.repoID, key, soft, by)
		if err != nil {
			t.Fatalf("Delete(%s, soft=%v): %v", key, soft, err)
		}
	})

	return ch
}

// TestApplyUnitIsTheOnlyWriterOfTheLog checks that the log of a destination
// changes only through federation_apply_unit, not through the row import.
func TestApplyUnitIsTheOnlyWriterOfTheLog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := fedcFormats()[0]
	tc := fedcTableCodec(t, f.format)
	src := fedcNewInstance(t, f.format)
	dst := fedcNewInstance(t, f.format)

	fedcSeed(t, src, f)

	content, err := tc.Export(ctx, src.db, src.repoID, f.key)
	if err != nil || content == nil {
		t.Fatalf("Export: %v, %v", content, err)
	}

	ids := fedcEnsureBlobs(t, dst, content)

	var origin string
	if err := src.db.QueryRowContext(ctx, `SELECT origin::text FROM federation_units WHERE repo_id = $1 AND unit_key = $2`, src.repoID, f.key).Scan(&origin); err != nil {
		t.Fatalf("origin: %v", err)
	}

	fedcApply(t, dst.db, func(tx *sql.Tx) {
		if _, err := tc.Import(ctx, tx, dst.repoID, f.key, content, ids); err != nil {
			t.Fatalf("Import: %v", err)
		}

		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM federation_units WHERE repo_id = $1`, dst.repoID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("log rows after Import = %d, %v, want 0", n, err)
		}

		var applied bool

		err := tx.QueryRowContext(ctx,
			`SELECT federation_apply_unit($1::uuid, $2, $3, $4, $5, false, 1, 0, $6::uuid)`,
			dst.repoID, f.key, KindArtifact, EventArtifactAdded, content.ContentRef, origin).Scan(&applied)
		if err != nil || !applied {
			t.Fatalf("federation_apply_unit = %v, %v", applied, err)
		}
	})

	if log := loggedUnits(t, dst.db, dst.repoID); !reflect.DeepEqual(log, map[string]string{f.key: "artifact_added/false"}) {
		t.Fatalf("log after apply = %v", log)
	}

	// Without the guard a local write is captured, proving the guard is what
	// kept the import out of the log.
	if _, err := dst.db.ExecContext(ctx, `UPDATE raw_artifacts SET content_type = 'text/html' WHERE repository_id = $1`, dst.repoID); err != nil {
		t.Fatalf("local update: %v", err)
	}

	var seq int64
	if err := dst.db.QueryRowContext(ctx, `SELECT seq FROM federation_units WHERE repo_id = $1 AND unit_key = $2`, dst.repoID, f.key).Scan(&seq); err != nil || seq != 2 {
		t.Fatalf("seq after local update = %d, %v, want 2", seq, err)
	}
}

// fedcGolden pins every column of the tables the simple codecs copy. A column
// added by a migration fails TestSimpleCodecColumnDrift until it is listed here
// as replicated (travels between peers) or local (excluded in its tableSpec or
// part of alwaysLocal): a conscious decision.
//
//nolint:gochecknoglobals // golden data
var fedcGolden = map[string]struct{ replicated, local []string }{
	"raw_artifacts": {
		replicated: []string{"path", "content_type", "metadata", "created_at", "blob_id"},
		local:      []string{"id", "repository_id", "updated_at", "deleted_at", "deleted_by"},
	},
	"maven_artifacts": {
		replicated: []string{
			"group_id", "artifact_id_coord", "version", "classifier", "extension", "snapshot_timestamp",
			"snapshot_build_number", "is_snapshot", "repo_path", "created_at", "blob_id", "md5", "sha1", "sha512",
		},
		local: []string{"id", "repository_id", "updated_at", "deleted_at", "deleted_by"},
	},
	"pypi_packages": {
		replicated: []string{
			"name", "version", "filename", "package_type", "python_version", "requires_python", "summary", "author",
			"author_email", "license", "keywords", "classifiers", "created_at", "blob_id",
		},
		local: []string{"id", "repository_id", "updated_at", "deleted_at", "deleted_by"},
	},
	"helm_charts": {
		replicated: []string{
			"name", "version", "app_version", "description", "keywords", "home", "sources", "maintainers", "icon",
			"api_version", "type", "created_at", "blob_id",
		},
		local: []string{"id", "repository_id", "updated_at", "deleted_at", "deleted_by"},
	},
	"cargo_crates": {
		replicated: []string{
			"name", "version", "description", "homepage", "documentation", "repository", "license", "keywords",
			"categories", "authors", "readme", "metadata_json", "yanked", "created_at", "blob_id",
		},
		local: []string{"id", "repository_id", "download_count", "updated_at", "deleted_at", "deleted_by"},
	},
	"nuget_packages": {
		replicated: []string{
			"package_id", "version", "title", "description", "summary", "authors", "owners", "tags", "project_url",
			"license_url", "icon_url", "require_license_acceptance", "is_prerelease", "min_client_version",
			"created_at", "blob_id", "dependency_groups",
		},
		local: []string{"id", "repository_id", "download_count", "updated_at", "deleted_at", "deleted_by"},
	},
	"gomod_modules": {
		replicated: []string{"module_path", "version", "go_mod_content", "created_at", "zip_blob_id", "mod_blob_id"},
		local:      []string{"id", "repository_id", "updated_at", "deleted_at", "deleted_by"},
	},
}

func fedcSorted(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)

	return out
}

func TestSimpleCodecColumnDrift(t *testing.T) {
	t.Parallel()

	db := freshDB(t).GetDB()
	ctx := context.Background()

	specs := map[string]*tableCodec{}

	for _, f := range registeredFormats() {
		c, _ := CodecFor(f)
		if tc, ok := c.(*tableCodec); ok {
			specs[tc.spec.table] = tc
		}
	}

	for table := range specs {
		if _, ok := fedcGolden[table]; !ok {
			t.Errorf("table %s has a table codec but no golden column list: add one to fedcGolden", table)
		}
	}

	for table := range fedcGolden {
		if _, ok := specs[table]; !ok {
			t.Errorf("golden column list of %s matches no table codec", table)
		}
	}

	for table, tc := range specs {
		fedcCheckColumnDrift(ctx, t, db, table, tc)
	}
}

func fedcCheckColumnDrift(ctx context.Context, t *testing.T, db *sql.DB, table string, tc *tableCodec) {
	t.Helper()

	golden := fedcGolden[table]

	all, err := tableColumns(ctx, db, table)
	if err != nil {
		t.Fatalf("tableColumns(%s): %v", table, err)
	}

	known := slices.Concat(golden.replicated, golden.local)
	for _, col := range all {
		if !slices.Contains(known, col) {
			t.Errorf("table %s has a new column %q: decide whether it replicates (add it to fedcGolden replicated) or stays local (add it to tableSpec.exclude and fedcGolden local)", table, col)
		}
	}

	for _, col := range known {
		if !slices.Contains(all, col) {
			t.Errorf("table %s no longer has column %q listed in the golden file", table, col)
		}
	}

	// Local columns are exactly alwaysLocal plus the spec's exclusions.
	local := slices.Concat(alwaysLocal, tc.spec.exclude)
	if got, want := fedcSorted(local), fedcSorted(golden.local); !slices.Equal(got, want) {
		t.Errorf("table %s: alwaysLocal+exclude = %v, golden local = %v", table, got, want)
	}

	// What replicated() computes is the table's columns minus the local ones.
	got, err := tc.replicated(ctx, db)
	if err != nil {
		t.Fatalf("replicated(%s): %v", table, err)
	}

	var want []string

	for _, col := range all {
		if !slices.Contains(golden.local, col) {
			want = append(want, col)
		}
	}

	if !slices.Equal(fedcSorted(got), fedcSorted(want)) || !slices.Equal(fedcSorted(got), fedcSorted(golden.replicated)) {
		t.Errorf("table %s: replicated columns = %v\n want (columns minus local) = %v\n golden = %v", table, fedcSorted(got), fedcSorted(want), fedcSorted(golden.replicated))
	}

	for _, col := range tc.spec.blobCols {
		if !slices.Contains(got, col) {
			t.Errorf("table %s: blob column %s is not replicated", table, col)
		}
	}
}

// fedcImportAgain imports an already applied unit: the live row is updated in
// place (no second row), nothing is released, and a missing blob id is refused.
func fedcImportAgain(t *testing.T, dst *fedcInstance, tc *tableCodec, f fedcFormat, content *Content, ids map[string]string) {
	t.Helper()

	ctx := context.Background()

	fedcApply(t, dst.db, func(tx *sql.Tx) {
		change, err := tc.Import(ctx, tx, dst.repoID, f.key, content, ids)
		if err != nil {
			t.Fatalf("second Import: %v", err)
		}

		if !change.Replaced || change.ReplacedCount != 1 || len(change.ReleasedBlobs) != 0 {
			t.Fatalf("second Import change = %+v, want an in-place replace releasing nothing", change)
		}
	})

	if live, trashed := fedcRowCounts(t, dst, tc.spec.table); live != 1 || trashed != 0 {
		t.Fatalf("after importing twice: %d live, %d trashed rows, want 1 and 0", live, trashed)
	}

	fedcApply(t, dst.db, func(tx *sql.Tx) {
		if _, err := tc.Import(ctx, tx, dst.repoID, f.key, content, map[string]string{}); err == nil {
			t.Fatal("Import without blob ids succeeded")
		}
	})
}
