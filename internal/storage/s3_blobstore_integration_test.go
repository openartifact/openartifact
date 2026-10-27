// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// randomBlob returns fresh content and its digest, so reruns against a
// long-lived bucket never collide with leftovers.
func randomBlob(t *testing.T, size int) ([]byte, string) {
	t.Helper()

	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	return data, sha256Of(data)
}

func TestS3BlobRoundTrip(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	data, sha := randomBlob(t, 4096)

	t.Cleanup(func() { _ = s.Delete(ctx, sha) })

	meta := map[string]string{"sha256": sha, "format": "generic"}

	id, err := s.Put(ctx, bytes.NewReader(data), meta)
	if err != nil || id != sha {
		t.Fatalf("Put = %q, %v", id, err)
	}

	rc, err := s.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, data) {
		t.Fatal("Get returned different bytes")
	}

	if n, err := s.GetSize(ctx, sha); err != nil || n != int64(len(data)) {
		t.Fatalf("GetSize = %d, %v", n, err)
	}

	md, err := s.GetMetadata(ctx, sha)
	if err != nil || md["sha256"] != sha || md["format"] != "generic" {
		t.Fatalf("GetMetadata = %v, %v", md, err)
	}

	if ok, err := s.Exists(ctx, sha); err != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, err)
	}

	if key, ok, err := s.ExistsBySHA256(ctx, sha); err != nil || !ok || key != sha {
		t.Fatalf("ExistsBySHA256 = %q, %v, %v", key, ok, err)
	}

	// A second Put of the same digest leaves the stored object (and its metadata) alone.
	if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha, "format": "other"}); err != nil {
		t.Fatal(err)
	}

	if md, _ := s.GetMetadata(ctx, sha); md["format"] != "generic" {
		t.Fatalf("duplicate Put replaced the metadata: %v", md)
	}

	if err := s.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}

	if ok, err := s.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("Exists after Delete = %v, %v", ok, err)
	}
}

func TestS3BlobNotFoundAndIdempotentDelete(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	_, sha := randomBlob(t, 16)

	if _, err := s.Get(ctx, sha); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("Get = %v, want ErrBlobNotFound", err)
	}

	if _, err := s.GetFrom(ctx, sha, 3); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("GetFrom = %v, want ErrBlobNotFound", err)
	}

	if _, err := s.GetSize(ctx, sha); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("GetSize = %v, want ErrBlobNotFound", err)
	}

	if md, err := s.GetMetadata(ctx, sha); err != nil || md != nil {
		t.Errorf("GetMetadata of a missing blob = %v, %v; want nil, nil", md, err)
	}

	if ok, err := s.Exists(ctx, sha); err != nil || ok {
		t.Errorf("Exists = %v, %v", ok, err)
	}

	if key, ok, err := s.ExistsBySHA256(ctx, sha); err != nil || ok || key != "" {
		t.Errorf("ExistsBySHA256 = %q, %v, %v", key, ok, err)
	}

	if err := s.Delete(ctx, sha); err != nil {
		t.Errorf("Delete of a missing blob = %v", err)
	}
}

func TestS3BlobRejectsBadInput(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	data, sha := randomBlob(t, 64)

	if _, err := s.Put(ctx, bytes.NewReader(data), nil); !errors.Is(err, ErrS3PutRequiresSHA256) {
		t.Errorf("Put without sha256 = %v, want ErrS3PutRequiresSHA256", err)
	}

	if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": "../x"}); !errors.Is(err, ErrInvalidBlobID) {
		t.Errorf("Put with a malformed digest = %v, want ErrInvalidBlobID", err)
	}

	_, other := randomBlob(t, 64)

	if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": other}); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("Put with the digest of other bytes = %v, want ErrDigestMismatch", err)
	}

	if ok, _ := s.Exists(ctx, other); ok {
		t.Error("a rejected Put became visible")
	}

	if ok, _ := s.Exists(ctx, sha); ok {
		t.Error("a rejected Put stored the bytes under their own digest")
	}

	for _, id := range []string{"", "short", "../../x", "ABCDEF" + sha[6:]} {
		if _, err := s.Get(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("Get(%q) = %v", id, err)
		}

		if _, err := s.GetFrom(ctx, id, 1); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("GetFrom(%q) = %v", id, err)
		}

		if _, err := s.GetSize(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("GetSize(%q) = %v", id, err)
		}

		if _, err := s.GetMetadata(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("GetMetadata(%q) = %v", id, err)
		}

		if err := s.Delete(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("Delete(%q) = %v", id, err)
		}

		if _, err := s.Exists(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
			t.Errorf("Exists(%q) = %v", id, err)
		}
	}
}

func TestS3BlobGetFromOffsets(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	data, sha := randomBlob(t, 1000)

	t.Cleanup(func() { _ = s.Delete(ctx, sha) })

	if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	for _, off := range []int64{0, 1, 500, 999} {
		rc, err := s.GetFrom(ctx, sha, off)
		if err != nil {
			t.Fatalf("GetFrom(%d): %v", off, err)
		}

		if got := readAll(t, rc); !bytes.Equal(got, data[off:]) {
			t.Errorf("GetFrom(%d) returned %d bytes, want %d", off, len(got), len(data)-int(off))
		}
	}

	if _, err := s.GetFrom(ctx, sha, 5000); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Errorf("GetFrom past the end = %v, want ErrOffsetOutOfRange", err)
	}

	if _, err := s.GetFrom(ctx, sha, -1); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Errorf("GetFrom(-1) = %v, want ErrOffsetOutOfRange", err)
	}
}

func TestS3BlobPutLargerThanOnePart(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	data, sha := randomBlob(t, int(s3PartSize)+1234)

	t.Cleanup(func() { _ = s.Delete(ctx, sha) })

	if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	if n, err := s.GetSize(ctx, sha); err != nil || n != int64(len(data)) {
		t.Fatalf("GetSize = %d, %v", n, err)
	}

	md, err := s.GetMetadata(ctx, sha)
	if err != nil || md["sha256"] != sha {
		t.Fatalf("GetMetadata = %v, %v", md, err)
	}
}

func TestS3BlobListPagesThroughStoredBlobs(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()

	want := map[string]bool{}

	for range 3 {
		data, sha := randomBlob(t, 32)
		want[sha] = true

		t.Cleanup(func() { _ = s.Delete(ctx, sha) })

		if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
			t.Fatal(err)
		}
	}

	seen, token := map[string]bool{}, ""

	for {
		ids, next, err := s.List(ctx, token, 2)
		if err != nil {
			t.Fatal(err)
		}

		for _, id := range ids {
			seen[id] = true
		}

		if next == "" {
			break
		}

		token = next
	}

	for sha := range want {
		if !seen[sha] {
			t.Errorf("List never returned %s", sha)
		}
	}
}

func TestS3PresignURLServesTheBlob(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	data, sha := randomBlob(t, 128)

	t.Cleanup(func() { _ = s.Delete(ctx, sha) })

	if _, err := s.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	u, err := s.PresignURL(ctx, sha, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := httpGet(ctx, u)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	if got, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		t.Fatalf("presigned GET = %d, %d bytes", resp.StatusCode, len(got))
	}

	if _, err := s.PresignURL(ctx, "bad", time.Minute); !errors.Is(err, ErrInvalidBlobID) {
		t.Fatalf("PresignURL with a bad id = %v", err)
	}
}

func TestS3HealthCheck(t *testing.T) {
	s := integrationS3(t)

	if err := s.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck of the test bucket: %v", err)
	}

	missing := *s
	missing.bucket = "openartifact-no-such-bucket-" + uuid.NewString()

	if err := missing.HealthCheck(context.Background()); err == nil {
		t.Fatal("HealthCheck of a missing bucket succeeded")
	}
}

// Promoting a staging object that is gone or no longer the verified one
// fails with the staging sentinels and publishes nothing.
func TestS3PromoteStagingFailuresPublishNothing(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	id := uuid.NewString()
	data, sha := randomBlob(t, 256)

	t.Cleanup(func() {
		_ = s.DeleteStaging(ctx, id)
		_ = s.Delete(ctx, sha)
	})

	if err := s.PromoteStaging(ctx, id, sha, `"v"`, nil); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("promote of a missing staging object = %v, want ErrStagingNotFound", err)
	}

	put, err := s.PresignStagingPut(ctx, id, int64(len(data)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	putPresigned(t, put, data)

	obj, err := s.StatStaging(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.PromoteStaging(ctx, id, sha, `"not-the-etag"`, nil); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("promote with a stale version = %v, want ErrStagingChanged", err)
	}

	if ok, _ := s.Exists(ctx, sha); ok {
		t.Fatal("a failed promote left a blob behind")
	}

	if _, err := s.OpenStaging(ctx, id, `"not-the-etag"`); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("OpenStaging with a stale version = %v, want ErrStagingChanged", err)
	}

	if err := s.PromoteStaging(ctx, id, sha, obj.Version, map[string]string{"format": "generic"}); err != nil {
		t.Fatalf("promote with the right version: %v", err)
	}

	md, err := s.GetMetadata(ctx, sha)
	if err != nil || md["sha256"] != sha || md["format"] != "generic" {
		t.Fatalf("promoted metadata = %v, %v", md, err)
	}
}

// discardUnverifiedCopy removes whatever was copied and always reports the
// staging object as changed, also when the copy is already gone.
func TestS3DiscardUnverifiedCopy(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	data, sha := randomBlob(t, 64)
	key := objectKey(sha)

	t.Cleanup(func() { _ = s.Delete(ctx, sha) })

	if _, err := s.client.PutObject(
		ctx,
		&s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data)},
	); err != nil {
		t.Fatal(err)
	}

	if err := s.discardUnverifiedCopy(ctx, key); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("discardUnverifiedCopy = %v, want ErrStagingChanged", err)
	}

	if ok, _ := s.Exists(ctx, sha); ok {
		t.Fatal("the unverified copy was not removed")
	}

	if err := s.discardUnverifiedCopy(ctx, key); !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("discardUnverifiedCopy of a missing object = %v, want ErrStagingChanged", err)
	}
}

// An abandoned multipart upload is found by the sweep, can be aborted by key
// or id, and aborting one that no longer exists is not an error.
func TestS3MultipartAbort(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	id := uuid.NewString()

	handle, err := s.InitiateMultipart(ctx, id, 2*MinPartSize, MinPartSize)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.DeleteStaging(ctx, id) })

	key := stagingPrefix + id

	// Only uploads started before the cutoff are reported.
	if ids, err := s.stagingMultipartIDs(ctx, time.Now().Add(-time.Hour)); err != nil || slices.Contains(ids, id) {
		t.Fatalf("stagingMultipartIDs(past) = %v, %v; a fresh upload must not be reported", ids, err)
	}

	if ids, err := s.stagingMultipartIDs(ctx, time.Now().Add(time.Hour)); err != nil || !slices.Contains(ids, id) {
		t.Fatalf("stagingMultipartIDs(future) = %v, %v; want the open upload", ids, err)
	}

	if err := s.abortStagingMultipart(ctx, key); err != nil {
		t.Fatal(err)
	}

	if ids, _ := s.stagingMultipartIDs(ctx, time.Now().Add(time.Hour)); slices.Contains(ids, id) {
		t.Fatal("abortStagingMultipart left the upload open")
	}

	if err := s.abortMultipart(ctx, key, handle); err != nil {
		t.Fatalf("aborting an already aborted upload = %v, want nil", err)
	}

	if err := s.abortStagingMultipart(ctx, key); err != nil {
		t.Fatalf("abortStagingMultipart with nothing open = %v", err)
	}

	// An upload for another key sharing the prefix is left alone.
	other := uuid.NewString()
	if _, err := s.InitiateMultipart(ctx, other, 2*MinPartSize, MinPartSize); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.DeleteStaging(ctx, other) })

	if err := s.abortStagingMultipart(ctx, key); err != nil {
		t.Fatal(err)
	}

	if ids, _ := s.stagingMultipartIDs(ctx, time.Now().Add(time.Hour)); !slices.Contains(ids, other) {
		t.Fatal("abortStagingMultipart aborted an unrelated upload")
	}
}

func TestS3MultipartRejectsBadStagingIDs(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()

	if _, err := s.InitiateMultipart(ctx, "not-a-uuid", MinPartSize, MinPartSize); err == nil {
		t.Error("InitiateMultipart accepted a non-UUID id")
	}

	if _, err := s.PresignStagingPut(ctx, "../x", 1, time.Minute); err == nil {
		t.Error("PresignStagingPut accepted a non-UUID id")
	}

	if err := s.DeleteStaging(ctx, "../x"); err == nil {
		t.Error("DeleteStaging accepted a non-UUID id")
	}

	if _, err := s.StatStaging(ctx, "../x"); err == nil {
		t.Error("StatStaging accepted a non-UUID id")
	}
}

func httpGet(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}

	return http.DefaultClient.Do(req)
}
