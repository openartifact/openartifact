// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func newTestFilesystemStore(t *testing.T) *FilesystemStore {
	t.Helper()
	dir := t.TempDir()

	store, err := NewFilesystemBlobStore(StorageConfig{
		Type: "filesystem",
		Path: dir,
	})
	if err != nil {
		t.Fatalf("NewFilesystemBlobStore: %v", err)
	}

	return store
}

func TestFilesystem_PutAndGet(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	testData := []byte("test data")

	id, err := store.Put(ctx, bytes.NewReader(testData), nil)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	reader, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(data, testData) {
		t.Errorf("data mismatch: got %q, want %q", data, testData)
	}
}

func TestFilesystem_PutWithMetadata(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	testData := []byte("test data with metadata")
	metadata := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}

	id, err := store.Put(ctx, bytes.NewReader(testData), metadata)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	retrievedMeta, err := store.GetMetadata(ctx, id)
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}

	if retrievedMeta["key1"] != "value1" {
		t.Errorf("metadata mismatch: got %q, want %q", retrievedMeta["key1"], "value1")
	}

	if retrievedMeta["key2"] != "value2" {
		t.Errorf("metadata mismatch: got %q, want %q", retrievedMeta["key2"], "value2")
	}
}

func TestFilesystem_Exists(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	testData := []byte("test data")

	id, err := store.Put(ctx, bytes.NewReader(testData), nil)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	exists, err := store.Exists(ctx, id)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if !exists {
		t.Error("expected blob to exist")
	}

	// Check non-existent blob
	exists, err = store.Exists(ctx, sha64)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if exists {
		t.Error("expected blob to not exist")
	}
}

func TestFilesystem_Delete(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	testData := []byte("test data")

	id, err := store.Put(ctx, bytes.NewReader(testData), nil)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	err = store.Delete(ctx, id)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	exists, err := store.Exists(ctx, id)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if exists {
		t.Error("expected blob to be deleted")
	}
}

func TestFilesystem_GetSize(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	testData := []byte("test data")

	id, err := store.Put(ctx, bytes.NewReader(testData), nil)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	size, err := store.GetSize(ctx, id)
	if err != nil {
		t.Fatalf("GetSize failed: %v", err)
	}

	if size != int64(len(testData)) {
		t.Errorf("size mismatch: got %d, want %d", size, len(testData))
	}
}

func TestFilesystem_List(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	// Put 3 blobs
	ids := make([]string, 3)

	for i := range 3 {
		testData := []byte("test data " + string(rune(i)))

		id, err := store.Put(ctx, bytes.NewReader(testData), nil)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		ids[i] = id
	}

	// List all blobs
	list, _, err := store.List(ctx, "", 0)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(list) != 3 {
		t.Errorf("expected 3 blobs, got %d", len(list))
	}
}

const sha64 = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"

func TestFilesystem_ExistsBySHA256(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	testData := []byte("test data")
	sha := sha256Hex(testData)
	// Put with SHA256 as ID (content-addressed)
	id, err := store.Put(ctx, bytes.NewReader(testData), map[string]string{
		"sha256": sha,
	})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Check with correct hash
	foundID, exists, err := store.ExistsBySHA256(ctx, sha)
	if err != nil {
		t.Fatalf("ExistsBySHA256 failed: %v", err)
	}

	if !exists {
		t.Error("expected blob to be found by SHA256")
	}

	if foundID != id {
		t.Errorf("ID mismatch: got %q, want %q", foundID, id)
	}

	// Check with non-existent hash
	_, exists, err = store.ExistsBySHA256(ctx, strings.Repeat("0", 64))
	if err != nil {
		t.Fatalf("ExistsBySHA256 failed: %v", err)
	}

	if exists {
		t.Error("expected blob to not be found by SHA256")
	}
}

func TestFilesystem_GetNotFound(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, sha64)
	if err == nil {
		t.Error("expected error for non-existent blob")
	}
}

func TestFilesystem_DeleteNotFound(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	// Delete non-existent blob should not error
	err := store.Delete(ctx, sha64)
	if err != nil {
		t.Fatalf("Delete should not error for non-existent blob: %v", err)
	}
}

func TestFilesystem_PutEmptyData(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	// Put empty data
	id, err := store.Put(ctx, bytes.NewReader([]byte("")), nil)
	if err != nil {
		t.Fatalf("Put empty data failed: %v", err)
	}

	// Verify it exists
	exists, err := store.Exists(ctx, id)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}

	if !exists {
		t.Error("expected empty blob to exist")
	}

	// Verify size is 0
	size, err := store.GetSize(ctx, id)
	if err != nil {
		t.Fatalf("GetSize failed: %v", err)
	}

	if size != 0 {
		t.Errorf("expected size 0, got %d", size)
	}
}

func TestFilesystem_RejectsInvalidBlobID(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	for _, id := range []string{"../../../../etc/passwd", "ab", "", "zz" + sha64[2:], sha64 + "/.."} {
		if _, err := store.Get(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("Get(%q) err = %v, want ErrInvalidBlobID", id, err)
		}

		if err := store.Delete(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("Delete(%q) err = %v", id, err)
		}
	}

	if _, err := store.Put(ctx, bytes.NewReader([]byte("x")), map[string]string{"sha256": "../../x"}); !errors.Is(err, ErrInvalidBlobID) {
		t.Errorf("Put traversal err = %v", err)
	}
}

func TestFilesystem_ListSkipsUploadTmp(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(store.basePath, "blob-upload-123.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ids, _, err := store.List(ctx, "", 0)
	if err != nil || len(ids) != 0 {
		t.Fatalf("List = %v, %v; want empty", ids, err)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// putLegacyAliased lays out a blob the way versions that accepted an unverified
// "sha256" claim did: stored under id, with a sidecar naming the real digest.
func putLegacyAliased(t *testing.T, store *FilesystemStore, id string, data []byte) {
	t.Helper()

	blobPath, err := store.getBlobPath(id)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(blobPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	meta, _ := json.Marshal(map[string]string{"sha256": id, "_sha256": sha256Hex(data)})
	if err := os.WriteFile(blobPath+".meta", meta, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystem_ExistsBySHA256_AliasIndex(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	// A blob written by an older version under an ID that differs from the content digest.
	dataA := []byte("alias blob A")
	putLegacyAliased(t, store, sha64, dataA)

	// First lookup builds the index from the sidecars.
	id, ok, err := store.ExistsBySHA256(ctx, sha256Hex(dataA))
	if err != nil || !ok || id != sha64 {
		t.Fatalf("lookup A = (%q, %v, %v), want (%q, true, nil)", id, ok, err, sha64)
	}

	// A blob stored after the index was built is added incrementally.
	dataB := []byte("alias blob B")
	idB := strings.Repeat("cd", 32)

	putLegacyAliased(t, store, idB, dataB)
	store.aliases.add(sha256Hex(dataB), idB)

	if id, ok, err = store.ExistsBySHA256(ctx, sha256Hex(dataB)); err != nil || !ok || id != idB {
		t.Fatalf("lookup B = (%q, %v, %v), want (%q, true, nil)", id, ok, err, idB)
	}

	// A fresh store over the same directory rebuilds the index from disk.
	fresh, err := NewFilesystemStore(store.basePath)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if id, ok, err = fresh.ExistsBySHA256(ctx, sha256Hex(dataA)); err != nil || !ok || id != sha64 {
		t.Fatalf("fresh lookup A = (%q, %v, %v)", id, ok, err)
	}

	// Deleting the blob drops it from the index.
	if err := store.Delete(ctx, sha64); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, ok, err = store.ExistsBySHA256(ctx, sha256Hex(dataA)); err != nil || ok {
		t.Fatalf("lookup after delete = (%v, %v), want (false, nil)", ok, err)
	}

	// Unknown digests stay a miss.
	if _, ok, err = store.ExistsBySHA256(ctx, strings.Repeat("0", 64)); err != nil || ok {
		t.Fatalf("unknown digest = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestFilesystem_getBlobPathStaysInsideRoot(t *testing.T) {
	root := t.TempDir()

	fs, err := NewFilesystemStore(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"../../etc/passwd", "..", "/abs", strings.Repeat("A", 64), strings.Repeat("a", 63) + "/"} {
		if _, err := fs.getBlobPath(id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("getBlobPath(%q) err = %v, want ErrInvalidBlobID", id, err)
		}
	}

	p, err := fs.getBlobPath(strings.Repeat("a", 64))
	if err != nil || !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		t.Errorf("valid id: path=%q err=%v", p, err)
	}
}

func TestFilesystem_ListPagesByToken(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	want := make([]string, 0, 25)

	for i := range 25 {
		id, err := store.Put(ctx, bytes.NewReader([]byte(fmt.Sprintf("blob %d", i))), nil)
		if err != nil {
			t.Fatal(err)
		}

		want = append(want, id)
	}

	sort.Strings(want)

	var got []string

	token := ""

	for {
		ids, next, err := store.List(ctx, token, 7)
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, ids...)

		if next == "" {
			break
		}

		token = next
	}

	if !slices.Equal(got, want) {
		t.Fatalf("paged list = %v, want %v", got, want)
	}
}

func TestNewFilesystemStoreSweepsStaleUploadTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "blob-upload-stale.tmp")
	fresh := filepath.Join(dir, "blob-upload-fresh.tmp")

	for _, f := range []string{stale, fresh} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := NewFilesystemStore(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file should be removed, stat err = %v", err)
	}

	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp file must be kept: %v", err)
	}
}

func TestFilesystem_PutRejectsDigestMismatch(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	good := []byte("verified")
	sha := sha256Hex(good)

	if _, err := store.Put(ctx, bytes.NewReader([]byte("other")), map[string]string{"sha256": sha}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Put mismatching bytes = %v, want ErrDigestMismatch", err)
	}

	if ok, _ := store.Exists(ctx, sha); ok {
		t.Fatal("object visible after rejected Put")
	}

	entries, _ := os.ReadDir(store.basePath)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file %s left behind", e.Name())
		}
	}

	if _, err := store.Put(ctx, bytes.NewReader(good), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Verified content already present is not replaced, and a mismatching
	// Put over it cannot corrupt it.
	if _, err := store.Put(ctx, bytes.NewReader([]byte("poison")), map[string]string{"sha256": sha}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Put over existing = %v, want ErrDigestMismatch", err)
	}

	rc, err := store.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, good) {
		t.Fatalf("stored bytes = %q, want %q", got, good)
	}
}

func TestAzurePutRejectsDigestMismatchWithoutRequest(t *testing.T) {
	var puts int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := azureTestStore(t, srv.URL)

	if _, err := s.Put(context.Background(), strings.NewReader("bytes"), map[string]string{"sha256": strings.Repeat("0", 64)}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Put = %v, want ErrDigestMismatch", err)
	}

	if puts != 0 {
		t.Fatalf("%d PUT requests reached the server, want 0", puts)
	}
}

// The metadata sidecar is written whole (temp + rename): it is complete, 0600
// and leaves no temp file behind.
func TestFilesystem_PutWritesSidecarAtomically(t *testing.T) {
	store := newTestFilesystemStore(t)

	id, err := store.Put(context.Background(), strings.NewReader("sidecar content"), map[string]string{"content_type": "text/plain"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	blobPath, err := store.getBlobPath(id)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(blobPath + ".meta")
	if err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}

	var meta map[string]string
	if err := json.Unmarshal(raw, &meta); err != nil || meta["content_type"] != "text/plain" || meta["_sha256"] != id {
		t.Fatalf("sidecar = %s (%v), want the content type and digest", raw, err)
	}

	if st, err := os.Stat(blobPath + ".meta"); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %v (%v), want 0600", st.Mode().Perm(), err)
	}

	tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(blobPath), "blob-upload-*"))
	if len(tmps) != 0 {
		t.Fatalf("temp files left behind: %v", tmps)
	}
}
