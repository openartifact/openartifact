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
	"os"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

func TestS3Integration_PutGetDelete(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		skipOrFailMissingEnv(t, "TEST_S3_ENDPOINT")
	}

	bucket := os.Getenv("TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "openartifact-test"
	}

	cfg := storage.StorageConfig{
		Type:           "s3",
		Endpoint:       endpoint,
		Bucket:         bucket,
		Region:         getEnvOrDefault("TEST_S3_REGION", "us-east-1"),
		AccessKey:      getEnvOrDefault("TEST_S3_ACCESS_KEY", "minioadmin"),
		SecretKey:      getEnvOrDefault("TEST_S3_SECRET_KEY", "minioadmin"),
		ForcePathStyle: true,
		PresignTTL:     5 * time.Minute,
	}

	store, err := storage.NewS3BlobStore(cfg)
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}

	ctx := context.Background()

	payload := []byte("integration test data")
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	meta := map[string]string{"sha256": sha, "filename": "test.bin"}

	id, err := store.Put(ctx, bytes.NewReader(payload), meta)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if id != sha {
		t.Errorf("Put returned %q, want %q", id, sha)
	}

	exists, err := store.Exists(ctx, sha)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Fatal("expected blob to exist after Put")
	}

	rc, err := store.Get(ctx, sha)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	_ = rc.Close()

	if err := store.Delete(ctx, sha); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := store.Delete(ctx, sha); err != nil {
		t.Errorf("Delete (idempotent): %v", err)
	}
}

// Put must refuse bytes that do not hash to the claimed sha256 and leave no
// object behind, and must not replace an object that is already stored.
func TestS3Integration_PutVerifiesDigest(t *testing.T) {
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
		PresignTTL:     5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}

	ctx := context.Background()
	good := []byte("verified content " + t.Name() + time.Now().String())
	sum := sha256.Sum256(good)
	sha := hex.EncodeToString(sum[:])

	t.Cleanup(func() { _ = store.Delete(context.Background(), sha) })

	if _, err := store.Put(ctx, bytes.NewReader([]byte("other bytes")), map[string]string{"sha256": sha}); !errors.Is(err, storage.ErrDigestMismatch) {
		t.Fatalf("Put of mismatching bytes = %v, want ErrDigestMismatch", err)
	}

	if ok, err := store.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("object after rejected Put: exists=%v err=%v, want none", ok, err)
	}

	if _, err := store.Put(ctx, bytes.NewReader(good), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("Put of matching bytes: %v", err)
	}

	// A second Put of different bytes under the same key is rejected, and the
	// verified object is untouched.
	if _, err := store.Put(ctx, bytes.NewReader([]byte("poison")), map[string]string{"sha256": sha}); !errors.Is(err, storage.ErrDigestMismatch) {
		t.Fatalf("Put over existing = %v, want ErrDigestMismatch", err)
	}

	rc, err := store.Get(ctx, sha)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, good) {
		t.Fatalf("stored bytes = %q, want %q", got, good)
	}
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
