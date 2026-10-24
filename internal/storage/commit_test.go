// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
)

// lockDB is a database whose per-blob lock is observable: held reports whether
// any lock is currently held.
type lockDB struct {
	database.BaseMockDB

	mu    sync.Mutex
	depth int
	taken []string
}

func (d *lockDB) WithBlobLock(ctx context.Context, sha string, fn func(ctx context.Context) error) error {
	d.mu.Lock()
	d.depth++
	d.taken = append(d.taken, sha)
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.depth--
		d.mu.Unlock()
	}()

	return fn(ctx)
}

func (d *lockDB) held() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.depth > 0
}

// putRecord is one Put the memStore saw.
type putRecord struct {
	sha    string
	meta   map[string]string
	locked bool
}

// memStore is an in-memory BlobStore that reports, per Put, whether the
// database lock was held, and lets a test act between the steps.
type memStore struct {
	BlobStore

	db *lockDB

	mu      sync.Mutex
	objects map[string][]byte
	puts    []putRecord
	// afterFirstLookup runs once, after the first ExistsBySHA256 call returned
	// (the pre-lock lookup), before it is used.
	afterFirstLookup func()
	lookups          int
	putErr           error
}

func newMemStore(db *lockDB) *memStore {
	return &memStore{db: db, objects: map[string][]byte{}}
}

func (s *memStore) ExistsBySHA256(_ context.Context, sha string) (string, bool, error) {
	s.mu.Lock()
	_, ok := s.objects[sha]
	s.lookups++
	first := s.lookups == 1
	hook := s.afterFirstLookup
	s.mu.Unlock()

	if first && hook != nil {
		hook()
	}

	if !ok {
		return "", false, nil
	}

	return "key-" + sha, true, nil
}

func (s *memStore) Put(_ context.Context, r io.Reader, meta map[string]string) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sha := meta["sha256"]
	s.puts = append(s.puts, putRecord{sha: sha, meta: meta, locked: s.db.held()})

	if s.putErr != nil {
		return "", s.putErr
	}

	s.objects[sha] = data

	return "key-" + sha, nil
}

const testSHA = "abababababababababababababababababababababababababababababababab"

func TestCommitBlobStoresBeforeTheLockAndCommitsUnderIt(t *testing.T) {
	db := &lockDB{}
	store := newMemStore(db)

	var (
		gotKey       string
		heldAtCommit bool
	)

	err := CommitBlob(context.Background(), db, store, BytesBlob(testSHA, []byte("payload"), map[string]string{"format": "raw"}),
		func(_ context.Context, key string) error {
			gotKey, heldAtCommit = key, db.held()

			return nil
		})
	if err != nil {
		t.Fatalf("CommitBlob: %v", err)
	}

	if gotKey != "key-"+testSHA || !heldAtCommit {
		t.Fatalf("commit ran with key %q, lock held %v; want key-<sha> under the lock", gotKey, heldAtCommit)
	}

	if len(store.puts) != 1 || store.puts[0].locked {
		t.Fatalf("puts = %+v; want exactly one Put, made before the lock was taken", store.puts)
	}

	if store.puts[0].meta["sha256"] != testSHA || store.puts[0].meta["format"] != "raw" {
		t.Errorf("put metadata = %v; want the caller's entries plus sha256", store.puts[0].meta)
	}
}

func TestCommitBlobSkipsThePutOfAnExistingObject(t *testing.T) {
	db := &lockDB{}
	store := newMemStore(db)
	store.objects[testSHA] = []byte("payload")

	if err := CommitBlob(context.Background(), db, store, BytesBlob(testSHA, []byte("payload"), nil),
		func(context.Context, string) error { return nil }); err != nil {
		t.Fatalf("CommitBlob: %v", err)
	}

	if len(store.puts) != 0 {
		t.Fatalf("puts = %+v; an existing object must not be written again", store.puts)
	}
}

// A delete that removed the object between the Put and the lock is repaired:
// the object is stored again from the source before the row is committed.
func TestCommitBlobRestoresAnObjectDeletedBeforeTheLock(t *testing.T) {
	db := &lockDB{}
	store := newMemStore(db)
	store.objects[testSHA] = []byte("payload")
	store.afterFirstLookup = func() { delete(store.objects, testSHA) }

	// The lookup answered "exists" (skipping the Put) and the object vanished
	// right after: the state the sweeper leaves behind.
	committedWith := ""

	err := CommitBlob(context.Background(), db, store, BytesBlob(testSHA, []byte("payload"), nil),
		func(_ context.Context, key string) error {
			committedWith = key
			store.mu.Lock()
			_, ok := store.objects[testSHA]
			store.mu.Unlock()

			if !ok {
				t.Error("commit ran although the object is not in the store")
			}

			return nil
		})
	if err != nil {
		t.Fatalf("CommitBlob: %v", err)
	}

	if committedWith != "key-"+testSHA {
		t.Errorf("commit key = %q", committedWith)
	}

	if len(store.puts) != 1 || !store.puts[0].locked {
		t.Fatalf("puts = %+v; want one repair Put, under the lock", store.puts)
	}

	if !bytes.Equal(store.objects[testSHA], []byte("payload")) {
		t.Errorf("restored object = %q", store.objects[testSHA])
	}
}

func TestCommitBlobWithoutSourceFailsRetryablyWhenTheObjectIsGone(t *testing.T) {
	db := &lockDB{}
	store := newMemStore(db)

	called := false

	err := CommitBlob(context.Background(), db, store, Blob{SHA256: testSHA},
		func(context.Context, string) error { called = true; return nil })
	if !errors.Is(err, ErrBlobGone) {
		t.Fatalf("err = %v; want ErrBlobGone", err)
	}

	if called {
		t.Error("commit ran for a blob that is missing")
	}

	if len(store.puts) != 0 {
		t.Errorf("puts = %+v; a blob without a source cannot be written", store.puts)
	}
}

func TestCommitBlobsReportsStoreAndCommitErrorsApart(t *testing.T) {
	db := &lockDB{}
	store := newMemStore(db)
	boom := errors.New("storage unavailable")
	store.putErr = boom

	err := CommitBlob(context.Background(), db, store, BytesBlob(testSHA, nil, nil),
		func(context.Context, string) error { t.Fatal("commit ran after a failed Put"); return nil })

	se, ok := errors.AsType[*StoreError](err)
	if !ok || se.Op != "store" || se.SHA256 != testSHA || !errors.Is(err, boom) {
		t.Fatalf("err = %v; want a store *StoreError wrapping the Put error", err)
	}

	if len(db.taken) != 0 {
		t.Errorf("locks taken = %v; a failed Put must not take the lock", db.taken)
	}

	store.putErr = nil
	sentinel := errors.New("duplicate row")

	err = CommitBlob(context.Background(), db, store, BytesBlob(testSHA, nil, nil),
		func(context.Context, string) error { return sentinel })
	if err != sentinel { //nolint:errorlint // the commit error must come back unchanged
		t.Fatalf("err = %v; want the commit error unchanged", err)
	}
}

func TestCommitBlobsStoresEveryBlobAndAlignsKeys(t *testing.T) {
	db := &lockDB{}
	store := newMemStore(db)
	other := strings.Repeat("cd", 32)

	var got []string

	err := CommitBlobs(context.Background(), db, store,
		[]Blob{BytesBlob(other, []byte("two"), nil), BytesBlob(testSHA, []byte("one"), nil)},
		func(_ context.Context, keys []string) error {
			got = keys

			return nil
		})
	if err != nil {
		t.Fatalf("CommitBlobs: %v", err)
	}

	if len(got) != 2 || got[0] != "key-"+other || got[1] != "key-"+testSHA {
		t.Fatalf("keys = %v; want them in input order", got)
	}

	for _, p := range store.puts {
		if p.locked {
			t.Errorf("Put of %s ran under the lock", p.sha)
		}
	}
}

func TestSeekerBlobRewindsBeforeEveryAttempt(t *testing.T) {
	src := strings.NewReader("payload")

	if _, err := io.ReadAll(src); err != nil { // leave the source at EOF
		t.Fatal(err)
	}

	r, err := SeekerBlob(testSHA, src, nil).Open()
	if err != nil {
		t.Fatal(err)
	}

	data, _ := io.ReadAll(r)
	if string(data) != "payload" {
		t.Fatalf("read %q after Open; want the whole content", data)
	}
}
