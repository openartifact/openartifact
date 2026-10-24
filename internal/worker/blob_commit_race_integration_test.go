// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/storage"
)

// hookStore is the blob store of a commit race test: it wraps the in-memory
// store and lets a test act at exact points of storage.CommitBlob.
type hookStore struct {
	*formattest.BlobStore

	mu sync.Mutex
	// afterFirstLookup runs once, right after the first existence check (the one
	// before the lock), with its answer already decided.
	afterFirstLookup func()
	// afterPut runs once, right after the first Put returned.
	afterPut func()
	// inPut runs at the start of every Put.
	inPut func()

	lookups, puts int
}

func (s *hookStore) ExistsBySHA256(ctx context.Context, sha string) (string, bool, error) {
	key, ok, err := s.BlobStore.ExistsBySHA256(ctx, sha)

	s.mu.Lock()
	s.lookups++
	first := s.lookups == 1
	hook := s.afterFirstLookup
	s.mu.Unlock()

	if first && hook != nil {
		hook()
	}

	return key, ok, err
}

func (s *hookStore) Put(ctx context.Context, data io.Reader, meta map[string]string) (string, error) {
	s.mu.Lock()
	s.puts++
	n := s.puts
	in := s.inPut
	s.mu.Unlock()

	if in != nil {
		in()
	}

	key, err := s.BlobStore.Put(ctx, data, meta)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	hook := s.afterPut
	s.mu.Unlock()

	if n == 1 && hook != nil {
		hook()
	}

	return key, nil
}

func (s *hookStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.puts
}

func contentSHA(content string) string {
	sum := sha256.Sum256([]byte(content))

	return hex.EncodeToString(sum[:])
}

// commitUpload is what an upload handler does with CommitBlob: it stores the
// content and, under the lock, registers the blobs row and a maven row of
// repoID that references it.
func (s *purgeSeed) commitUpload(ctx context.Context, store storage.BlobStore, repoID, content string) error {
	sha := contentSHA(content)

	return storage.CommitBlob(ctx, s.pg, store, storage.BytesBlob(sha, []byte(content), nil),
		func(ctx context.Context, key string) error {
			blobID, err := s.pg.UpsertBlob(ctx, sha, key, int64(len(content)))
			if err != nil {
				return fmt.Errorf("upsert blob: %w", err)
			}

			n := rand.Text()

			_, err = s.pg.GetDB().ExecContext(ctx,
				`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id)
				 VALUES ($1, 'g', $2, '1.0', 'jar', $3, $4)`,
				repoID, "c"+n, fmt.Sprintf("g/c%[1]s/1.0/c%[1]s-1.0.jar", n), blobID)
			if err != nil {
				return fmt.Errorf("insert row: %w", err)
			}

			return nil
		})
}

// unpublishRows deletes every maven row that references sha and frees the blob
// the way a delete does: SafeDeleteBlob (which only queues the bytes).
func (s *purgeSeed) unpublishRows(ctx context.Context, store storage.BlobStore, sha string) {
	s.t.Helper()

	s.exec(`DELETE FROM maven_artifacts WHERE blob_id IN (SELECT id FROM blobs WHERE sha256 = $1)`, sha)

	if err := storage.SafeDeleteBlob(ctx, s.pg, store, sha); err != nil {
		s.t.Fatalf("SafeDeleteBlob: %v", err)
	}
}

func (s *purgeSeed) rowsOf(sha string) int {
	s.t.Helper()

	return s.count(`SELECT COUNT(*) FROM maven_artifacts m JOIN blobs b ON b.id = m.blob_id WHERE b.sha256 = $1`, sha)
}

func (s *purgeSeed) hasObject(sha string) bool {
	s.t.Helper()

	ok, err := s.blobs.Exists(context.Background(), sha)
	if err != nil {
		s.t.Fatalf("exists: %v", err)
	}

	return ok
}

// requireConsistent fails unless blob sha satisfies, with no sweep or upload
// in between: a referenced blob has its object and its row.
func (s *purgeSeed) requireConsistent(sha, when string) {
	s.t.Helper()

	if s.rowsOf(sha) == 0 {
		return
	}

	if !s.hasObject(sha) {
		s.t.Errorf("%s: a row references blob %s but its object is gone", when, sha)
	}

	if !s.blobRowExists(sha) {
		s.t.Errorf("%s: a row references blob %s but its blobs row is gone", when, sha)
	}
}

// The deferred-deletion sweeper removes the object of a blob a delete just
// freed between the uploader's existence check and its lock: the upload must
// store the object again before it commits its row.
func TestCommitBlobRestoresObjectRemovedByTheSweeperBeforeTheLock(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()
	repo := s.repo("commit-sweeper-race")
	store := &hookStore{BlobStore: s.blobs}

	content := "bytes a delete frees while an identical upload is in flight"
	blobID, sha := s.blob(content, 2*time.Hour)
	s.maven(repo, blobID)

	store.afterFirstLookup = func() {
		s.unpublishRows(ctx, s.blobs, sha)
		s.sweep() // the object is deleted for real

		if s.hasObject(sha) {
			t.Error("setup: the sweeper did not remove the object")
		}
	}

	if err := s.commitUpload(ctx, store, repo, content); err != nil {
		t.Fatalf("commitUpload: %v", err)
	}

	if store.putCount() != 1 {
		t.Errorf("Put calls = %d; want one repair Put", store.putCount())
	}

	s.requireConsistent(sha, "after the commit")

	if s.rowsOf(sha) != 1 {
		t.Fatalf("rows referencing the blob = %d, want 1", s.rowsOf(sha))
	}

	s.sweep()
	s.requireConsistent(sha, "after a later sweep")
}

// The delete is queued before the commit but the sweeper only runs after it:
// the sweeper must see the new row and keep the bytes.
func TestCommitBlobKeepsObjectWhenTheSweeperRunsAfterTheCommit(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()
	repo := s.repo("commit-sweeper-after")
	store := &hookStore{BlobStore: s.blobs}

	content := "bytes queued for deletion while an identical upload commits"
	blobID, sha := s.blob(content, 2*time.Hour)
	s.maven(repo, blobID)

	store.afterFirstLookup = func() { s.unpublishRows(ctx, s.blobs, sha) }

	if err := s.commitUpload(ctx, store, repo, content); err != nil {
		t.Fatalf("commitUpload: %v", err)
	}

	if store.putCount() != 0 {
		t.Errorf("Put calls = %d; the object was never missing", store.putCount())
	}

	s.sweep()
	s.requireConsistent(sha, "after the sweep")

	if n := s.count(`SELECT COUNT(*) FROM blob_deletions WHERE sha256 = $1`, sha); n != 0 {
		t.Errorf("%d deletions of a referenced blob still queued", n)
	}

	if s.rowsOf(sha) != 1 {
		t.Fatalf("rows referencing the blob = %d, want 1", s.rowsOf(sha))
	}
}

// A new blob is written before the lock; a delete of identical earlier content
// (queued, then swept) removes the object right after that Put. The commit must
// put it back.
func TestCommitBlobRestoresObjectRemovedRightAfterItsPut(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()
	repo := s.repo("commit-after-put")
	store := &hookStore{BlobStore: s.blobs}

	content := "fresh bytes whose object a stale queued deletion removes"
	sha := contentSHA(content)

	// A deletion of the same content is already queued (the previous owner
	// of identical bytes was deleted a moment ago).
	s.exec(`INSERT INTO blob_deletions (sha256, scope, not_before) VALUES ($1, '*', now())`, sha)

	store.afterPut = func() {
		s.sweep() // deletes the bytes just written: nothing references them yet

		if s.hasObject(sha) {
			t.Error("setup: the sweeper did not remove the object")
		}
	}

	if err := s.commitUpload(ctx, store, repo, content); err != nil {
		t.Fatalf("commitUpload: %v", err)
	}

	if store.putCount() != 2 {
		t.Errorf("Put calls = %d; want the first Put and the repair Put", store.putCount())
	}

	s.requireConsistent(sha, "after the commit")

	if s.rowsOf(sha) != 1 {
		t.Fatalf("rows referencing the blob = %d, want 1", s.rowsOf(sha))
	}
}

// Uploads are not capped by the lock slots: every one of them is inside the
// blob store at the same time, and none holds a database connection while
// there. (With the Put inside the lock, only MaxOpenConns/2 could be.)
func TestCommitBlobDoesNotHoldADatabaseConnectionDuringThePut(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()
	repo := s.repo("commit-concurrency")

	const uploads = 16 // far above the lock slots of the 8-connection test pool

	var (
		inside  atomic.Int32
		release = make(chan struct{})
		allIn   = make(chan struct{})
		once    sync.Once
	)

	store := &hookStore{BlobStore: s.blobs}
	store.inPut = func() {
		if inside.Add(1) == uploads {
			once.Do(func() { close(allIn) })
		}

		<-release
	}

	errs := make(chan error, uploads)

	for i := range uploads {
		go func() { errs <- s.commitUpload(ctx, store, repo, fmt.Sprintf("slow upload %d", i)) }()
	}

	select {
	case <-allIn:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatalf("only %d of %d uploads reached the blob store: uploads are capped below the pool size", inside.Load(), uploads)
	}

	if inUse := s.pg.GetDB().Stats().InUse; inUse != 0 {
		t.Errorf("database connections in use during the Puts = %d, want 0", inUse)
	}

	close(release)

	for range uploads {
		if err := <-errs; err != nil {
			t.Errorf("commitUpload: %v", err)
		}
	}

	if n := s.count(`SELECT COUNT(*) FROM maven_artifacts WHERE repository_id = $1`, repo); n != uploads {
		t.Errorf("rows = %d, want %d", n, uploads)
	}
}

// Uploads of the same contents race deletes of them and a running sweeper. At
// every moment a referenced blob has its object, and when the dust settles
// every referenced blob is complete (object and row) and, once its last
// reference is removed, nothing of it is left behind.
func TestCommitBlobVersusDeleteAndSweepStress(t *testing.T) {
	s := newPurgeSeed(t)
	ctx := context.Background()
	repo := s.repo("commit-stress")
	store := &hookStore{BlobStore: s.blobs}

	contents := []string{"stress content one", "stress content two", "stress content three"}

	shas := make([]string, len(contents))
	for i, c := range contents {
		shas[i] = contentSHA(c)
	}

	const (
		uploaders = 4
		deleters  = 3
		rounds    = 25
	)

	var (
		wg      sync.WaitGroup
		stop    = make(chan struct{})
		bgWG    sync.WaitGroup
		failure atomic.Value
	)

	fail := func(format string, args ...any) {
		failure.CompareAndSwap(nil, fmt.Sprintf(format, args...))
	}

	// The sweeper and the invariant checker run until the writers are done.
	bgWG.Go(func() { s.sweepUntil(t, ctx, stop, fail) })
	bgWG.Go(func() { s.checkInvariantUntil(ctx, stop, shas, fail) })

	for u := range uploaders {
		wg.Go(func() {
			for r := range rounds {
				if err := s.commitUpload(ctx, store, repo, contents[(u+r)%len(contents)]); err != nil {
					fail("upload: %v", err)

					return
				}
			}
		})
	}

	for d := range deleters {
		wg.Go(func() {
			for r := range rounds {
				sha := shas[(d+r)%len(shas)]

				if _, err := s.pg.GetDB().ExecContext(ctx,
					`DELETE FROM maven_artifacts WHERE id = (
						SELECT m.id FROM maven_artifacts m JOIN blobs b ON b.id = m.blob_id WHERE b.sha256 = $1 LIMIT 1)`, sha); err != nil {
					fail("delete row: %v", err)

					return
				}

				if err := storage.SafeDeleteBlob(ctx, s.pg, s.blobs, sha); err != nil {
					fail("SafeDeleteBlob: %v", err)

					return
				}
			}
		})
	}

	wg.Wait()
	close(stop)
	bgWG.Wait()

	if msg, _ := failure.Load().(string); msg != "" {
		t.Fatal(msg)
	}

	s.sweep()

	for _, sha := range shas {
		s.requireConsistent(sha, "after the run")
	}

	// Free everything: no object or row may be left behind.
	for _, sha := range shas {
		s.unpublishRows(ctx, s.blobs, sha)
	}

	s.sweep()

	for _, sha := range shas {
		if s.hasObject(sha) || s.blobRowExists(sha) {
			t.Errorf("blob %s left behind after every reference was removed (object %v, row %v)", sha, s.hasObject(sha), s.blobRowExists(sha))
		}
	}

	if n := s.count(`SELECT COUNT(*) FROM blob_deletions`); n != 0 {
		t.Errorf("deletions still queued: %d", n)
	}
}

// sweepUntil runs the deletion sweeper in a loop until stop is closed.
func (s *purgeSeed) sweepUntil(t *testing.T, ctx context.Context, stop <-chan struct{}, fail func(string, ...any)) {
	t.Helper()

	sw := NewBlobDeletionSweeper(s.pg, copyDeleter{s.blobs}, testLogger(t), 0, 100)

	for {
		select {
		case <-stop:
			return
		default:
		}

		if _, err := sw.RunOnce(ctx); err != nil {
			fail("sweep: %v", err)

			return
		}
	}
}

// checkInvariantUntil checks, under each blob's lock, that a referenced blob
// has its object, until stop is closed.
func (s *purgeSeed) checkInvariantUntil(ctx context.Context, stop <-chan struct{}, shas []string, fail func(string, ...any)) {
	for {
		for _, sha := range shas {
			select {
			case <-stop:
				return
			default:
			}

			err := storage.WithBlobLock(ctx, s.pg, sha, func(ctx context.Context) error {
				var refs int
				if err := s.pg.GetDB().QueryRowContext(ctx,
					`SELECT COUNT(*) FROM maven_artifacts m JOIN blobs b ON b.id = m.blob_id WHERE b.sha256 = $1`, sha).Scan(&refs); err != nil {
					return err
				}

				if refs == 0 {
					return nil
				}

				if ok, _ := s.blobs.Exists(ctx, sha); !ok {
					fail("blob %s referenced by %d rows has no object", sha, refs)
				}

				return nil
			})
			if err != nil {
				fail("check: %v", err)

				return
			}
		}
	}
}
