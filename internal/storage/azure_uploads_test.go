// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
)

var errAzureUploadCommit = errors.New("commit failed")

// azureUploadFile is the upload store of a caller that records the position the
// way the server does: only when commit returned nil.
type azureUploadFile struct {
	t   *testing.T
	s   *AzureBlobStore
	id  string
	pos UploadPosition
}

func newAzureUploadFile(t *testing.T, s *AzureBlobStore) *azureUploadFile {
	t.Helper()

	return &azureUploadFile{t: t, s: s, id: uuid.NewString()}
}

// append appends r and returns the commit calls it made.
func (u *azureUploadFile) append(r io.Reader) (int, error) {
	u.t.Helper()

	calls := 0

	err := u.s.AppendUpload(u.t.Context(), u.id, u.pos, r, func(_ context.Context, state string, n int64) error {
		calls++
		u.pos = UploadPosition{State: state, Offset: u.pos.Offset + n}

		return nil
	})

	return calls, err
}

func (u *azureUploadFile) mustAppend(data []byte) {
	u.t.Helper()

	if _, err := u.append(bytes.NewReader(data)); err != nil {
		u.t.Fatalf("AppendUpload(%d bytes) = %v", len(data), err)
	}
}

func (u *azureUploadFile) complete() StagedObject {
	u.t.Helper()

	obj, err := u.s.CompleteUpload(u.t.Context(), u.id, u.pos)
	if err != nil {
		u.t.Fatalf("CompleteUpload = %v", err)
	}

	return obj
}

// requireBlob checks that the blob with the digest of want holds want.
func requireAzureBlob(t *testing.T, s *AzureBlobStore, want []byte) string {
	t.Helper()

	sha := sha256Hex(want)

	rc, err := s.Get(t.Context(), sha)
	if err != nil {
		t.Fatalf("Get(%s) = %v", sha, err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, want) {
		t.Fatalf("blob has %d bytes, want %d (content differs: %v)", len(got), len(want), len(got) == len(want))
	}

	return sha
}

func azureUploadData(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%251)
	}

	return b
}

func walkUploadsAll(t *testing.T, s UploadStore, olderThan time.Time) []string {
	t.Helper()

	var ids []string

	err := s.WalkUploads(t.Context(), olderThan, func(batch []string) error {
		ids = append(ids, batch...)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return ids
}

// Every append is staged block by block (8 MiB at most), nothing is committed
// until CompleteUpload, and the completed upload promotes to the blob that
// holds the concatenation of all appends.
func TestAzureUploadsAppendCompletePromote(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	u := newAzureUploadFile(t, s)

	var want []byte

	steps := []struct {
		size       int
		wantBlocks int
		wantCommit int
	}{
		{1 << 10, 1, 1},
		{3 << 20, 1, 1},
		{9 << 20, 2, 1},
		{17 << 20, 3, 1},
		{0, 0, 0},
	}

	for i, step := range steps {
		data := azureUploadData(step.size, byte(i))
		blocksBefore := f.opCount("put-block")

		calls, err := u.append(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("step %d: AppendUpload = %v", i, err)
		}

		if got := f.opCount("put-block") - blocksBefore; got != step.wantBlocks {
			t.Errorf("step %d: put-block calls = %d, want %d", i, got, step.wantBlocks)
		}

		if calls != step.wantCommit {
			t.Errorf("step %d: commit calls = %d, want %d", i, calls, step.wantCommit)
		}

		want = append(want, data...)

		if u.pos.Offset != int64(len(want)) {
			t.Fatalf("step %d: offset = %d, want %d", i, u.pos.Offset, len(want))
		}
	}

	if f.blob(uploadPrefix+u.id) != nil {
		t.Fatal("the upload is a blob before it was completed")
	}

	obj := u.complete()
	if obj.Size != int64(len(want)) || obj.Version == "" {
		t.Fatalf("CompleteUpload = %+v, want size %d and a version", obj, len(want))
	}

	if got := f.blob(uploadPrefix + u.id); got == nil || got.etag != obj.Version {
		t.Fatalf("the completed object is %+v, want version %s", got, obj.Version)
	}

	sha := sha256Hex(want)
	if err := s.PromoteUpload(t.Context(), u.id, sha, obj.Version, nil); err != nil {
		t.Fatalf("PromoteUpload = %v", err)
	}

	requireAzureBlob(t, s, want)

	if f.opCount("copy") != 1 {
		t.Errorf("copy calls = %d, want the one server-side copy", f.opCount("copy"))
	}

	rc, err := s.OpenUpload(t.Context(), u.id, obj.Version)
	if err != nil {
		t.Fatalf("OpenUpload = %v", err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, want) {
		t.Fatalf("OpenUpload returned %d bytes, want %d", len(got), len(want))
	}
}

// A read error in the middle of a block and a commit that fails leave the
// recorded position where it was; the retry from there overwrites what the
// failed attempts staged, whatever its size.
func TestAzureUploadsRetryAfterFailure(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	u := newAzureUploadFile(t, s)

	first := azureUploadData(2<<20, 1)
	u.mustAppend(first)

	// One full block is staged, then the body fails.
	failing := io.MultiReader(bytes.NewReader(azureUploadData(azureUploadBlockSize+100, 2)), iotest.ErrReader(errChunkBody))
	if _, err := u.append(failing); !errors.Is(err, errChunkBody) {
		t.Fatalf("AppendUpload with a failing body = %v, want %v", err, errChunkBody)
	}

	if u.pos.Offset != int64(len(first)) {
		t.Fatalf("offset after the failed append = %d, want %d", u.pos.Offset, len(first))
	}

	if f.stagedBlocks(uploadPrefix+u.id) != 2 {
		t.Fatalf("staged blocks = %d, want the committed one and the failed block", f.stagedBlocks(uploadPrefix+u.id))
	}

	// Everything staged but commit refuses.
	lost := azureUploadData(3<<20, 3)
	before := u.pos

	err := s.AppendUpload(t.Context(), u.id, u.pos, bytes.NewReader(lost), func(context.Context, string, int64) error {
		return errAzureUploadCommit
	})
	if !errors.Is(err, errAzureUploadCommit) {
		t.Fatalf("AppendUpload with a failing commit = %v", err)
	}

	if u.pos != before {
		t.Fatalf("position moved to %+v", u.pos)
	}

	// The retry is shorter than the failed attempts.
	retry := azureUploadData(500, 4)
	u.mustAppend(retry)

	last := azureUploadData(12<<20, 5)
	u.mustAppend(last)

	want := slices.Concat(first, retry, last)
	obj := u.complete()

	if obj.Size != int64(len(want)) {
		t.Fatalf("size = %d, want %d", obj.Size, len(want))
	}

	if err := s.PromoteUpload(t.Context(), u.id, sha256Hex(want), obj.Version, nil); err != nil {
		t.Fatal(err)
	}

	requireAzureBlob(t, s, want)
}

// The blocks of a failed append that went past the position at completion are
// not part of the object.
func TestAzureUploadsCompleteIgnoresStrayBlocks(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	u := newAzureUploadFile(t, s)

	data := azureUploadData(1<<20, 1)
	u.mustAppend(data)

	failing := io.MultiReader(bytes.NewReader(azureUploadData(azureUploadBlockSize, 2)), iotest.ErrReader(errChunkBody))
	if _, err := u.append(failing); err == nil {
		t.Fatal("AppendUpload succeeded")
	}

	obj := u.complete()
	if obj.Size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", obj.Size, len(data))
	}

	if f.stagedBlocks(uploadPrefix+u.id) != 0 {
		t.Fatal("blocks are still staged after the commit")
	}

	if got := f.blob(uploadPrefix + u.id); got == nil || !bytes.Equal(got.data, data) {
		t.Fatal("the completed object holds the stray block")
	}
}

func TestAzureUploadsCompleteTwice(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	u := newAzureUploadFile(t, s)

	u.mustAppend(azureUploadData(9<<20, 1))

	first := u.complete()
	second := u.complete()

	if first != second {
		t.Fatalf("second CompleteUpload = %+v, want %+v", second, first)
	}

	if f.opCount("put-block-list") != 1 {
		t.Errorf("put-block-list calls = %d, want 1", f.opCount("put-block-list"))
	}

	// A position that disagrees with the finished object is refused.
	bad := u.pos
	bad.Offset++

	if _, err := s.CompleteUpload(t.Context(), u.id, bad); !errors.Is(err, ErrUploadDataLost) {
		t.Fatalf("CompleteUpload at the wrong offset = %v, want ErrUploadDataLost", err)
	}
}

func TestAzureUploadsEmpty(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	u := newAzureUploadFile(t, s)

	if calls, err := u.append(bytes.NewReader(nil)); err != nil || calls != 0 {
		t.Fatalf("AppendUpload of nothing = %d commits, %v", calls, err)
	}

	obj := u.complete()
	if obj.Size != 0 || obj.Version == "" {
		t.Fatalf("CompleteUpload = %+v", obj)
	}

	if again := u.complete(); again != obj {
		t.Fatalf("second CompleteUpload = %+v, want %+v", again, obj)
	}

	if f.opCount("put-block-list") != 1 {
		t.Errorf("put-block-list calls = %d, want 1", f.opCount("put-block-list"))
	}

	if err := s.PromoteUpload(t.Context(), u.id, sha256Hex(nil), obj.Version, nil); err != nil {
		t.Fatal(err)
	}

	requireAzureBlob(t, s, nil)
}

// An upload that never received data cannot be completed at a position that
// claims bytes.
func TestAzureUploadsDataLost(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})

	t.Run("block missing", func(t *testing.T) {
		t.Parallel()

		u := newAzureUploadFile(t, s)

		for i := range 3 {
			u.mustAppend(azureUploadData(1<<20, byte(i)))
		}

		f.mu.Lock()
		delete(f.staged[uploadPrefix+u.id], azureBlockID(2))
		f.mu.Unlock()

		if _, err := s.CompleteUpload(t.Context(), u.id, u.pos); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload = %v, want ErrUploadDataLost", err)
		}
	})

	t.Run("wrong size", func(t *testing.T) {
		t.Parallel()

		u := newAzureUploadFile(t, s)
		u.mustAppend(azureUploadData(1<<20, 1))

		at := u.pos
		at.Offset--

		if _, err := s.CompleteUpload(t.Context(), u.id, at); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload = %v, want ErrUploadDataLost", err)
		}
	})

	t.Run("nothing stored", func(t *testing.T) {
		t.Parallel()

		at := UploadPosition{State: `{"n":2}`, Offset: 10}

		if _, err := s.CompleteUpload(t.Context(), uuid.NewString(), at); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload = %v, want ErrUploadDataLost", err)
		}
	})

	t.Run("invalid state", func(t *testing.T) {
		t.Parallel()

		for _, state := range []string{"x", `{"n":-1}`, `{"n":50001}`} {
			at := UploadPosition{State: state}

			if _, err := s.CompleteUpload(t.Context(), uuid.NewString(), at); !errors.Is(err, ErrUploadDataLost) {
				t.Errorf("CompleteUpload(%q) = %v, want ErrUploadDataLost", state, err)
			}

			err := s.AppendUpload(t.Context(), uuid.NewString(), at, bytes.NewReader([]byte("x")), nil)
			if !errors.Is(err, ErrUploadDataLost) {
				t.Errorf("AppendUpload(%q) = %v, want ErrUploadDataLost", state, err)
			}
		}
	})
}

func TestAzureUploadsTooLarge(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	id := uuid.NewString()
	at := UploadPosition{State: `{"n":50000}`, Offset: 50000}

	err := s.AppendUpload(t.Context(), id, at, bytes.NewReader([]byte("x")), func(context.Context, string, int64) error {
		t.Fatal("commit called")

		return nil
	})
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("AppendUpload = %v, want ErrUploadTooLarge", err)
	}

	if f.opCount("put-block") != 0 {
		t.Fatal("a block was staged beyond the limit")
	}
}

// The version taken at completion pins the object: promoting or opening it
// after it changed, or when it is gone, is refused.
func TestAzureUploadsVersion(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	u := newAzureUploadFile(t, s)
	data := azureUploadData(1<<20, 1)
	u.mustAppend(data)

	obj := u.complete()
	key := uploadPrefix + u.id

	// Overwritten after the digest was verified.
	evil := []byte("something else")
	f.seed(key, evil, nil)

	sha := sha256Hex(data)

	if err := s.PromoteUpload(ctx, u.id, sha, obj.Version, nil); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("PromoteUpload of a changed object = %v, want ErrStagingChanged", err)
	}

	if ok, err := s.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("the blob exists = %v, %v", ok, err)
	}

	if _, err := s.OpenUpload(ctx, u.id, obj.Version); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("OpenUpload of a changed object = %v, want ErrStagingChanged", err)
	}

	if _, err := s.OpenUpload(ctx, u.id, ""); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("OpenUpload without a version = %v, want ErrStagingChanged", err)
	}

	missing := uuid.NewString()

	if _, err := s.OpenUpload(ctx, missing, obj.Version); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("OpenUpload of a missing upload = %v, want ErrStagingNotFound", err)
	}

	if err := s.PromoteUpload(ctx, missing, sha, obj.Version, nil); err == nil {
		t.Fatal("PromoteUpload of a missing upload succeeded")
	}
}

func TestAzureUploadsDelete(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	u := newAzureUploadFile(t, s)
	u.mustAppend(azureUploadData(1<<20, 1))

	obj := u.complete()

	if err := s.DeleteUpload(ctx, u.id); err != nil {
		t.Fatal(err)
	}

	if f.blob(uploadPrefix+u.id) != nil {
		t.Fatal("the upload is still there")
	}

	if _, err := s.OpenUpload(ctx, u.id, obj.Version); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("OpenUpload after delete = %v, want ErrStagingNotFound", err)
	}

	// Missing and never-completed uploads are fine.
	if err := s.DeleteUpload(ctx, u.id); err != nil {
		t.Fatalf("second DeleteUpload = %v", err)
	}

	if err := s.DeleteUpload(ctx, uuid.NewString()); err != nil {
		t.Fatalf("DeleteUpload of an unknown upload = %v", err)
	}

	if err := s.DeleteUpload(ctx, "../blobs"); err == nil {
		t.Fatal("DeleteUpload accepted an invalid id")
	}
}

func TestAzureUploadsWalk(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	start := time.Now().Truncate(time.Second)

	f.freeze(start)

	old := newAzureUploadFile(t, s)
	old.mustAppend([]byte("old"))
	old.complete()

	// A blob of the previous release (a plain uuid) is reported as well.
	legacy := uuid.NewString()
	f.seed(uploadPrefix+legacy, []byte("legacy"), nil)
	f.seed(uploadPrefix+"not-a-uuid", []byte("x"), nil)

	// An upload that was never completed has no blob to walk.
	pending := newAzureUploadFile(t, s)
	pending.mustAppend([]byte("pending"))

	f.advance(2 * time.Hour)

	fresh := newAzureUploadFile(t, s)
	fresh.mustAppend([]byte("fresh"))
	fresh.complete()

	ids := walkUploadsAll(t, s, start.Add(time.Hour))
	slices.Sort(ids)

	want := []string{old.id, legacy}
	slices.Sort(want)

	if !slices.Equal(ids, want) {
		t.Fatalf("walk = %v, want %v", ids, want)
	}

	if all := walkUploadsAll(t, s, start.Add(3*time.Hour)); len(all) != 3 {
		t.Fatalf("walk of everything = %v, want 3 uploads", all)
	}

	// fn errors stop the walk.
	errStop := errors.New("stop")

	if err := s.WalkUploads(t.Context(), start.Add(3*time.Hour), func([]string) error { return errStop }); !errors.Is(err, errStop) {
		t.Fatalf("WalkUploads = %v, want %v", err, errStop)
	}
}

// The walk batches per list page.
func TestAzureUploadsWalkPages(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	f.pageSize = 2

	var want []string

	for range 5 {
		id := uuid.NewString()
		f.seed(uploadPrefix+id, []byte("x"), nil)

		want = append(want, id)
	}

	var (
		got     []string
		batches int
	)

	err := s.WalkUploads(t.Context(), time.Now().Add(time.Hour), func(ids []string) error {
		batches++

		got = append(got, ids...)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) || batches != 3 {
		t.Fatalf("walk = %v in %d batches, want %v in 3", got, batches, want)
	}
}

func TestAzureUploadsInvalidID(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	if !s.UploadsSupported() {
		t.Fatal("UploadsSupported = false")
	}

	if err := s.AppendUpload(ctx, "nope", UploadPosition{}, bytes.NewReader([]byte("x")), nil); err == nil {
		t.Error("AppendUpload accepted an invalid id")
	}

	if _, err := s.CompleteUpload(ctx, "nope", UploadPosition{}); err == nil {
		t.Error("CompleteUpload accepted an invalid id")
	}

	if _, err := s.OpenUpload(ctx, "nope", "v"); err == nil {
		t.Error("OpenUpload accepted an invalid id")
	}

	if err := s.PromoteUpload(ctx, "nope", sha256Hex(nil), "v", nil); err == nil {
		t.Error("PromoteUpload accepted an invalid id")
	}
}
