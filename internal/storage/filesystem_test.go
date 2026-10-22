// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
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
	// Put with SHA256 as ID (content-addressed)
	id, err := store.Put(ctx, bytes.NewReader(testData), map[string]string{
		"sha256": sha64,
	})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Check with correct hash
	foundID, exists, err := store.ExistsBySHA256(ctx, sha64)
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

func TestFilesystem_ExistsBySHA256_AliasIndex(t *testing.T) {
	store := newTestFilesystemStore(t)
	ctx := context.Background()

	// The blob ID is caller-supplied and differs from the content digest.
	dataA := []byte("alias blob A")
	if _, err := store.Put(ctx, bytes.NewReader(dataA), map[string]string{"sha256": sha64}); err != nil {
		t.Fatalf("Put A: %v", err)
	}

	// First lookup builds the index from the sidecars.
	id, ok, err := store.ExistsBySHA256(ctx, sha256Hex(dataA))
	if err != nil || !ok || id != sha64 {
		t.Fatalf("lookup A = (%q, %v, %v), want (%q, true, nil)", id, ok, err, sha64)
	}

	// A blob stored after the index was built is added incrementally.
	dataB := []byte("alias blob B")
	idB := strings.Repeat("cd", 32)

	if _, err := store.Put(ctx, bytes.NewReader(dataB), map[string]string{"sha256": idB}); err != nil {
		t.Fatalf("Put B: %v", err)
	}

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
