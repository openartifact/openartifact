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

// errChunkBody is what the failing bodies of the upload tests return.
var errChunkBody = errors.New("chunk body failed")

// uploadHandle is the position a test keeps for an upload, as the database row
// does for the server.
type uploadHandle struct {
	store UploadStore
	id    string
	at    UploadPosition
}

func newUploadHandle(s UploadStore) *uploadHandle {
	return &uploadHandle{store: s, id: uuid.NewString()}
}

// append appends data and records the position the commit callback reported.
func (u *uploadHandle) append(ctx context.Context, r io.Reader) error {
	return u.store.AppendUpload(ctx, u.id, u.at, r, func(_ context.Context, state string, n int64) error {
		u.at = UploadPosition{State: state, Offset: u.at.Offset + n}

		return nil
	})
}

// uploadContract is what every upload store promises, whatever it keeps the
// bytes in.
//
//nolint:gocyclo,cyclop // one subtest per promise
func uploadContract(t *testing.T, s UploadStore) {
	t.Helper()

	if !s.UploadsSupported() {
		t.Fatal("UploadsSupported = false")
	}

	t.Run("appends accumulate into one object", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		var want []byte

		for _, n := range []int{1, 1000, 7, 64 << 10} {
			part := testBytes(n)
			want = append(want, part...)

			if err := u.append(ctx, bytes.NewReader(part)); err != nil {
				t.Fatalf("AppendUpload %d bytes: %v", n, err)
			}

			if u.at.Offset != int64(len(want)) {
				t.Fatalf("offset after append = %d, want %d", u.at.Offset, len(want))
			}
		}

		obj, err := s.CompleteUpload(ctx, u.id, u.at)
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}

		if obj.Size != int64(len(want)) || obj.Version == "" {
			t.Fatalf("CompleteUpload = %+v, want size %d and a version", obj, len(want))
		}

		if got := readAll(t, mustOpenUpload(t, s, u.id, obj.Version)); !bytes.Equal(got, want) {
			t.Fatalf("OpenUpload returned %d bytes that differ from the %d appended", len(got), len(want))
		}

		again, err := s.CompleteUpload(ctx, u.id, u.at)
		if err != nil || again != obj {
			t.Fatalf("second CompleteUpload = %+v, %v; want %+v", again, err, obj)
		}
	})

	t.Run("an empty append commits nothing", func(t *testing.T) {
		t.Parallel()

		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		called := false

		err := s.AppendUpload(t.Context(), u.id, u.at, bytes.NewReader(nil), func(context.Context, string, int64) error {
			called = true

			return nil
		})
		if err != nil || called {
			t.Fatalf("empty append: err = %v, commit called = %v", err, called)
		}
	})

	t.Run("an upload that received nothing completes as the empty object", func(t *testing.T) {
		t.Parallel()

		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		obj, err := s.CompleteUpload(t.Context(), u.id, u.at)
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}

		if obj.Size != 0 {
			t.Fatalf("size = %d, want 0", obj.Size)
		}

		if got := readAll(t, mustOpenUpload(t, s, u.id, obj.Version)); len(got) != 0 {
			t.Fatalf("object holds %d bytes, want none", len(got))
		}
	})

	t.Run("a failed read leaves the position where it was", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		first := testBytes(5000)
		if err := u.append(ctx, bytes.NewReader(first)); err != nil {
			t.Fatal(err)
		}

		held := u.at

		failing := io.MultiReader(bytes.NewReader(testBytes(3000)), iotest.ErrReader(errChunkBody))
		if err := u.append(ctx, failing); !errors.Is(err, errChunkBody) {
			t.Fatalf("append with a failing body = %v, want %v", err, errChunkBody)
		}

		if u.at != held {
			t.Fatalf("position moved to %+v after a failed append, want %+v", u.at, held)
		}

		retry := testBytes(100)
		if err := u.append(ctx, bytes.NewReader(retry)); err != nil {
			t.Fatalf("retry: %v", err)
		}

		obj, err := s.CompleteUpload(ctx, u.id, u.at)
		if err != nil {
			t.Fatal(err)
		}

		want := append(slices.Clone(first), retry...)
		if got := readAll(t, mustOpenUpload(t, s, u.id, obj.Version)); !bytes.Equal(got, want) {
			t.Fatalf("object holds %d bytes, want the first chunk and the retry (%d)", len(got), len(want))
		}
	})

	t.Run("a failed commit leaves the position where it was", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		errCommit := errors.New("commit failed")

		err := s.AppendUpload(ctx, u.id, u.at, bytes.NewReader(testBytes(1000)), func(context.Context, string, int64) error {
			return errCommit
		})
		if !errors.Is(err, errCommit) {
			t.Fatalf("append = %v, want %v", err, errCommit)
		}

		want := testBytes(10)
		if err := u.append(ctx, bytes.NewReader(want)); err != nil {
			t.Fatal(err)
		}

		obj, err := s.CompleteUpload(ctx, u.id, u.at)
		if err != nil {
			t.Fatal(err)
		}

		if got := readAll(t, mustOpenUpload(t, s, u.id, obj.Version)); !bytes.Equal(got, want) {
			t.Fatalf("object holds %d bytes, want only the committed 10", len(got))
		}
	})

	t.Run("completing a position the store does not hold is refused", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		if err := u.append(ctx, bytes.NewReader(testBytes(100))); err != nil {
			t.Fatal(err)
		}

		beyond := UploadPosition{State: u.at.State, Offset: u.at.Offset + 1}
		if _, err := s.CompleteUpload(ctx, u.id, beyond); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload beyond the data = %v, want ErrUploadDataLost", err)
		}

		if _, err := s.CompleteUpload(ctx, uuid.NewString(), UploadPosition{Offset: 5}); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload of an unknown upload = %v, want ErrUploadDataLost", err)
		}
	})

	t.Run("an object changed after completion is refused", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		if err := u.append(ctx, bytes.NewReader(testBytes(100))); err != nil {
			t.Fatal(err)
		}

		obj, err := s.CompleteUpload(ctx, u.id, u.at)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := s.OpenUpload(ctx, u.id, obj.Version+"x"); !errors.Is(err, ErrStagingChanged) {
			t.Fatalf("OpenUpload with another version = %v, want ErrStagingChanged", err)
		}

		if _, err := s.OpenUpload(ctx, uuid.NewString(), obj.Version); !errors.Is(err, ErrStagingNotFound) {
			t.Fatalf("OpenUpload of an unknown upload = %v, want ErrStagingNotFound", err)
		}
	})

	t.Run("delete is idempotent and removes the data", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newUploadHandle(s)

		if err := u.append(ctx, bytes.NewReader(testBytes(100))); err != nil {
			t.Fatal(err)
		}

		obj, err := s.CompleteUpload(ctx, u.id, u.at)
		if err != nil {
			t.Fatal(err)
		}

		for range 2 {
			if err := s.DeleteUpload(ctx, u.id); err != nil {
				t.Fatalf("DeleteUpload: %v", err)
			}
		}

		if _, err := s.OpenUpload(ctx, u.id, obj.Version); !errors.Is(err, ErrStagingNotFound) {
			t.Fatalf("OpenUpload after delete = %v, want ErrStagingNotFound", err)
		}
	})

	t.Run("ids that are not UUIDs are refused", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		for _, id := range []string{"", "../x", "a/b", "not-a-uuid"} {
			err := s.AppendUpload(
				ctx,
				id,
				UploadPosition{},
				bytes.NewReader([]byte("x")),
				func(context.Context, string, int64) error { return nil },
			)
			if !errors.Is(err, errInvalidUploadID) {
				t.Errorf("AppendUpload(%q) = %v, want errInvalidUploadID", id, err)
			}

			if err := s.DeleteUpload(ctx, id); !errors.Is(err, errInvalidUploadID) {
				t.Errorf("DeleteUpload(%q) = %v, want errInvalidUploadID", id, err)
			}
		}
	})

	t.Run("walk reports stored uploads older than the cut off", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := newUploadHandle(s)

		t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

		if err := u.append(ctx, bytes.NewReader(testBytes(10))); err != nil {
			t.Fatal(err)
		}

		if _, err := s.CompleteUpload(ctx, u.id, u.at); err != nil {
			t.Fatal(err)
		}

		if ids := walkUploadIDs(t, s, time.Now().Add(-time.Hour)); slices.Contains(ids, u.id) {
			t.Fatal("an upload newer than olderThan was reported")
		}

		if ids := walkUploadIDs(t, s, time.Now().Add(time.Hour)); !slices.Contains(ids, u.id) {
			t.Fatalf("an upload older than olderThan was not reported: %v", ids)
		}

		stop := errors.New("stop")
		if err := s.WalkUploads(ctx, time.Now().Add(time.Hour), func([]string) error { return stop }); !errors.Is(err, stop) {
			t.Fatalf("WalkUploads = %v, want the callback error", err)
		}
	})
}

func mustOpenUpload(t *testing.T, s UploadStore, id, version string) io.ReadCloser {
	t.Helper()

	rc, err := s.OpenUpload(t.Context(), id, version)
	if err != nil {
		t.Fatalf("OpenUpload: %v", err)
	}

	return rc
}

func walkUploadIDs(t *testing.T, s UploadStore, olderThan time.Time) []string {
	t.Helper()

	var ids []string

	if err := s.WalkUploads(t.Context(), olderThan, func(b []string) error {
		ids = append(ids, b...)

		return nil
	}); err != nil {
		t.Fatalf("WalkUploads: %v", err)
	}

	return ids
}

func TestDirUploadStore(t *testing.T) {
	t.Parallel()

	uploadContract(t, NewDirUploadStore(filepath.Join(t.TempDir(), "uploads")))
}

func TestFilesystemUploadStore(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	uploadContract(t, fsys)
}

// A directory upload store keeps the file at its recorded length: bytes of an
// append that was never committed are cut off, and a file shorter than the
// recorded position means data was lost.
func TestDirUploadStoreFitsTheRecordedLength(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "uploads")
	s := NewDirUploadStore(dir)
	u := newUploadHandle(s)

	if err := u.append(t.Context(), bytes.NewReader(testBytes(100))); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, u.id)

	if err := os.Truncate(path, 40); err != nil {
		t.Fatal(err)
	}

	if err := u.append(t.Context(), bytes.NewReader(testBytes(10))); !errors.Is(err, ErrUploadDataLost) {
		t.Fatalf("append to a truncated file = %v, want ErrUploadDataLost", err)
	}

	if _, err := s.CompleteUpload(t.Context(), u.id, u.at); !errors.Is(err, ErrUploadDataLost) {
		t.Fatalf("CompleteUpload of a truncated file = %v, want ErrUploadDataLost", err)
	}
}

// A completed upload on the blob filesystem becomes the blob without being
// copied; one in a host directory cannot.
func TestPromoteUpload(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	data := testBytes(200 << 10)
	u := newUploadHandle(fsys)

	if err := u.append(t.Context(), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}

	obj, err := fsys.CompleteUpload(t.Context(), u.id, u.at)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256Hex(data)
	meta := map[string]string{"content-type": "application/octet-stream"}

	if err := fsys.PromoteUpload(t.Context(), u.id, sum, obj.Version+"x", meta); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("PromoteUpload of another version = %v, want ErrStagingChanged", err)
	}

	if err := fsys.PromoteUpload(t.Context(), u.id, sum, obj.Version, meta); err != nil {
		t.Fatalf("PromoteUpload: %v", err)
	}

	rc, err := fsys.Get(t.Context(), sum)
	if err != nil {
		t.Fatalf("Get promoted blob: %v", err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, data) {
		t.Fatalf("promoted blob holds %d bytes, want %d", len(got), len(data))
	}

	// The upload can go once the blob exists, and promoting again is a no-op.
	if err := fsys.DeleteUpload(t.Context(), u.id); err != nil {
		t.Fatal(err)
	}

	if err := fsys.PromoteUpload(t.Context(), u.id, sum, obj.Version, meta); err != nil {
		t.Fatalf("PromoteUpload of an existing blob = %v, want nil", err)
	}

	if err := fsys.PromoteUpload(
		t.Context(),
		uuid.NewString(),
		sha256Hex([]byte("other")),
		"1-1",
		meta,
	); !errors.Is(
		err,
		ErrStagingNotFound,
	) {
		t.Fatalf("PromoteUpload of an unknown upload = %v, want ErrStagingNotFound", err)
	}

	dirStore := NewDirUploadStore(t.TempDir())
	if err := dirStore.PromoteUpload(t.Context(), u.id, sum, obj.Version, meta); !errors.Is(err, ErrPromoteNotSupported) {
		t.Fatalf("PromoteUpload on a directory store = %v, want ErrPromoteNotSupported", err)
	}
}

// Uploads stay out of the blob listing and the alias scan of a filesystem.
func TestFilesystemUploadsStayOutOfBlobListing(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	u := newUploadHandle(fsys)
	if err := u.append(t.Context(), bytes.NewReader(testBytes(100))); err != nil {
		t.Fatal(err)
	}

	ids, _, err := fsys.List(t.Context(), "", 100)
	if err != nil {
		t.Fatal(err)
	}

	if len(ids) != 0 {
		t.Fatalf("List = %v, want no blobs", ids)
	}
}

// UploadsFor uses the store when it can hold uploads and a host directory
// otherwise.
func TestUploadsFor(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	fallback := t.TempDir()

	tests := []struct {
		name      string
		store     BlobStore
		wantDir   bool // the upload ends up in the fallback directory
		wantStore bool // the upload ends up in the store's own uploads directory
	}{
		{name: "filesystem", store: fsys, wantStore: true},
		{name: "metrics wrapper of a supporting store", store: NewMetricsBlobStore(fsys, nil), wantStore: true},
		{name: "oci", store: &OCIBlobStore{}, wantDir: true},
		{name: "wrapper of an unsupporting store", store: NewMetricsBlobStore(&OCIBlobStore{}, nil), wantDir: true},
		{name: "store without the interface", store: struct{ BlobStore }{}, wantDir: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := UploadsFor(tc.store, fallback)
			if !s.UploadsSupported() {
				t.Fatal("the selected upload store does not support uploads")
			}

			u := newUploadHandle(s)
			if err := u.append(t.Context(), bytes.NewReader([]byte("x"))); err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = s.DeleteUpload(context.Background(), u.id) })

			_, inFallback := os.Stat(filepath.Join(fallback, u.id))
			_, inStore := os.Stat(filepath.Join(fsys.basePath, filesystemUploadsDir, u.id))

			if (inFallback == nil) != tc.wantDir || (inStore == nil) != tc.wantStore {
				t.Fatalf("upload in fallback dir = %v, in store = %v; want %v, %v",
					inFallback == nil, inStore == nil, tc.wantDir, tc.wantStore)
			}
		})
	}
}

// The OCI backend holds no uploads and says so.
func TestOCIBlobStoreHasNoUploads(t *testing.T) {
	t.Parallel()

	if u, ok := any(&OCIBlobStore{}).(UploadStore); ok && u.UploadsSupported() {
		t.Fatal("the oci backend claims to support uploads")
	}
}

// A forwarder reaches the upload store of its target on every call and answers
// "not supported" for a target without one.
func TestStagingForwarderUploads(t *testing.T) {
	t.Parallel()

	fsys, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	var target BlobStore = &OCIBlobStore{}

	f := NewStagingForwarder(func() BlobStore { return target })

	if f.UploadsSupported() {
		t.Fatal("UploadsSupported with an unsupporting target")
	}

	err = f.AppendUpload(
		t.Context(),
		uuid.NewString(),
		UploadPosition{},
		bytes.NewReader([]byte("x")),
		func(context.Context, string, int64) error { return nil },
	)
	if !errors.Is(err, ErrUploadsNotSupported) {
		t.Fatalf("AppendUpload = %v, want ErrUploadsNotSupported", err)
	}

	target = fsys

	uploadContract(t, f)
}
