// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/storage"
)

// TestRebuildIsMergeOnly: a rebuild into a database that already holds rows
// never changes or removes them, even when the sidecars say otherwise.
func TestRebuildIsMergeOnly(t *testing.T) {
	store := newFSStore(t)
	_, _, a, b := seedSource(t, store)

	dst := newTestDB(t)
	runRebuild(t, dst, store, RebuildOptions{})

	mustExec(t, dst, `UPDATE raw_artifacts SET content_type = 'edited/after' WHERE id = $1`, a.artifactID)
	mustExec(t, dst, `DELETE FROM raw_artifacts WHERE id = $1`, b.artifactID)

	extra := seedRaw(t, dst, store, "raw-only-here", "only.txt", []byte("only here"))

	rep := runRebuild(t, dst, store, RebuildOptions{})
	if rep.ExitCode() != ExitOK {
		t.Fatalf("exit %d: %+v %+v", rep.ExitCode(), rep.Failures, rep.Unrecoverable)
	}

	if got := mustString(t, dst, `SELECT content_type FROM raw_artifacts WHERE id = $1`, a.artifactID); got != "edited/after" {
		t.Errorf("content_type = %q: the rebuild overwrote an existing row", got)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE id = $1`, b.artifactID); n != 1 {
		t.Errorf("the deleted row was not merged back (count %d)", n)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE id = $1`, extra.artifactID); n != 1 {
		t.Errorf("a row without a sidecar was removed (count %d)", n)
	}

	if tr := tableReport(rep, "raw_artifacts"); tr.Inserted != 1 || tr.AlreadyPresent != 2 {
		t.Errorf("raw_artifacts report = %+v, want 1 inserted and 2 already present", tr)
	}
}

// TestSidecarsNeverCarrySecrets: an encrypted upstream password is journaled
// nowhere in the store, and a rebuilt repository comes back without it.
func TestSidecarsNeverCarrySecrets(t *testing.T) {
	const secret = "enc:v1:TOP-SECRET-CIPHERTEXT"

	store := newFSStore(t)
	src := newTestDB(t)
	j := newTestJournal(t, src, store)

	a := seedRaw(t, src, store, "proxied", "p.txt", []byte("payload"))
	mustExec(t, src, `UPDATE repositories SET upstream_username = 'bob', upstream_password_enc = $2 WHERE id = $1`, a.repoID, secret)
	drain(t, j)

	for _, id := range listAll(t, store) {
		rc, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}

		data, err := io.ReadAll(rc)
		_ = rc.Close()

		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}

		if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte("upstream_password_enc")) {
			t.Fatalf("object %s carries the repository secret", id)
		}
	}

	dst := newTestDB(t)
	runRebuild(t, dst, store, RebuildOptions{})

	if got := mustString(t, dst, `SELECT COALESCE(upstream_password_enc, '') FROM repositories WHERE id = $1`, a.repoID); got != "" {
		t.Errorf("rebuilt repository has upstream_password_enc %q", got)
	}

	if got := mustString(t, dst, `SELECT upstream_username FROM repositories WHERE id = $1`, a.repoID); got != "bob" {
		t.Errorf("upstream_username = %q, want the non-secret column restored", got)
	}
}

// publicHashColumns are journaled columns whose name matches the secret
// pattern but hold public content hashes of a Nix store path.
var publicHashColumns = map[string]bool{ //nolint:gochecknoglobals // immutable test allowlist
	"nix_paths.store_hash": true,
	"nix_paths.nar_hash":   true,
}

// TestRegistryExcludesSecretLookingColumns: no journaled table may carry a
// column that looks like a credential unless the registry excludes it.
func TestRegistryExcludesSecretLookingColumns(t *testing.T) {
	db := newTestDB(t)

	for i := range Tables {
		tbl := &Tables[i]

		for _, col := range queryStrings(t, db, `
			SELECT column_name FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = $1
			   AND column_name ~ '(password|passwd|secret|credential|private|_enc$|_hash$|api_key|(^|_)token$)'`, tbl.Name) {
			if !tbl.isExcluded(col) && !publicHashColumns[tbl.Name+"."+col] {
				t.Errorf("%s.%s looks like a secret but is journaled", tbl.Name, col)
			}
		}
	}
}

// TestRebuildOrphanIsUnrecoverable: a row whose parent sidecar is gone is
// reported (not failed) and is not inserted; the rest of the data is.
func TestRebuildOrphanIsUnrecoverable(t *testing.T) {
	store := newFSStore(t)
	src, j, a, b := seedSource(t, store)
	_ = src

	if err := store.Delete(context.Background(), trackedSha(t, j, "repositories", a.repoID)); err != nil {
		t.Fatal(err)
	}

	dst := newTestDB(t)

	rep := runRebuild(t, dst, store, RebuildOptions{})
	if rep.ExitCode() != ExitIncomplete {
		t.Fatalf("exit %d, want %d: %+v %+v", rep.ExitCode(), ExitIncomplete, rep.Failures, rep.Unrecoverable)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE repository_id = $1`, a.repoID); n != 0 {
		t.Errorf("%d rows of a repository without a sidecar were inserted", n)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE id = $1`, b.artifactID); n != 1 {
		t.Errorf("the intact repository was not restored")
	}

	if tr := tableReport(rep, "raw_artifacts"); tr.Unrecoverable != 2 || tr.Failed != 0 {
		t.Errorf("raw_artifacts report = %+v, want the 2 orphans unrecoverable and none failed", tr)
	}
}

// TestRebuildCollidingRowIsUnrecoverable: a sidecar whose coordinates are held
// by a different live row is left out and the existing row stays.
func TestRebuildCollidingRowIsUnrecoverable(t *testing.T) {
	store := newFSStore(t)
	a := seedFirst(t, store)

	dst := newTestDB(t)
	runRebuild(t, dst, store, RebuildOptions{})

	ghost := uuid.NewString()
	row := `{"id":"` + ghost + `","repository_id":"` + a.repoID + `","path":"dir/a.txt","blob_id":"` + a.blobID + `"}`
	putSidecar(t, store, ghost, time.Now(), row, true)

	rep := runRebuild(t, dst, store, RebuildOptions{})
	if rep.ExitCode() != ExitIncomplete {
		t.Fatalf("exit %d, want %d: %+v %+v", rep.ExitCode(), ExitIncomplete, rep.Failures, rep.Unrecoverable)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE id = $1`, ghost); n != 0 {
		t.Errorf("the colliding row was inserted")
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE id = $1`, a.artifactID); n != 1 {
		t.Errorf("the existing row is gone")
	}
}

// TestRebuildIgnoresUnknownColumns: keys of a row image that are not columns of
// the live table (a dropped column, or a hostile name) never reach the SQL, and
// columns the image lacks keep their defaults.
func TestRebuildIgnoresUnknownColumns(t *testing.T) {
	store := newFSStore(t)
	a := seedFirst(t, store)

	dst := newTestDB(t)
	runRebuild(t, dst, store, RebuildOptions{})

	id := uuid.NewString()
	row := `{"id":"` + id + `","repository_id":"` + a.repoID + `","path":"new.txt","blob_id":"` + a.blobID + `",` +
		`"dropped_column":"x","path\"); DROP TABLE raw_artifacts; --":"y"}`
	putSidecar(t, store, id, time.Now(), row, true)

	rep := runRebuild(t, dst, store, RebuildOptions{})
	if rep.ExitCode() != ExitOK {
		t.Fatalf("exit %d: %+v %+v", rep.ExitCode(), rep.Failures, rep.Unrecoverable)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE id = $1 AND path = 'new.txt' AND deleted_at IS NULL`, id); n != 1 {
		t.Errorf("the row with unknown columns was not restored (count %d)", n)
	}
}

// TestRebuildSkipsUnsupportedSidecars: a sidecar of a newer format or of a
// table this release does not journal is skipped and counted, not a failure.
func TestRebuildSkipsUnsupportedSidecars(t *testing.T) {
	store := newFSStore(t)
	meta := func(doc string) {
		t.Helper()

		sum := sha([]byte(doc))
		if _, err := store.Put(context.Background(), strings.NewReader(doc), SidecarMetadata("raw_artifacts", sum)); err != nil {
			t.Fatal(err)
		}
	}

	meta(`{"openartifact_sidecar":2,"table":"raw_artifacts","key":"` + testID + `","row":` + testRow + `}`)
	meta(`{"openartifact_sidecar":1,"table":"auth_users","key":"` + testID + `","row":` + testRow + `}`)

	rep := runRebuild(t, newTestDB(t), store, RebuildOptions{})
	if rep.SidecarsUnsupported != 2 || rep.SidecarsInvalid != 0 || rep.SidecarsValid != 0 || rep.ExitCode() != ExitOK {
		t.Fatalf(
			"unsupported=%d invalid=%d valid=%d exit=%d",
			rep.SidecarsUnsupported,
			rep.SidecarsInvalid,
			rep.SidecarsValid,
			rep.ExitCode(),
		)
	}
}

// TestRebuildWarnsWhenNoSidecarIsMarked: a store full of objects without any
// sidecar marker is flagged, since it usually means the wrong store.
func TestRebuildWarnsWhenNoSidecarIsMarked(t *testing.T) {
	store := newFSStore(t)
	if _, err := store.Put(
		context.Background(),
		strings.NewReader("plain blob"),
		map[string]string{"sha256": sha([]byte("plain blob"))},
	); err != nil {
		t.Fatal(err)
	}

	rep := runRebuild(t, newTestDB(t), store, RebuildOptions{})
	if len(rep.Warnings) != 1 || rep.ExitCode() != ExitOK {
		t.Fatalf("warnings = %v, exit = %d", rep.Warnings, rep.ExitCode())
	}

	if rep.UnreferencedBlobs != 1 {
		t.Errorf("unreferenced blobs = %d, want 1", rep.UnreferencedBlobs)
	}
}

// TestRebuildHooks: the post-restore steps run only on a real run, with the
// repositories that received rows, and a failing step fails the run.
func TestRebuildHooks(t *testing.T) {
	store := newFSStore(t)
	_, _, a, b := seedSource(t, store)

	var (
		usage      int
		regenRepos []string
	)

	hooks := Hooks{
		ReconcileUsage: func(context.Context) error { usage++; return nil },
		Regenerate: func(_ context.Context, repos, _ []string) error {
			regenRepos = repos
			return nil
		},
	}

	run := func(dry bool, h Hooks) *Report {
		t.Helper()

		rep, err := NewRebuilder(newTestDB(t), store, testLog(t), RebuildOptions{DryRun: dry, Concurrency: 2}, h).Run(context.Background())
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}

		return rep
	}

	if run(true, hooks); usage != 0 || regenRepos != nil {
		t.Fatalf("a dry run called the hooks (usage=%d repos=%v)", usage, regenRepos)
	}

	rep := run(false, hooks)
	if usage != 1 || !slices.Equal(regenRepos, sortedStrings(a.repoID, b.repoID)) {
		t.Errorf("usage=%d regenerated=%v, want 1 and both repositories", usage, regenRepos)
	}

	if !slices.Equal(rep.PostSteps, []string{"storage usage", "metadata regeneration"}) {
		t.Errorf("post steps = %v", rep.PostSteps)
	}

	failing := Hooks{
		ReconcileUsage: func(context.Context) error { return errors.New("usage broke") },
		Regenerate:     func(context.Context, []string, []string) error { return errors.New("regen broke") },
	}

	rep = run(false, failing)
	if rep.ExitCode() != ExitFailed || rep.FailureCount != 2 || len(rep.PostSteps) != 0 {
		t.Errorf("exit=%d failures=%d steps=%v, want both hook errors reported", rep.ExitCode(), rep.FailureCount, rep.PostSteps)
	}
}

func sortedStrings(s ...string) []string {
	slices.Sort(s)

	return s
}

// TestRebuildStopsWhenCancelled: an interrupted rebuild returns an error
// instead of reporting a partial success.
func TestRebuildStopsWhenCancelled(t *testing.T) {
	store := newFSStore(t)
	seedSource(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewRebuilder(newTestDB(t), store, testLog(t), RebuildOptions{Concurrency: 2}, Hooks{}).Run(ctx); err == nil {
		t.Fatal("Run on a cancelled context succeeded")
	}
}

// seedFirst seeds a source and returns only its first repository's rows.
func seedFirst(t *testing.T, store storage.BlobStore) seededRaw {
	t.Helper()

	db, j, a, b := seedSource(t, store)
	if db == nil || j == nil || b.repoID == "" {
		t.Fatal("seedSource returned an incomplete seed")
	}

	return a
}
