// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
)

// errChunkBody is what the failing bodies of these tests return.
var errChunkBody = errors.New("chunk body failed")

// walkAll collects the ids a walk reports and the sizes of its batches.
func walkAll(t *testing.T, c ChunkStore, olderThan time.Time) ([]string, []int) {
	t.Helper()

	var (
		ids     []string
		batches []int
	)

	if err := c.WalkChunks(t.Context(), olderThan, func(b []string) error {
		ids = append(ids, b...)
		batches = append(batches, len(b))

		return nil
	}); err != nil {
		t.Fatalf("WalkChunks: %v", err)
	}

	slices.Sort(ids)

	return ids, batches
}

// requireNoChunk fails unless id is absent from c.
func requireNoChunk(t *testing.T, c ChunkStore, id string) {
	t.Helper()

	if _, err := c.OpenChunk(t.Context(), id); !errors.Is(err, ErrChunkNotFound) {
		t.Fatalf("OpenChunk(%s) = %v, want ErrChunkNotFound", id, err)
	}
}

// chunkContract is the behaviour every ChunkStore that reports support has.
func chunkContract(t *testing.T, c ChunkStore) {
	t.Helper()

	if !c.ChunksSupported() {
		t.Fatal("ChunksSupported = false")
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, ChunkStore)
	}{
		{"round trip and replace", chunkCaseRoundTripAndReplace},
		{"empty chunk", chunkCaseEmptyChunk},
		{"unknown length body", chunkCaseUnknownLengthBody},
		{"delete", chunkCaseDelete},
		{"failing body leaves nothing", chunkCaseFailingBodyLeavesNothing},
		{"invalid ids", chunkCaseInvalidIds},
		{"walk by age", chunkCaseWalkByAge},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, c) })
	}
}

func chunkCaseRoundTripAndReplace(t *testing.T, c ChunkStore) {
	t.Helper()

	ctx := t.Context()

	id := uuid.NewString()
	first := []byte("first chunk body")

	n, err := c.PutChunk(ctx, id, bytes.NewReader(first))
	if err != nil || n != int64(len(first)) {
		t.Fatalf("PutChunk = %d, %v", n, err)
	}

	rc, err := c.OpenChunk(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, first) {
		t.Fatalf("OpenChunk = %q", got)
	}

	second := []byte("second, longer chunk body")
	if n, err := c.PutChunk(ctx, id, bytes.NewReader(second)); err != nil || n != int64(len(second)) {
		t.Fatalf("replacing PutChunk = %d, %v", n, err)
	}

	rc, err = c.OpenChunk(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, second) {
		t.Fatalf("OpenChunk after replace = %q", got)
	}
}

func chunkCaseEmptyChunk(t *testing.T, c ChunkStore) {
	t.Helper()

	ctx := t.Context()

	id := uuid.NewString()

	if n, err := c.PutChunk(ctx, id, bytes.NewReader(nil)); err != nil || n != 0 {
		t.Fatalf("PutChunk = %d, %v", n, err)
	}

	rc, err := c.OpenChunk(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); len(got) != 0 {
		t.Fatalf("OpenChunk = %q", got)
	}
}

func chunkCaseUnknownLengthBody(t *testing.T, c ChunkStore) {
	t.Helper()

	ctx := t.Context()

	id := uuid.NewString()
	data := bytes.Repeat([]byte("0123456789"), 1000)

	// OneByteReader hides every optional interface (Seeker, WriterTo, Len).
	n, err := c.PutChunk(ctx, id, iotest.OneByteReader(bytes.NewReader(data)))
	if err != nil || n != int64(len(data)) {
		t.Fatalf("PutChunk = %d, %v", n, err)
	}

	rc, err := c.OpenChunk(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, data) {
		t.Fatal("OpenChunk returned different bytes")
	}
}

func chunkCaseDelete(t *testing.T, c ChunkStore) {
	t.Helper()

	ctx := t.Context()

	id := uuid.NewString()

	if _, err := c.PutChunk(ctx, id, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := c.DeleteChunk(ctx, id); err != nil {
			t.Fatalf("DeleteChunk: %v", err)
		}
	}

	requireNoChunk(t, c, id)

	if err := c.DeleteChunk(ctx, uuid.NewString()); err != nil {
		t.Fatalf("DeleteChunk of a missing chunk: %v", err)
	}
}

func chunkCaseFailingBodyLeavesNothing(t *testing.T, c ChunkStore) {
	t.Helper()

	ctx := t.Context()

	id := uuid.NewString()
	body := io.MultiReader(bytes.NewReader([]byte("partial")), iotest.ErrReader(errChunkBody))

	if _, err := c.PutChunk(ctx, id, body); !errors.Is(err, errChunkBody) {
		t.Fatalf("PutChunk = %v, want the body error", err)
	}

	requireNoChunk(t, c, id)
}

func chunkCaseInvalidIds(t *testing.T, c ChunkStore) {
	t.Helper()

	ctx := t.Context()

	for _, id := range []string{
		"", "x", "../etc/passwd", "uploads/" + uuid.NewString(), "{" + uuid.NewString() + "}",
		"urn:uuid:" + uuid.NewString(), uuid.NewString() + "/..", "staging/" + uuid.NewString(),
	} {
		if _, err := c.PutChunk(ctx, id, bytes.NewReader(nil)); err == nil {
			t.Errorf("PutChunk(%q) succeeded", id)
		}

		if _, err := c.OpenChunk(ctx, id); err == nil || errors.Is(err, ErrChunkNotFound) {
			t.Errorf("OpenChunk(%q) = %v, want a validation error", id, err)
		}

		if err := c.DeleteChunk(ctx, id); err == nil {
			t.Errorf("DeleteChunk(%q) succeeded", id)
		}
	}
}

func chunkCaseWalkByAge(t *testing.T, c ChunkStore) {
	t.Helper()

	ctx := t.Context()

	id := uuid.NewString()

	if _, err := c.PutChunk(ctx, id, bytes.NewReader([]byte("walk"))); err != nil {
		t.Fatal(err)
	}

	if ids, _ := walkAll(t, c, time.Now().Add(-time.Hour)); slices.Contains(ids, id) {
		t.Fatal("a chunk newer than olderThan was reported")
	}

	if ids, _ := walkAll(t, c, time.Now().Add(time.Hour)); !slices.Contains(ids, id) {
		t.Fatalf("a chunk older than olderThan was not reported: %v", ids)
	}

	stop := errors.New("stop")
	if err := c.WalkChunks(ctx, time.Now().Add(time.Hour), func([]string) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("WalkChunks = %v, want the callback error", err)
	}
}

// A store that cannot hold chunks makes ChunksFor fall back to the directory;
// one that can is used as is.
func TestChunksFor(t *testing.T) {
	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	fallback := t.TempDir()
	id := uuid.NewString()

	tests := []struct {
		name      string
		store     BlobStore
		wantDir   bool // chunks end up in the fallback directory
		wantStore bool // chunks end up in the store's own chunk directory
	}{
		{name: "filesystem", store: fsys, wantStore: true},
		{name: "metrics wrapper of a supporting store", store: NewMetricsBlobStore(fsys, nil), wantStore: true},
		{name: "oci", store: &OCIBlobStore{}, wantDir: true},
		{name: "wrapper of an unsupporting store", store: NewMetricsBlobStore(&OCIBlobStore{}, nil), wantDir: true},
		{name: "store without the interface", store: struct{ BlobStore }{}, wantDir: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := ChunksFor(tc.store, fallback)
			if !c.ChunksSupported() {
				t.Fatal("the selected chunk store does not support chunks")
			}

			if _, err := c.PutChunk(t.Context(), id, bytes.NewReader([]byte("x"))); err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = c.DeleteChunk(context.Background(), id) })

			_, inFallback := os.Stat(filepath.Join(fallback, id))
			_, inStore := os.Stat(filepath.Join(fsys.basePath, filesystemChunksDir, id))

			if (inFallback == nil) != tc.wantDir || (inStore == nil) != tc.wantStore {
				t.Fatalf("chunk in fallback dir = %v, in store = %v; want %v, %v",
					inFallback == nil, inStore == nil, tc.wantDir, tc.wantStore)
			}
		})
	}
}

// The OCI backend holds no chunks and says so.
func TestOCIBlobStoreHasNoChunks(t *testing.T) {
	t.Parallel()

	var c ChunkStore = &OCIBlobStore{}

	ctx := t.Context()
	id := uuid.NewString()

	if c.ChunksSupported() {
		t.Fatal("ChunksSupported = true")
	}

	if _, err := c.PutChunk(ctx, id, bytes.NewReader(nil)); !errors.Is(err, ErrChunksNotSupported) {
		t.Errorf("PutChunk = %v", err)
	}

	if _, err := c.OpenChunk(ctx, id); !errors.Is(err, ErrChunksNotSupported) {
		t.Errorf("OpenChunk = %v", err)
	}

	if err := c.DeleteChunk(ctx, id); !errors.Is(err, ErrChunksNotSupported) {
		t.Errorf("DeleteChunk = %v", err)
	}

	if err := c.WalkChunks(ctx, time.Now(), func([]string) error { return nil }); !errors.Is(err, ErrChunksNotSupported) {
		t.Errorf("WalkChunks = %v", err)
	}
}

// The forwarder reaches the chunk store of its target on every call, answers
// "not supported" for a target without one, and the wrappers keep the contract.
func TestStagingForwarderChunks(t *testing.T) {
	t.Parallel()

	// A walk sweeps temp files younger than its cutoff, so the contract runs
	// need a blob root each.
	newFS := func(t *testing.T) *FilesystemStore {
		t.Helper()

		fsys, err := NewFilesystemStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		return fsys
	}

	t.Run("supporting target", func(t *testing.T) {
		t.Parallel()

		fsys := newFS(t)
		chunkContract(t, NewStagingForwarder(func() BlobStore { return fsys }))
	})

	t.Run("metrics wrapper", func(t *testing.T) {
		t.Parallel()

		chunkContract(t, NewMetricsBlobStore(newFS(t), nil))
	})

	t.Run("target is read on every call", func(t *testing.T) {
		t.Parallel()

		var target BlobStore = &OCIBlobStore{}

		f := NewStagingForwarder(func() BlobStore { return target })
		if f.ChunksSupported() {
			t.Fatal("ChunksSupported = true for oci")
		}

		target = newFS(t)
		if !f.ChunksSupported() {
			t.Fatal("ChunksSupported = false after the target changed")
		}
	})

	t.Run("unsupporting targets", func(t *testing.T) {
		t.Parallel()

		for name, target := range map[string]BlobStore{"oci": &OCIBlobStore{}, "no interface": struct{ BlobStore }{}} {
			f := NewStagingForwarder(func() BlobStore { return target })
			ctx := t.Context()
			id := uuid.NewString()

			if f.ChunksSupported() {
				t.Errorf("%s: ChunksSupported = true", name)
			}

			if _, err := f.PutChunk(ctx, id, bytes.NewReader(nil)); !errors.Is(err, ErrChunksNotSupported) {
				t.Errorf("%s: PutChunk = %v", name, err)
			}

			if _, err := f.OpenChunk(ctx, id); !errors.Is(err, ErrChunksNotSupported) {
				t.Errorf("%s: OpenChunk = %v", name, err)
			}

			if err := f.DeleteChunk(ctx, id); !errors.Is(err, ErrChunksNotSupported) {
				t.Errorf("%s: DeleteChunk = %v", name, err)
			}

			if err := f.WalkChunks(ctx, time.Now(), func([]string) error { return nil }); !errors.Is(err, ErrChunksNotSupported) {
				t.Errorf("%s: WalkChunks = %v", name, err)
			}
		}
	})
}
