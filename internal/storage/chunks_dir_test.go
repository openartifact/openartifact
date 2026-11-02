// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
)

func TestDirChunkStore(t *testing.T) {
	chunkContract(t, NewDirChunkStore(filepath.Join(t.TempDir(), "chunks")))
}

func TestFilesystemStoreChunks(t *testing.T) {
	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	chunkContract(t, fsys)
}

// Without a public URL (no relay) the filesystem still holds chunks, under
// <base>/uploads/<id>.
func TestFilesystemStoreChunkLocation(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	fsys, err := NewFilesystemBlobStore(StorageConfig{Type: "filesystem", Path: base})
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.NewString()
	if _, err := fsys.PutChunk(t.Context(), id, bytes.NewReader([]byte("here"))); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(base, "uploads", id))
	if err != nil || string(got) != "here" {
		t.Fatalf("chunk file = %q, %v", got, err)
	}
}

// A missing directory is an empty store, and only PutChunk creates it.
func TestDirChunkStoreCreatesDirectoryLazily(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "a", "b")
	c := NewDirChunkStore(dir)
	id := uuid.NewString()

	requireNoChunk(t, c, id)

	if err := c.DeleteChunk(t.Context(), id); err != nil {
		t.Fatalf("DeleteChunk: %v", err)
	}

	if ids, _ := walkAll(t, c, time.Now().Add(time.Hour)); len(ids) != 0 {
		t.Fatalf("walk of a missing directory = %v", ids)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a read created the directory: %v", err)
	}

	if _, err := c.PutChunk(t.Context(), id, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("PutChunk did not create the directory: %v", err)
	}
}

// A body that fails halfway leaves neither a chunk nor a temp file; one that
// fails while replacing a chunk keeps the old content.
func TestDirChunkStoreWriteIsAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c := NewDirChunkStore(dir)
	ctx := t.Context()
	id := uuid.NewString()

	failing := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("x"), 1<<20)), iotest.ErrReader(errChunkBody))
	if _, err := c.PutChunk(ctx, id, failing); err == nil {
		t.Fatal("PutChunk succeeded")
	}

	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a failed write left %d entries", len(entries))
	}

	if _, err := c.PutChunk(ctx, id, bytes.NewReader([]byte("old"))); err != nil {
		t.Fatal(err)
	}

	if _, err := c.PutChunk(ctx, id, io.MultiReader(bytes.NewReader([]byte("new")), iotest.ErrReader(errChunkBody))); err == nil {
		t.Fatal("PutChunk succeeded")
	}

	rc, err := c.OpenChunk(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); string(got) != "old" {
		t.Fatalf("chunk after a failed replace = %q", got)
	}

	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the chunk", len(entries))
	}
}

// A walk reports chunk files only: foreign names and subdirectories are
// skipped, live temp files are left alone and stale ones are swept.
func TestDirChunkStoreWalkSkipsForeignNames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c := NewDirChunkStore(dir)
	id := uuid.NewString()

	if _, err := c.PutChunk(t.Context(), id, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		return p
	}

	write("README")
	write("not-a-uuid")
	write(uuid.NewString() + ".bak")
	write("{" + uuid.NewString() + "}")

	if err := os.Mkdir(filepath.Join(dir, uuid.NewString()), 0o755); err != nil {
		t.Fatal(err)
	}

	stale := write(chunkTempPrefix + "stale" + chunkTempSuffix)
	live := write(chunkTempPrefix + "live" + chunkTempSuffix)

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	ids, _ := walkAll(t, c, time.Now().Add(-time.Hour))
	if len(ids) != 0 {
		t.Fatalf("walk reported %v for chunks newer than the cutoff", ids)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale temp file survived: %v", err)
	}

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the live temp file was removed: %v", err)
	}

	ids, _ = walkAll(t, c, time.Now().Add(time.Hour))
	if !slices.Equal(ids, []string{id}) {
		t.Fatalf("walk = %v, want only %s", ids, id)
	}
}

func TestDirChunkStoreWalkBatches(t *testing.T) {
	t.Parallel()

	c := NewDirChunkStore(t.TempDir())

	const total = 2*chunkWalkBatch + 50

	want := make([]string, 0, total)

	for range total {
		id := uuid.NewString()
		want = append(want, id)

		if _, err := c.PutChunk(t.Context(), id, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}

	slices.Sort(want)

	ids, batches := walkAll(t, c, time.Now().Add(time.Hour))
	if !slices.Equal(ids, want) {
		t.Fatalf("walk reported %d ids, want %d", len(ids), total)
	}

	if !slices.Equal(batches, []int{chunkWalkBatch, chunkWalkBatch, 50}) {
		t.Fatalf("batches = %v", batches)
	}
}

// Chunks never show up as blobs, in a listing or in alias resolution.
func TestFilesystemStoreListIgnoresChunks(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	data := []byte("a real blob")
	sha := sha256Hex(data)

	if _, err := fsys.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if _, err := fsys.PutChunk(ctx, uuid.NewString(), bytes.NewReader([]byte("chunk"))); err != nil {
			t.Fatal(err)
		}
	}

	ids, next, err := fsys.List(ctx, "", 100)
	if err != nil || next != "" || !slices.Equal(ids, []string{sha}) {
		t.Fatalf("List = %v, %q, %v", ids, next, err)
	}

	if err := fsys.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}

	if aliases := fsys.scanAliases(); len(aliases) != 0 {
		t.Fatalf("scanAliases = %v", aliases)
	}
}
