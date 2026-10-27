// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/storage"
)

func newPresignS3Store(t *testing.T) *storage.S3BlobStore {
	t.Helper()

	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		skipOrFailMissingEnv(t, "TEST_S3_ENDPOINT")
	}

	store, err := storage.NewS3BlobStore(storage.StorageConfig{
		Type:           "s3",
		Endpoint:       endpoint,
		Bucket:         getEnvOrDefault("TEST_S3_BUCKET", "openartifact-test"),
		Region:         getEnvOrDefault("TEST_S3_REGION", "us-east-1"),
		AccessKey:      getEnvOrDefault("TEST_S3_ACCESS_KEY", "minioadmin"),
		SecretKey:      getEnvOrDefault("TEST_S3_SECRET_KEY", "minioadmin"),
		ForcePathStyle: true,
		PresignTTL:     time.Minute,
	})
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}

	return store
}

func do(t *testing.T, req *http.Request) (int, http.Header, []byte) {
	t.Helper()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, resp.Header, b
}

// TestS3Integration_PresignedUploadPromotesToBlob drives the staging flow
// against a real S3 API: signed PUT, size check, promotion to the
// content-addressed key, signed GET with response header overrides.
func assertStagedContent(t *testing.T, store *storage.S3BlobStore, id, version string, want []byte) {
	t.Helper()

	rc, err := store.OpenStaging(context.Background(), id, version)
	if err != nil {
		t.Fatalf("OpenStaging: %v", err)
	}

	read, _ := io.ReadAll(rc)
	_ = rc.Close()

	if !bytes.Equal(read, want) {
		t.Fatal("staging object differs from the uploaded bytes")
	}
}

func TestS3Integration_PresignedUploadPromotesToBlob(t *testing.T) {
	store := newPresignS3Store(t)
	ctx := context.Background()

	content := bytes.Repeat([]byte("presigned-upload "), 4096)
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	id := uuid.NewString()

	t.Cleanup(func() {
		_ = store.DeleteStaging(ctx, id)
		_ = store.Delete(ctx, sha)
	})

	put, err := store.PresignStagingPut(ctx, id, int64(len(content)), time.Minute)
	if err != nil {
		t.Fatalf("PresignStagingPut: %v", err)
	}

	newPut := func(body []byte) *http.Request {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, put.URL, bytes.NewReader(body))
		for k, v := range put.Headers {
			req.Header.Set(k, v)
		}

		return req
	}

	// The signature covers Content-Length: a different size is refused by S3.
	if status, _, _ := do(t, newPut(content[:100])); status/100 == 2 {
		t.Fatalf("a PUT of the wrong size was accepted (HTTP %d)", status)
	}

	if _, err := store.StatStaging(ctx, id); !errors.Is(err, storage.ErrStagingNotFound) {
		t.Fatalf("StatStaging before upload: %v, want ErrStagingNotFound", err)
	}

	if status, _, body := do(t, newPut(content)); status/100 != 2 {
		t.Fatalf("PUT failed: HTTP %d %s", status, body)
	}

	staged, err := store.StatStaging(ctx, id)
	if err != nil || staged.Size != int64(len(content)) || staged.Version == "" {
		t.Fatalf("StatStaging = %+v, %v", staged, err)
	}

	assertStagedContent(t, store, id, staged.Version, content)

	if err := store.PromoteStaging(ctx, id, sha, staged.Version, map[string]string{"filename": "app.bin"}); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}

	if ok, err := store.Exists(ctx, sha); err != nil || !ok {
		t.Fatalf("blob missing after promotion: %v, %v", ok, err)
	}

	if err := store.DeleteStaging(ctx, id); err != nil {
		t.Fatalf("DeleteStaging: %v", err)
	}

	if _, err := store.StatStaging(ctx, id); !errors.Is(err, storage.ErrStagingNotFound) {
		t.Fatalf("staging object survived deletion: %v", err)
	}

	url, err := store.PresignGet(
		ctx,
		sha,
		time.Minute,
		storage.PresignGetOptions{ContentDisposition: `attachment; filename="app.bin"`, ContentType: "application/x-test"},
	)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	status, hdr, body := do(t, req)
	if status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("signed GET: HTTP %d, %d bytes", status, len(body))
	}

	if got := hdr.Get("Content-Disposition"); !strings.Contains(got, "app.bin") {
		t.Errorf("Content-Disposition = %q, want the response-content-disposition override", got)
	}

	if got := hdr.Get("Content-Type"); got != "application/x-test" {
		t.Errorf("Content-Type = %q, want the override", got)
	}
}

func TestS3Integration_PresignedGetOfMissingBlobIsRejectedByStore(t *testing.T) {
	store := newPresignS3Store(t)

	url, err := store.PresignGet(context.Background(), strings.Repeat("cd", 32), time.Minute, storage.PresignGetOptions{})
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if status, _, _ := do(t, req); status != http.StatusNotFound {
		t.Errorf("HTTP %d, want 404 for an object that does not exist", status)
	}
}

// TestS3Integration_StagingObjectOverwrittenAfterVerification is the
// poisoning scenario: the client's presigned PUT URL stays valid, so it can
// overwrite the staging object after the server verified it. The conditional
// read and copy must refuse the new bytes and leave no blob behind.
func TestS3Integration_StagingObjectOverwrittenAfterVerification(t *testing.T) {
	store := newPresignS3Store(t)
	ctx := context.Background()

	good := bytes.Repeat([]byte("verified "), 1024)
	evil := bytes.Repeat([]byte("EVIL!!!!! "), 900)
	sum := sha256.Sum256(good)
	sha := hex.EncodeToString(sum[:])
	id := uuid.NewString()

	t.Cleanup(func() {
		_ = store.DeleteStaging(ctx, id)
		_ = store.Delete(ctx, sha)
	})

	put := func(body []byte) {
		t.Helper()

		p, err := store.PresignStagingPut(ctx, id, int64(len(body)), time.Minute)
		if err != nil {
			t.Fatalf("PresignStagingPut: %v", err)
		}

		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, p.URL, bytes.NewReader(body))
		for k, v := range p.Headers {
			req.Header.Set(k, v)
		}

		if status, _, b := do(t, req); status/100 != 2 {
			t.Fatalf("PUT failed: HTTP %d %s", status, b)
		}
	}

	put(good)

	verified, err := store.StatStaging(ctx, id)
	if err != nil {
		t.Fatalf("StatStaging: %v", err)
	}

	put(evil) // after verification, before promotion

	if _, err := store.OpenStaging(ctx, id, verified.Version); !errors.Is(err, storage.ErrStagingChanged) {
		t.Errorf("OpenStaging of an overwritten object: %v, want ErrStagingChanged", err)
	}

	if err := store.PromoteStaging(ctx, id, sha, verified.Version, nil); !errors.Is(err, storage.ErrStagingChanged) {
		t.Fatalf("PromoteStaging of an overwritten object: %v, want ErrStagingChanged", err)
	}

	if ok, err := store.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("a blob exists under the declared hash after a rejected promotion: %v, %v", ok, err)
	}

	// The staging prefix walk reports the object once it is old enough.
	var seen []string

	err = store.WalkStaging(ctx, time.Now().Add(time.Hour), func(ids []string) error {
		seen = append(seen, ids...)
		return nil
	})
	if err != nil || !slices.Contains(seen, id) {
		t.Errorf("WalkStaging = %v, %v; want it to list %s", seen, err, id)
	}

	seen = nil
	_ = store.WalkStaging(ctx, time.Now().Add(-time.Hour), func(ids []string) error {
		seen = append(seen, ids...)
		return nil
	})

	if slices.Contains(seen, id) {
		t.Error("WalkStaging listed an object younger than the cutoff")
	}
}
