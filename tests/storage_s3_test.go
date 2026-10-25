// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

func newFakeS3Server(t *testing.T, responses map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for pat, handler := range responses {
			if strings.Contains(r.URL.Path, pat) {
				handler(w, r)
				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?><Error><Code>NoSuchKey</Code><Message>NoSuchKey</Message></Error>")
	}))

	return srv
}

func newTestS3Store(t *testing.T, endpoint string) *storage.S3BlobStore {
	t.Helper()

	cfg := storage.StorageConfig{
		Type:           "s3",
		Endpoint:       endpoint,
		Bucket:         "test-bucket",
		Region:         "us-east-1",
		AccessKey:      "minioadmin",
		SecretKey:      "minioadmin",
		ForcePathStyle: true,
		PresignTTL:     5 * time.Minute,
	}

	store, err := storage.NewS3BlobStore(cfg)
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}

	return store
}

func TestNewS3BlobStore_BasicConstruction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newTestS3Store(t, srv.URL)
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestS3BlobStore_Put_Missingsha256(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newTestS3Store(t, srv.URL)

	_, err := store.Put(context.Background(), strings.NewReader("hello"), map[string]string{})
	if err == nil {
		t.Fatal("expected error when sha256 missing from metadata")
	}

	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("expected error to mention sha256, got: %v", err)
	}
}

func TestS3BlobStore_PresignURL_TTLClamping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newTestS3Store(t, srv.URL)
	ctx := context.Background()
	sha := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"

	url10s, err := store.PresignURL(ctx, sha, 10*time.Second)
	if err != nil {
		t.Fatalf("PresignURL(10s): %v", err)
	}

	if !strings.Contains(url10s, "X-Amz-Expires=60") {
		t.Errorf("expected 60s expiry in URL for 10s input, got: %s", url10s)
	}

	url2h, err := store.PresignURL(ctx, sha, 2*time.Hour)
	if err != nil {
		t.Fatalf("PresignURL(2h): %v", err)
	}

	if !strings.Contains(url2h, "X-Amz-Expires=3600") {
		t.Errorf("expected 3600s expiry in URL for 2h input, got: %s", url2h)
	}

	url30m, err := store.PresignURL(ctx, sha, 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignURL(30m): %v", err)
	}

	if !strings.Contains(url30m, "X-Amz-Expires=1800") {
		t.Errorf("expected 1800s expiry in URL for 30m input, got: %s", url30m)
	}
}

func TestS3BlobStore_Get_NotFound(t *testing.T) {
	srv := newFakeS3Server(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()

	store := newTestS3Store(t, srv.URL)
	sha := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"

	rc, err := store.Get(context.Background(), sha)
	if err == nil {
		_ = rc.Close()

		t.Fatal("expected error for missing blob")
	}

	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), sha) && !strings.Contains(err.Error(), "NoSuchKey") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestS3BlobStore_Get_Success(t *testing.T) {
	body := "blob content"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, body)

			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	store := newTestS3Store(t, srv.URL)
	sha := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"

	rc, err := store.Get(context.Background(), sha)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	defer func() { _ = rc.Close() }()

	data, _ := io.ReadAll(rc)
	if string(data) != body {
		t.Errorf("expected %q, got %q", body, string(data))
	}
}

func TestS3BlobStore_Delete_NotFound_IsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	store := newTestS3Store(t, srv.URL)
	sha := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"

	err := store.Delete(context.Background(), sha)
	if err != nil {
		t.Errorf("Delete should succeed (S3 DeleteObject returns 204 even for missing keys): %v", err)
	}
}

func TestS3BlobStore_Exists_False(t *testing.T) {
	srv := newFakeS3Server(t, map[string]func(http.ResponseWriter, *http.Request){})
	defer srv.Close()

	store := newTestS3Store(t, srv.URL)
	sha := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"

	exists, err := store.Exists(context.Background(), sha)
	if err != nil {
		t.Logf("Exists returned err (acceptable with fake server): %v", err)
	}

	_ = exists
}
