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
	"strings"
	"testing"
)

// tempFilesIn lists leftover upload temp files in dir.
func tempFilesIn(t *testing.T, dir string) []string {
	t.Helper()

	var left []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			left = append(left, e.Name())
		}
	}

	return left
}

func TestWriteFileAtomicPublishesWholeFileWithRestrictedMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "blob.meta")

	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(dir, target, []byte("new contents")); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new contents" {
		t.Fatalf("file = %q, %v", got, err)
	}

	if st, _ := os.Stat(target); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}

	if left := tempFilesIn(t, dir); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestWriteFileAtomicFailureLeavesTargetAndNoTemp(t *testing.T) {
	t.Parallel()

	t.Run("rename onto a directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		target := filepath.Join(dir, "occupied")

		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := writeFileAtomic(dir, target, []byte("data")); err == nil {
			t.Fatal("writeFileAtomic over a non-empty directory succeeded")
		}

		if left := tempFilesIn(t, dir); len(left) != 0 {
			t.Fatalf("temp files left after a failed rename: %v", left)
		}

		if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
			t.Fatalf("existing entry was disturbed: %v", err)
		}
	})

	t.Run("directory does not exist", func(t *testing.T) {
		t.Parallel()

		dir := filepath.Join(t.TempDir(), "missing")
		target := filepath.Join(dir, "x.meta")

		if err := writeFileAtomic(dir, target, []byte("data")); err == nil {
			t.Fatal("writeFileAtomic into a missing directory succeeded")
		}

		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("target exists after a failed write: %v", err)
		}
	})
}

func TestSyncDir(t *testing.T) {
	t.Parallel()

	if err := syncDir(t.TempDir()); err != nil {
		t.Fatalf("syncDir of an existing directory: %v", err)
	}

	if err := syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("syncDir of a missing directory succeeded")
	}
}

// errReader fails after yielding its prefix.
type errReader struct {
	prefix []byte
	err    error
}

func (r *errReader) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]

		return n, nil
	}

	return 0, r.err
}

func TestFilesystemPutFailedReadLeavesNothingVisible(t *testing.T) {
	t.Parallel()

	store := newTestFilesystemStore(t)
	boom := errors.New("connection reset")

	_, err := store.Put(context.Background(), &errReader{prefix: []byte("partial"), err: boom}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the read error", err)
	}

	ids, _, err := store.List(context.Background(), "", 10)
	if err != nil || len(ids) != 0 {
		t.Fatalf("List = %v, %v; a failed Put must publish nothing", ids, err)
	}

	if left := tempFilesIn(t, store.basePath); len(left) != 0 {
		t.Fatalf("temp files left after a failed Put: %v", left)
	}
}

func TestFilesystemPutKeepsExistingBlobAndCleansTemp(t *testing.T) {
	t.Parallel()

	store := newTestFilesystemStore(t)
	ctx := context.Background()

	id, err := store.Put(ctx, strings.NewReader("shared layer"), map[string]string{"owner": "first"})
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]string{"owner": "second"}

	again, err := store.Put(ctx, strings.NewReader("shared layer"), meta)
	if err != nil || again != id {
		t.Fatalf("second Put = %q, %v", again, err)
	}

	if meta["_size"] != "12" || meta["_sha256"] != id {
		t.Fatalf("caller metadata not annotated: %v", meta)
	}

	got, err := store.GetMetadata(ctx, id)
	if err != nil || got["owner"] != "first" {
		t.Fatalf("metadata = %v, %v; the stored sidecar must be untouched by a duplicate Put", got, err)
	}

	if left := tempFilesIn(t, store.basePath); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestFilesystemGetFromOffsets(t *testing.T) {
	t.Parallel()

	store := newTestFilesystemStore(t)
	ctx := context.Background()
	data := []byte("0123456789")

	id, err := store.Put(ctx, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		offset  int64
		want    string
		wantErr error
	}{
		{"start", 0, "0123456789", nil},
		{"middle", 4, "456789", nil},
		{"exactly the size", 10, "", nil},
		{"past the end", 11, "", ErrOffsetOutOfRange},
		{"negative", -1, "", ErrOffsetOutOfRange},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rc, err := store.GetFrom(ctx, id, tc.offset)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if got := string(readAll(t, rc)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	if _, err := store.GetFrom(ctx, sha256Of([]byte("absent")), 3); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("GetFrom of a missing blob = %v, want ErrBlobNotFound", err)
	}

	if _, err := store.GetFrom(ctx, "bad", 3); !errors.Is(err, ErrInvalidBlobID) {
		t.Fatalf("GetFrom with a bad id = %v, want ErrInvalidBlobID", err)
	}
}

func TestFilesystemSizeAndMetadataLookups(t *testing.T) {
	t.Parallel()

	store := newTestFilesystemStore(t)
	ctx := context.Background()

	id, err := store.Put(ctx, strings.NewReader("twelve bytes"), map[string]string{"content_type": "text/plain"})
	if err != nil {
		t.Fatal(err)
	}

	if n, err := store.GetSize(ctx, id); err != nil || n != 12 {
		t.Fatalf("GetSize = %d, %v", n, err)
	}

	md, err := store.GetMetadata(ctx, id)
	if err != nil || md["content_type"] != "text/plain" || md["_size"] != "12" {
		t.Fatalf("GetMetadata = %v, %v", md, err)
	}

	absent := sha256Of([]byte("absent"))

	if _, err := store.GetSize(ctx, absent); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("GetSize of a missing blob = %v, want ErrBlobNotFound", err)
	}

	if md, err := store.GetMetadata(ctx, absent); err == nil || md != nil {
		t.Errorf("GetMetadata of a missing blob = %v, %v; want an error", md, err)
	}

	if _, err := store.GetSize(ctx, "bad"); !errors.Is(err, ErrInvalidBlobID) {
		t.Errorf("GetSize with a bad id = %v", err)
	}

	if _, err := store.GetMetadata(ctx, "bad"); !errors.Is(err, ErrInvalidBlobID) {
		t.Errorf("GetMetadata with a bad id = %v", err)
	}

	// A sidecar that is not valid JSON is reported, not served as empty metadata.
	path, _ := store.getBlobPath(id)
	if err := os.WriteFile(path+".meta", []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if md, err := store.GetMetadata(ctx, id); err == nil || md != nil {
		t.Errorf("GetMetadata of a corrupt sidecar = %v, %v; want an error", md, err)
	}
}

func TestCopyIntoBlob(t *testing.T) {
	t.Parallel()

	payload := []byte("staged payload")

	t.Run("publishes the whole object from the start of src", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		blob := filepath.Join(dir, "blob")
		src := bytes.NewReader(payload)

		if _, err := src.Seek(5, io.SeekStart); err != nil { // a reader left mid-stream is rewound
			t.Fatal(err)
		}

		if err := copyIntoBlob(src, dir, blob); err != nil {
			t.Fatal(err)
		}

		if got, _ := os.ReadFile(blob); !bytes.Equal(got, payload) {
			t.Fatalf("blob = %q, want the full payload", got)
		}

		if left := tempFilesIn(t, dir); len(left) != 0 {
			t.Fatalf("temp files left: %v", left)
		}
	})

	t.Run("a blob that appeared meanwhile is kept", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		blob := filepath.Join(dir, "blob")

		if err := os.WriteFile(blob, []byte("winner"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := copyIntoBlob(bytes.NewReader(payload), dir, blob); err != nil {
			t.Fatal(err)
		}

		if got, _ := os.ReadFile(blob); string(got) != "winner" {
			t.Fatalf("existing blob replaced: %q", got)
		}

		if left := tempFilesIn(t, dir); len(left) != 0 {
			t.Fatalf("temp files left: %v", left)
		}
	})

	t.Run("a failing source publishes nothing and cleans the temp", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		blob := filepath.Join(dir, "blob")
		boom := errors.New("disk gone")

		err := copyIntoBlob(&seekErrReader{errReader{prefix: []byte("part"), err: boom}}, dir, blob)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the read error", err)
		}

		if _, err := os.Stat(blob); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial blob visible: %v", err)
		}

		if left := tempFilesIn(t, dir); len(left) != 0 {
			t.Fatalf("temp files left: %v", left)
		}
	})

	t.Run("an unseekable source is reported", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		if err := copyIntoBlob(&brokenSeeker{}, dir, filepath.Join(dir, "blob")); err == nil {
			t.Fatal("copyIntoBlob with a failing Seek succeeded")
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()

		dir := filepath.Join(t.TempDir(), "missing")

		if err := copyIntoBlob(bytes.NewReader(payload), dir, filepath.Join(dir, "blob")); err == nil {
			t.Fatal("copyIntoBlob into a missing directory succeeded")
		}
	})
}

// seekErrReader is an io.ReadSeeker whose reads fail after a prefix.
type seekErrReader struct{ errReader }

func (*seekErrReader) Seek(int64, int) (int64, error) { return 0, nil }

// brokenSeeker fails to rewind.
type brokenSeeker struct{}

func (*brokenSeeker) Read([]byte) (int, error)       { return 0, io.EOF }
func (*brokenSeeker) Seek(int64, int) (int64, error) { return 0, errors.New("seek: illegal") }
