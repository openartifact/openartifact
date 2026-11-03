// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

const (
	kib = 1 << 10
	mib = 1 << 20
)

var errUploadTest = errors.New("injected failure")

// uploadSim plays the part of the docker handler: it keeps the position and
// moves it only when the store's commit callback succeeded.
type uploadSim struct {
	t         *testing.T
	s         *S3BlobStore
	id        string
	pos       UploadPosition
	want      []byte
	commitErr error
	commits   int
}

func newUploadSim(t *testing.T, s *S3BlobStore) *uploadSim {
	t.Helper()

	u := &uploadSim{t: t, s: s, id: uuid.NewString()}

	t.Cleanup(func() { _ = s.DeleteUpload(context.WithoutCancel(t.Context()), u.id) })

	return u
}

// append sends r; the caller says what the committed bytes are through want
// when it succeeds (want grows by data only for a committed append).
func (u *uploadSim) append(r io.Reader) error {
	u.t.Helper()

	var (
		state     string
		n         int64
		committed bool
	)

	err := u.s.AppendUpload(u.t.Context(), u.id, u.pos, r, func(_ context.Context, st string, delta int64) error {
		if u.commitErr != nil {
			return u.commitErr
		}

		state, n, committed = st, delta, true

		return nil
	})
	if err == nil && committed {
		u.pos = UploadPosition{State: state, Offset: u.pos.Offset + n}
		u.commits++
	}

	return err
}

// add appends data and records it as stored.
func (u *uploadSim) add(data []byte) {
	u.t.Helper()

	before := u.pos.Offset

	if err := u.append(bytes.NewReader(data)); err != nil {
		u.t.Fatalf("append %d bytes at %d: %v", len(data), before, err)
	}

	if got := u.pos.Offset - before; got != int64(len(data)) {
		u.t.Fatalf("position moved by %d, want %d", got, len(data))
	}

	u.want = append(u.want, data...)
}

func randomUploadBytes(t *testing.T, n int) []byte {
	t.Helper()

	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return b
}

func (u *uploadSim) complete() StagedObject {
	u.t.Helper()

	obj, err := u.s.CompleteUpload(u.t.Context(), u.id, u.pos)
	if err != nil {
		u.t.Fatalf("CompleteUpload: %v", err)
	}

	if obj.Size != int64(len(u.want)) || obj.Version == "" {
		u.t.Fatalf("CompleteUpload = %+v, want %d bytes and a version", obj, len(u.want))
	}

	return obj
}

// requireContent reads the completed upload back and compares it with want.
func (u *uploadSim) requireContent(obj StagedObject) {
	u.t.Helper()

	rc, err := u.s.OpenUpload(u.t.Context(), u.id, obj.Version)
	if err != nil {
		u.t.Fatalf("OpenUpload: %v", err)
	}

	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		u.t.Fatal(err)
	}

	if !bytes.Equal(got, u.want) {
		u.t.Fatalf("upload holds %d bytes, want %d (content differs: %v)", len(got), len(u.want), len(got) == len(u.want))
	}
}

func requireNoUploadObjects(t *testing.T, s *S3BlobStore, id string) {
	t.Helper()

	out, err := s.client.ListObjectsV2(t.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket), Prefix: aws.String(uploadPrefix + id),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, o := range out.Contents {
		t.Errorf("object %s is left", *o.Key)
	}

	if open, err := s.multipartIDs(t.Context(), uploadPrefix+id, time.Now().Add(time.Hour), uploadObjectID); err != nil || len(open) > 0 {
		t.Errorf("multipart uploads left: %v, %v", open, err)
	}
}

func chunkSizes(sizes ...int) string {
	parts := make([]string, len(sizes))
	for i, n := range sizes {
		parts[i] = strconv.Itoa(n)
	}

	return strings.Join(parts, "+")
}

// Any run of PATCH sizes below, at and above the S3 minimum part and the part
// size completes to one object identical to the concatenation, which a second
// CompleteUpload reports the same, and which promotes to the blob.
func TestS3UploadsChunkSequences(t *testing.T) {
	s := integrationS3(t)

	sequences := [][]int{
		{1 * kib},
		{1 * kib, 1 * kib, 1 * kib},
		{0},
		{0, 0, 1 * kib, 0},
		{3 * mib, 3 * mib, 3 * mib},
		{6 * mib},
		{20 * mib},
		{4 * mib, 1 * mib, 1},
		{5 * mib},
		{5*mib - 1, 1},
		{s3PartSize},
		{s3PartSize, s3PartSize, 7},
		{1 * kib, 20 * mib, 3 * mib, 100, 6 * mib},
		{17 * mib, 17 * mib},
	}

	for _, seq := range sequences {
		t.Run(chunkSizes(seq...), func(t *testing.T) {
			u := newUploadSim(t, s)

			for _, n := range seq {
				u.add(randomUploadBytes(t, n))
			}

			obj := u.complete()
			u.requireContent(obj)

			again, err := s.CompleteUpload(t.Context(), u.id, u.pos)
			if err != nil || again != obj {
				t.Fatalf("second CompleteUpload = %+v, %v; want %+v", again, err, obj)
			}

			sum := sha256.Sum256(u.want)
			sha := hex.EncodeToString(sum[:])

			t.Cleanup(func() { _ = s.Delete(context.WithoutCancel(t.Context()), sha) })

			if err := s.PromoteUpload(t.Context(), u.id, sha, obj.Version, map[string]string{"k": "v"}); err != nil {
				t.Fatalf("PromoteUpload: %v", err)
			}

			rc, err := s.Get(t.Context(), sha)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = rc.Close() }()

			if got, _ := io.ReadAll(rc); !bytes.Equal(got, u.want) {
				t.Fatal("the promoted blob differs")
			}

			if err := s.DeleteUpload(t.Context(), u.id); err != nil {
				t.Fatal(err)
			}

			requireNoUploadObjects(t, s, u.id)
		})
	}
}

// An append that fails (the body breaks, commit refuses) leaves the position
// where it was; the retry from there, even cut differently, gives correct data.
func TestS3UploadsResumeAfterFailure(t *testing.T) {
	s := integrationS3(t)

	t.Run("body breaks mid-way", func(t *testing.T) {
		u := newUploadSim(t, s)
		u.add(randomUploadBytes(t, 3*mib))

		data := randomUploadBytes(t, 20*mib)
		pos := u.pos

		broken := io.MultiReader(bytes.NewReader(data[:18*mib]), iotest.ErrReader(errUploadTest))
		if err := u.append(broken); !errors.Is(err, errUploadTest) {
			t.Fatalf("append of a broken body = %v, want the body error", err)
		}

		if u.pos != pos || u.commits != 1 {
			t.Fatalf("position moved to %+v after a failure", u.pos)
		}

		u.add(data)
		u.add(randomUploadBytes(t, 700*kib))
		u.requireContent(u.complete())
	})

	t.Run("retry is cut differently", func(t *testing.T) {
		u := newUploadSim(t, s)
		u.add(randomUploadBytes(t, 4*mib))

		data := randomUploadBytes(t, 40*mib)

		broken := io.MultiReader(bytes.NewReader(data[:30*mib]), iotest.ErrReader(errUploadTest))
		if err := u.append(broken); !errors.Is(err, errUploadTest) {
			t.Fatalf("append = %v", err)
		}

		u.add(data[:2*mib])
		u.add(data[2*mib : 2*mib+1])
		u.add(data[2*mib+1:])
		u.requireContent(u.complete())
	})

	t.Run("commit refuses", func(t *testing.T) {
		u := newUploadSim(t, s)
		u.add(randomUploadBytes(t, 2*mib))

		data := randomUploadBytes(t, 22*mib)
		pos := u.pos

		u.commitErr = errUploadTest
		if err := u.append(bytes.NewReader(data)); !errors.Is(err, errUploadTest) {
			t.Fatalf("append = %v, want the commit error", err)
		}

		u.commitErr = nil

		if u.pos != pos {
			t.Fatalf("position moved to %+v after a refused commit", u.pos)
		}

		// The tail of the position the caller still holds must have survived.
		if _, err := s.statObject(t.Context(), tailKey(uploadPrefix+u.id, pos.Offset)); err != nil {
			t.Fatalf("the tail of the held position is gone: %v", err)
		}

		u.add(data)
		u.requireContent(u.complete())
	})

	t.Run("first append fails after a part was sent", func(t *testing.T) {
		u := newUploadSim(t, s)

		data := randomUploadBytes(t, 25*mib)
		broken := io.MultiReader(bytes.NewReader(data[:24*mib]), iotest.ErrReader(errUploadTest))

		if err := u.append(broken); !errors.Is(err, errUploadTest) {
			t.Fatalf("append = %v", err)
		}

		u.add(data)
		u.requireContent(u.complete())

		// DeleteUpload also aborts the multipart upload the failed attempt left.
		if err := s.DeleteUpload(t.Context(), u.id); err != nil {
			t.Fatal(err)
		}

		requireNoUploadObjects(t, s, u.id)
	})
}

// An empty body is not an append: nothing is committed or stored.
func TestS3UploadsEmptyAppendCommitsNothing(t *testing.T) {
	s := integrationS3(t)
	u := newUploadSim(t, s)
	u.add(randomUploadBytes(t, 1*kib))

	if err := u.append(bytes.NewReader(nil)); err != nil || u.commits != 1 {
		t.Fatalf("empty append = %v, %d commits", err, u.commits)
	}
}

// Data the position refers to that is gone is reported as lost, not papered over.
func TestS3UploadsDataLost(t *testing.T) {
	s := integrationS3(t)

	t.Run("tail missing", func(t *testing.T) {
		u := newUploadSim(t, s)
		u.add(randomUploadBytes(t, 3*mib))

		if err := s.deleteObject(t.Context(), tailKey(uploadPrefix+u.id, u.pos.Offset)); err != nil {
			t.Fatal(err)
		}

		if err := u.append(bytes.NewReader([]byte("more"))); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("append = %v, want ErrUploadDataLost", err)
		}

		if _, err := s.CompleteUpload(t.Context(), u.id, u.pos); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload = %v, want ErrUploadDataLost", err)
		}
	})

	t.Run("position beyond the stored bytes", func(t *testing.T) {
		u := newUploadSim(t, s)
		u.add(randomUploadBytes(t, 20*mib))

		pos := u.pos
		pos.Offset += 100

		if _, err := s.CompleteUpload(t.Context(), u.id, pos); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload at a wrong offset = %v, want ErrUploadDataLost", err)
		}
	})

	t.Run("multipart upload aborted", func(t *testing.T) {
		u := newUploadSim(t, s)
		u.add(randomUploadBytes(t, 20*mib))

		if err := s.abortKeyMultipart(t.Context(), uploadPrefix+u.id); err != nil {
			t.Fatal(err)
		}

		if _, err := s.CompleteUpload(t.Context(), u.id, u.pos); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload = %v, want ErrUploadDataLost", err)
		}

		if err := u.append(bytes.NewReader([]byte("more"))); err != nil {
			// The next part may be the tail only; either outcome must not corrupt.
			t.Logf("append after abort: %v", err)
		}
	})

	t.Run("nothing at all", func(t *testing.T) {
		id := uuid.NewString()

		if _, err := s.CompleteUpload(t.Context(), id, UploadPosition{Offset: 10}); !errors.Is(err, ErrUploadDataLost) {
			t.Fatalf("CompleteUpload = %v, want ErrUploadDataLost", err)
		}
	})
}

// OpenUpload and PromoteUpload refuse an object that is not the verified version.
func TestS3UploadsVersionChecks(t *testing.T) {
	s := integrationS3(t)
	u := newUploadSim(t, s)
	u.add(randomUploadBytes(t, 20*mib))
	obj := u.complete()

	if _, err := s.OpenUpload(t.Context(), u.id, `"deadbeef-1"`); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("OpenUpload of another version = %v, want ErrStagingChanged", err)
	}

	sha := strings.Repeat("ab", 32)
	t.Cleanup(func() { _ = s.Delete(context.WithoutCancel(t.Context()), sha) })

	if err := s.PromoteUpload(t.Context(), u.id, sha, `"deadbeef-1"`, nil); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("PromoteUpload of another version = %v, want ErrStagingChanged", err)
	}

	if ok, _ := s.Exists(t.Context(), sha); ok {
		t.Fatal("a refused promote left a blob")
	}

	gone := uuid.NewString()
	if _, err := s.OpenUpload(t.Context(), gone, obj.Version); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("OpenUpload of a missing upload = %v, want ErrStagingNotFound", err)
	}

	if err := s.PromoteUpload(t.Context(), gone, sha, obj.Version, nil); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("PromoteUpload of a missing upload = %v, want ErrStagingNotFound", err)
	}
}

// DeleteUpload removes the object, the tails and the multipart upload, leaves
// other uploads alone, and a missing upload is not an error.
func TestS3UploadsDelete(t *testing.T) {
	s := integrationS3(t)

	other := newUploadSim(t, s)
	other.add(randomUploadBytes(t, 20*mib))

	u := newUploadSim(t, s)
	u.add(randomUploadBytes(t, 20*mib))
	u.add(randomUploadBytes(t, 1*kib))

	// Unfinished, with a tail and an open multipart upload.
	if err := s.DeleteUpload(t.Context(), u.id); err != nil {
		t.Fatal(err)
	}

	requireNoUploadObjects(t, s, u.id)

	// Completed.
	done := newUploadSim(t, s)
	done.add(randomUploadBytes(t, 20*mib))
	done.add(randomUploadBytes(t, 1*kib))
	done.complete()

	if err := s.DeleteUpload(t.Context(), done.id); err != nil {
		t.Fatal(err)
	}

	requireNoUploadObjects(t, s, done.id)

	if err := s.DeleteUpload(t.Context(), uuid.NewString()); err != nil {
		t.Fatalf("DeleteUpload of nothing = %v", err)
	}

	if err := s.DeleteUpload(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("DeleteUpload accepted an id that is not a UUID")
	}

	// The unrelated upload is intact.
	other.add(randomUploadBytes(t, 1*kib))
	other.requireContent(other.complete())
}

// WalkUploads reports uploads that are older than the cutoff (object, tail or
// unfinished multipart upload) and not fresh ones.
func TestS3UploadsWalk(t *testing.T) {
	s := integrationS3(t)

	withTail := newUploadSim(t, s)
	withTail.add(randomUploadBytes(t, 1*kib))

	withParts := newUploadSim(t, s)
	withParts.add(randomUploadBytes(t, 17*mib))

	completed := newUploadSim(t, s)
	completed.add(randomUploadBytes(t, 3*kib))
	completed.complete()

	// A multipart upload with no part stored yet is only visible as such.
	bare := uuid.NewString()
	if _, err := s.createUpload(t.Context(), uploadPrefix+bare); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.DeleteUpload(context.WithoutCancel(t.Context()), bare) })

	walk := func(olderThan time.Time) []string {
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

	fresh := walk(time.Now().Add(-time.Hour))
	for _, id := range []string{withTail.id, withParts.id, completed.id, bare} {
		if slices.Contains(fresh, id) {
			t.Errorf("WalkUploads reported the fresh upload %s", id)
		}
	}

	old := walk(time.Now().Add(time.Hour))
	for _, id := range []string{withTail.id, withParts.id, completed.id, bare} {
		if !slices.Contains(old, id) {
			t.Errorf("WalkUploads missed the old upload %s", id)
		}
	}

	// Staging objects and blobs are not uploads.
	for _, id := range old {
		if validUploadID(id) != nil {
			t.Errorf("WalkUploads reported %q, not an upload id", id)
		}
	}
}

// Uploads, tails and their multipart uploads stay out of the blob listing.
func TestS3UploadsStayOutOfBlobListing(t *testing.T) {
	s := integrationS3(t)
	u := newUploadSim(t, s)
	u.add(randomUploadBytes(t, 20*mib))
	u.add(randomUploadBytes(t, 1*kib))
	u.complete()

	token := ""

	for {
		ids, next, err := s.List(t.Context(), token, 1000)
		if err != nil {
			t.Fatal(err)
		}

		if slices.ContainsFunc(ids, func(id string) bool { return strings.Contains(id, u.id) }) {
			t.Fatal("List returned an upload")
		}

		if next == "" {
			break
		}

		token = next
	}
}

// requestCounter counts the S3 requests that go through an http.Client.
type requestCounter struct {
	mu     sync.Mutex
	counts map[string]int
	bytes  int64
}

func (c *requestCounter) RoundTrip(req *http.Request) (*http.Response, error) {
	op := req.Method
	if q := req.URL.Query(); q.Has("uploadId") || q.Has("uploads") || q.Has("partNumber") {
		op += " multipart"
	}

	c.mu.Lock()
	c.counts[op]++

	if req.ContentLength > 0 {
		c.bytes += req.ContentLength
	}

	c.mu.Unlock()

	return http.DefaultTransport.RoundTrip(req)
}

// Logs what a push of a layer costs in requests and bytes sent: the layer is
// sent once and never read back to the server (scaled down from 500 MB).
func TestS3UploadsRequestCost(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 120 MiB")
	}

	s := integrationS3(t)
	counter := &requestCounter{counts: map[string]int{}}

	opts := s.client.Options()
	opts.HTTPClient = &http.Client{Transport: counter}
	s.client = s3.New(opts)

	u := newUploadSim(t, s)

	const patches, patchSize = 10, 12 * mib

	for range patches {
		u.add(randomUploadBytes(t, patchSize))
	}

	obj := u.complete()
	sum := sha256.Sum256(u.want)
	sha := hex.EncodeToString(sum[:])

	t.Cleanup(func() { _ = s.Delete(context.WithoutCancel(t.Context()), sha) })

	if err := s.PromoteUpload(t.Context(), u.id, sha, obj.Version, nil); err != nil {
		t.Fatal(err)
	}

	total := int64(patches * patchSize)

	t.Logf("%d MiB in %d PATCHes: %d MiB sent to the store (%.2fx), requests %v",
		total/mib, patches, counter.bytes/mib, float64(counter.bytes)/float64(total), counter.counts)

	if counter.bytes > total+total/10 {
		t.Errorf("sent %d bytes for a %d byte layer", counter.bytes, total)
	}
}
