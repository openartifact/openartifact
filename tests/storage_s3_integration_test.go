// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"os"
	"strings"
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

	sha := strings.Repeat("ab", 32)
	meta := map[string]string{"sha256": sha, "filename": "test.bin"}

	id, err := store.Put(ctx, bytes.NewReader([]byte("integration test data")), meta)
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

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
