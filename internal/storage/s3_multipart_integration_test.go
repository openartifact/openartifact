// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func integrationS3(t *testing.T) *S3BlobStore {
	t.Helper()

	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT is not set")
	}

	getenv := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}

		return def
	}

	s, err := NewS3BlobStore(StorageConfig{
		Type: "s3", Endpoint: endpoint, ForcePathStyle: true,
		Bucket:    getenv("TEST_S3_BUCKET", "openartifact-test"),
		Region:    getenv("TEST_S3_REGION", "us-east-1"),
		AccessKey: getenv("TEST_S3_ACCESS_KEY", "minioadmin"),
		SecretKey: getenv("TEST_S3_SECRET_KEY", "minioadmin"),
	})
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}

	return s
}

func putPresigned(t *testing.T, put PresignedPut, body []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, put.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	for k, v := range put.Headers {
		if http.CanonicalHeaderKey(k) != "Content-Length" {
			req.Header.Set(k, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT status %d: %s", resp.StatusCode, msg)
	}
}

// A multipart upload assembles into the staging object, which verifies and is
// promoted to its content-addressed key; the promote copy is forced through
// UploadPartCopy by lowering the single-copy threshold.
func TestS3MultipartUploadAssemblesAndPromotes(t *testing.T) {
	s := integrationS3(t)
	s.copyOverride = &copyLimit{single: MinPartSize, part: MinPartSize}

	ctx := context.Background()
	id := uuid.NewString()
	partSize := MinPartSize
	content := bytes.Repeat([]byte("0123456789abcdef"), int(2*partSize+12345)/16+1)[:2*partSize+12345]
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	handle, err := s.InitiateMultipart(ctx, id, int64(len(content)), partSize)
	if err != nil || handle == "" {
		t.Fatalf("InitiateMultipart = %q, %v", handle, err)
	}

	t.Cleanup(func() {
		_ = s.DeleteStaging(ctx, id)
		_ = s.Delete(ctx, digest)
	})

	upload := func(number int) {
		length := PartLength(int64(len(content)), partSize, number)
		put, err := s.PresignStagingPart(ctx, id, handle, number, length, time.Minute)
		if err != nil {
			t.Fatalf("PresignStagingPart(%d): %v", number, err)
		}

		off := int64(number-1) * partSize
		putPresigned(t, put, content[off:off+length])
	}

	upload(1)
	upload(3)

	if err := s.CompleteMultipart(ctx, id, handle, int64(len(content)), partSize); !errors.Is(err, ErrMultipartIncomplete) {
		t.Fatalf("CompleteMultipart with a missing part = %v, want ErrMultipartIncomplete", err)
	}

	upload(2)

	if err := s.CompleteMultipart(ctx, id, handle, int64(len(content)), partSize); err != nil {
		t.Fatalf("CompleteMultipart: %v", err)
	}

	// A retry after the assembly succeeds without touching the object.
	if err := s.CompleteMultipart(ctx, id, handle, int64(len(content)), partSize); err != nil {
		t.Fatalf("repeated CompleteMultipart: %v", err)
	}

	obj, err := s.StatStaging(ctx, id)
	if err != nil || obj.Size != int64(len(content)) {
		t.Fatalf("StatStaging = %+v, %v", obj, err)
	}

	rc, err := s.OpenStaging(ctx, id, obj.Version)
	if err != nil {
		t.Fatalf("OpenStaging: %v", err)
	}

	got, _ := io.ReadAll(rc)
	_ = rc.Close()

	if !bytes.Equal(got, content) {
		t.Fatal("assembled staging object differs from the uploaded parts")
	}

	if err := s.PromoteStaging(ctx, id, digest, obj.Version, map[string]string{"sha256": digest}); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}

	blob, err := s.Get(ctx, digest)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	got, _ = io.ReadAll(blob)
	_ = blob.Close()

	if !bytes.Equal(got, content) {
		t.Fatal("promoted blob differs from the upload")
	}
}

// Deleting the staging id aborts a multipart upload that was never completed,
// and the sweep reports an open one that is old enough.
func TestS3MultipartAbortedByDeleteStaging(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	id := uuid.NewString()

	if _, err := s.InitiateMultipart(ctx, id, 2*MinPartSize, MinPartSize); err != nil {
		t.Fatalf("InitiateMultipart: %v", err)
	}

	var seen bool

	if err := s.WalkStaging(ctx, time.Now().Add(time.Hour), func(ids []string) error {
		for _, got := range ids {
			seen = seen || got == id
		}

		return nil
	}); err != nil {
		t.Fatalf("WalkStaging: %v", err)
	}

	if !seen {
		t.Error("WalkStaging did not report the open multipart upload")
	}

	if err := s.DeleteStaging(ctx, id); err != nil {
		t.Fatalf("DeleteStaging: %v", err)
	}

	open, err := s.stagingMultipartIDs(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("stagingMultipartIDs: %v", err)
	}

	for _, got := range open {
		if got == id {
			t.Error("DeleteStaging left the multipart upload open")
		}
	}
}
