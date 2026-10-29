// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

// rowImages returns the journaled image of every row of the given tables, in a
// stable order, as the journal would write it.
func rowImages(t *testing.T, db *sql.DB, names ...string) []string {
	t.Helper()

	var out []string

	reg := NewRegistry()

	for _, name := range names {
		tbl, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("table %s is not journaled", name)
		}

		out = append(out, tableImages(t, db, tbl)...)
	}

	return out
}

func tableImages(t *testing.T, db *sql.DB, tbl *Table) []string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(),
		`SELECT `+tbl.KeyExpr("t")+` || ' ' || (to_jsonb(t) - `+tbl.ExcludeSQL()+`)::text FROM `+tbl.Name+` t ORDER BY 1`)
	if err != nil {
		t.Fatalf("row images of %s: %v", tbl.Name, err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}

		out = append(out, tbl.Name+" "+s)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func runRebuild(t *testing.T, db *sql.DB, store storage.BlobStore, opts RebuildOptions) *Report {
	t.Helper()

	opts.Concurrency = 4

	rep, err := NewRebuilder(db, store, testLog(t), opts, Hooks{}).Run(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	return rep
}

func listAll(t *testing.T, store storage.BlobStore) []string {
	t.Helper()

	var all []string

	token := ""

	for {
		ids, next, err := store.List(context.Background(), token, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		all = append(all, ids...)

		if next == "" {
			return all
		}

		token = next
	}
}

func tableReport(rep *Report, name string) TableReport {
	for _, tr := range rep.Tables {
		if tr.Table == name {
			return tr
		}
	}

	return TableReport{Table: name}
}

// seedSource builds a populated source database and drains its journal.
func seedSource(t *testing.T, store storage.BlobStore) (*sql.DB, *Journal, seededRaw, seededRaw) {
	t.Helper()

	src := newTestDB(t)
	j := newTestJournal(t, src, store)

	a := seedRaw(t, src, store, "raw-a", "dir/a.txt", []byte("alpha"))
	b := seedRaw(t, src, store, "raw-b", "b.bin", []byte("bravo"))

	insertRawFile(t, src, a.repoID, a.blobID, "trashed.txt", true)
	mustExec(t, src, `UPDATE package_files SET attrs = jsonb_set(attrs, '{metadata}', '{"k": "v"}') WHERE id = $1`, b.artifactID)
	drain(t, j)

	return src, j, a, b
}

func TestRebuildRoundTrip(t *testing.T) {
	store := newFSStore(t)
	src, j, a, _ := seedSource(t, store)

	// A row deleted before the loss must not come back.
	dead := seedRaw(t, src, store, "raw-dead", "dead.txt", []byte("dead"))
	drain(t, j)
	mustExec(t, src, `DELETE FROM package_files WHERE id = $1`, dead.artifactID)
	mustExec(t, src, `DELETE FROM repositories WHERE id = $1`, dead.repoID)
	drain(t, j)

	tables := []string{"repositories", "blobs", "packages", "package_versions", "package_files"}
	want := rowImages(t, src, tables...)

	dst := newTestDB(t)

	dry := runRebuild(t, dst, store, RebuildOptions{DryRun: true})
	if dry.ExitCode() != ExitOK || tableReport(dry, "package_files").Inserted != 3 {
		t.Fatalf("dry run report = %+v", dry.Tables)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM package_files`) + mustCount(t, dst, `SELECT count(*) FROM recovery_sidecars`); n != 0 {
		t.Fatalf("dry run wrote %d rows", n)
	}

	rep := runRebuild(t, dst, store, RebuildOptions{})
	if rep.ExitCode() != ExitOK {
		t.Fatalf("exit code %d: %+v %+v", rep.ExitCode(), rep.Failures, rep.Unrecoverable)
	}

	if got := rowImages(t, dst, tables...); !slices.Equal(got, want) {
		t.Fatalf("rebuilt rows differ\n got: %q\nwant: %q", got, want)
	}

	if !slices.Equal(rep.ReposRecovered, []string{"raw-a", "raw-b"}) {
		t.Fatalf("repositories recovered = %v", rep.ReposRecovered)
	}

	if trashed := mustCount(
		t,
		dst,
		`SELECT count(*) FROM package_files WHERE deleted_at IS NOT NULL AND repository_id = $1`,
		a.repoID,
	); trashed != 1 {
		t.Fatalf("trashed row restored as live (trashed=%d)", trashed)
	}

	// Idempotent: nothing new is inserted by a second run.
	again := runRebuild(t, dst, store, RebuildOptions{})
	if tr := tableReport(again, "package_files"); tr.Inserted != 0 || tr.AlreadyPresent != 3 || again.ExitCode() != ExitOK {
		t.Fatalf("second run = %+v", again.Tables)
	}

	// The restored rows are in step with their sidecars: draining the new
	// database writes and deletes nothing.
	before := listAll(t, store)

	jd := newTestJournal(t, dst, store)
	described := describeSidecars(t, jd, store, before)

	drain(t, jd)

	after := listAll(t, store)

	maps.Copy(described, describeSidecars(t, jd, store, after))

	// Every fresh database seeds its own system project (own timestamps), so
	// the sidecar of that one row is rewritten by the drain; nothing else is.
	systemProject := `projects {"id":"` + systemProjectID + `"`
	churn := func(ids []string, other []string) []string {
		return slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
			return slices.Contains(other, id) || strings.HasPrefix(described[id], systemProject)
		})
	}

	if added, removed := churn(after, before), churn(before, after); len(added) != 0 || len(removed) != 0 {
		t.Fatalf("draining the rebuilt database changed the sidecars, want none\n added: %v\n removed: %v",
			mapValues(described, added), mapValues(described, removed))
	}
}

func mapValues(m map[string]string, keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}

	return out
}

// describeSidecars maps sidecar ids to "table row" lines for a failure message.
func describeSidecars(t *testing.T, j *Journal, store storage.BlobStore, ids []string) map[string]string {
	t.Helper()

	out := make(map[string]string, len(ids))

	for _, id := range ids {
		rc, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get sidecar %s: %v", id, err)
		}

		data, _ := io.ReadAll(rc)
		_ = rc.Close()

		if !LooksLikeSidecar(data) { // a content blob
			out[id] = "blob " + id

			continue
		}

		sc, tbl, err := j.reg.DecodeSidecar(id, data)
		if err != nil {
			t.Fatalf("decode sidecar %s: %v", id, err)
		}

		out[id] = tbl.Name + " " + string(sc.Row)
	}

	return out
}

func TestRebuildMissingBlobIsUnrecoverable(t *testing.T) {
	store := newFSStore(t)
	_, _, a, b := seedSource(t, store)

	if err := store.Delete(context.Background(), a.sha); err != nil {
		t.Fatal(err)
	}

	dst := newTestDB(t)
	rep := runRebuild(t, dst, store, RebuildOptions{})

	if rep.ExitCode() != ExitIncomplete || rep.MissingBlobCount != 1 {
		t.Fatalf("exit=%d missing=%d unrecoverable=%+v", rep.ExitCode(), rep.MissingBlobCount, rep.Unrecoverable)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM package_files WHERE blob_id = $1`, a.blobID); n != 0 {
		t.Fatalf("rows of a blob with no bytes were inserted (%d)", n)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM package_files WHERE id = $1`, b.artifactID); n != 1 {
		t.Fatal("the intact row was not restored")
	}
}

// fileRow is the journaled image of a raw package_files row of s (hand-made:
// only the columns without a default), with id and path given.
func fileRow(s seededRaw, id, path, attrs string) string {
	return `{"id":"` + id + `","version_id":"` + s.versionID + `","repository_id":"` + s.repoID + `","path":"` + path +
		`","filename":"` + path + `","blob_id":"` + s.blobID + `","attrs":` + attrs + `}`
}

// putSidecar stores a hand-made sidecar of the package_files row with the given key.
func putSidecar(t *testing.T, store storage.BlobStore, key string, at time.Time, row string, marker bool) string {
	t.Helper()

	tbl, ok := NewRegistry().Lookup("package_files")
	if !ok {
		t.Fatal("package_files is not journaled")
	}

	data, sha, err := EncodeSidecar(tbl, key, SchemaVersion(), at, []byte(row))
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]string{"sha256": sha}
	if marker {
		meta = SidecarMetadata(tbl.Name, sha)
	}

	if _, err := store.Put(context.Background(), bytes.NewReader(data), meta); err != nil {
		t.Fatal(err)
	}

	return sha
}

func TestRebuildSupersededAndDeepAndInvalid(t *testing.T) {
	store := newFSStore(t)
	src, _, a, _ := seedSource(t, store)
	_ = src

	row := func(ct string) string {
		return fileRow(a, a.artifactID, "dir/a.txt", `{"content_type":"`+ct+`","metadata":{}}`)
	}

	// Two extra sidecars of the same row (as left by a crash or two writers):
	// the newest written_at wins.
	old := putSidecar(t, store, a.artifactID, time.Now().Add(-time.Hour), row("old/type"), true)
	newest := putSidecar(t, store, a.artifactID, time.Now().Add(time.Hour), row("new/type"), true)

	// A sidecar that lost its marker is only found with --deep.
	lost := putSidecar(
		t,
		store,
		"11111111-1111-1111-1111-111111111111",
		time.Now(),
		strings.Replace(row("x/y"), a.artifactID, "11111111-1111-1111-1111-111111111111", 1),
		false,
	)

	// A marked blob whose content is not a sidecar is invalid.
	badSum := sha256.Sum256([]byte("garbage"))
	badID, err := store.Put(
		context.Background(),
		strings.NewReader("garbage"),
		SidecarMetadata("package_files", hex.EncodeToString(badSum[:])),
	)
	if err != nil {
		t.Fatal(err)
	}

	plain := runRebuild(t, newTestDB(t), store, RebuildOptions{DryRun: true})
	if plain.Superseded < 2 || plain.SidecarsInvalid != 1 || plain.ExitCode() != ExitFailed {
		t.Fatalf("plain: superseded=%d invalid=%d exit=%d", plain.Superseded, plain.SidecarsInvalid, plain.ExitCode())
	}

	if n := tableReport(plain, "package_files").Found; n != 3 {
		t.Fatalf("found %d raw rows without --deep, want 3 (the unmarked sidecar must stay hidden)", n)
	}

	deep := runRebuild(t, newTestDB(t), store, RebuildOptions{DryRun: true, Deep: true})
	if n := tableReport(deep, "package_files").Found; n != 4 {
		t.Fatalf("found %d raw rows with --deep, want 4", n)
	}

	dst := newTestDB(t)

	rep := runRebuild(t, dst, store, RebuildOptions{PruneSuperseded: true})
	if got := mustString(t, dst, `SELECT attrs->>'content_type' FROM package_files WHERE id = $1`, a.artifactID); got != "new/type" {
		t.Fatalf("content_type = %q, want the newest sidecar's", got)
	}

	if rep.Pruned < 2 {
		t.Fatalf("pruned %d, want at least 2", rep.Pruned)
	}

	for _, id := range []string{old} {
		if blobExists(t, store, id) {
			t.Fatalf("superseded sidecar %s survived --prune-superseded", id)
		}
	}

	for _, id := range []string{newest, badID, lost} {
		if !blobExists(t, store, id) {
			t.Fatalf("blob %s was deleted", id)
		}
	}
}

func TestRebuildRequiresListing(t *testing.T) {
	_, err := NewRebuilder(nil, noListStore{}, testLog(t), RebuildOptions{}, Hooks{}).Run(context.Background())
	if !errors.Is(err, ErrListUnsupported) {
		t.Fatalf("err = %v, want ErrListUnsupported", err)
	}
}

type noListStore struct{ storage.BlobStore }

func (noListStore) List(context.Context, string, int) ([]string, string, error) {
	return nil, "", storage.ErrListNotSupported
}

// TestRebuildRestoresSideTableRows: a format's side table (here pacman_files)
// is journaled and rebuilt after its package_files parent, without the
// columns the descriptor excludes.
func TestRebuildRestoresSideTableRows(t *testing.T) {
	store := newFSStore(t)
	src := newTestDB(t)
	j := newTestJournal(t, src, store)

	a := seedRaw(t, src, store, "pac", "x.pkg.tar.zst", []byte("pacman package"))
	mustExec(t, src, `UPDATE packages SET format = 'pacman' WHERE id = $1`, a.packageID)
	mustExec(t, src,
		`INSERT INTO pacman_files (id, file_list, signature, signature_source) VALUES ($1, 'usr/bin/x', 'sig', 'uploader')`,
		a.artifactID)
	drain(t, j)

	if trackedSha(t, j, "pacman_files", a.artifactID) == "" {
		t.Fatal("the side-table row has no sidecar")
	}

	if sc := readTrackedSidecar(t, j, store, "pacman_files", a.artifactID); strings.Contains(string(sc.Row), "file_list") ||
		!strings.Contains(string(sc.Row), `"signature":"sig"`) {
		t.Fatalf("pacman_files image = %s, want signature and no file_list", sc.Row)
	}

	dst := newTestDB(t)

	rep := runRebuild(t, dst, store, RebuildOptions{})
	if rep.ExitCode() != ExitOK || tableReport(rep, "pacman_files").Inserted != 1 {
		t.Fatalf("exit %d, report %+v, %+v %+v", rep.ExitCode(), rep.Tables, rep.Failures, rep.Unrecoverable)
	}

	const restored = `SELECT signature || '|' || signature_source || '|' || COALESCE(file_list, 'NULL') FROM pacman_files WHERE id = $1`

	if got := mustString(t, dst, restored, a.artifactID); got != "sig|uploader|NULL" {
		t.Fatalf("restored side row = %q, want the journaled columns and a NULL file_list", got)
	}

	// Cascade: deleting the file takes its side row; the journal follows.
	mustExec(t, dst, `DELETE FROM package_files WHERE id = $1`, a.artifactID)
	drain(t, newTestJournal(t, dst, store))

	if n := mustCount(t, dst, `SELECT count(*) FROM recovery_sidecars WHERE table_name = 'pacman_files'`); n != 0 {
		t.Fatalf("%d sidecars of a cascade-deleted side row survived", n)
	}
}

// TestRebuildTouchesPackagesAndRepositories: the Regenerate hook receives the
// repositories that got rows and, of the npm packages among the restored
// packages (rows, versions or tags), their ids, found through the generic tables.
func TestRebuildTouchesPackagesAndRepositories(t *testing.T) {
	store := newFSStore(t)
	src := newTestDB(t)
	j := newTestJournal(t, src, store)

	a := seedRaw(t, src, store, "npmrepo", "left-pad", []byte("tarball"))
	mustExec(t, src, `UPDATE repositories SET format = 'npm' WHERE id = $1`, a.repoID)
	mustExec(t, src, `UPDATE packages SET format = 'npm' WHERE id = $1`, a.packageID)
	mustExec(
		t,
		src,
		`INSERT INTO package_tags (package_id, tag, version, unit_key) VALUES ($1, 'latest', '', 'left-pad#latest')`,
		a.packageID,
	)

	other := seedRaw(t, src, store, "rawrepo", "r.txt", []byte("raw"))
	drain(t, j)

	var (
		repos []string
		npm   []string
	)

	hooks := Hooks{Regenerate: func(_ context.Context, r, n []string) error { repos, npm = r, n; return nil }}

	rep, err := NewRebuilder(newTestDB(t), store, testLog(t), RebuildOptions{Concurrency: 2}, hooks).Run(context.Background())
	if err != nil || rep.ExitCode() != ExitOK {
		t.Fatalf("rebuild: %v, exit %d, %+v %+v", err, rep.ExitCode(), rep.Failures, rep.Unrecoverable)
	}

	if !slices.Equal(repos, sortedStrings(a.repoID, other.repoID)) || !slices.Equal(npm, []string{a.packageID}) {
		t.Fatalf(
			"regenerate got repositories %v and npm packages %v; want both repositories and only the npm package %s",
			repos,
			npm,
			a.packageID,
		)
	}
}
