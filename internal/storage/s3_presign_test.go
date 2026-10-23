// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const presignTestSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newPresignTestStore(t *testing.T, cfg StorageConfig) *S3BlobStore {
	t.Helper()

	cfg.Bucket, cfg.Region, cfg.AccessKey, cfg.SecretKey = "artifacts", "us-east-1", "AKIATEST", "secret"
	cfg.ForcePathStyle = true

	s, err := NewS3BlobStore(cfg)
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}

	return s
}

func parseSigned(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}

	if u.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("URL is not signed: %s", raw)
	}

	return u
}

func TestPresignGet_SignsForPublicEndpoint(t *testing.T) {
	s := newPresignTestStore(t, StorageConfig{
		Endpoint:        "http://otel-minio.observability.svc:9000",
		PresignEndpoint: "https://s3.example.com",
		PresignTTL:      7 * time.Minute,
	})

	raw, err := s.PresignGet(context.Background(), presignTestSHA, 0, PresignGetOptions{
		ContentDisposition: `attachment; filename="app-1.0.jar"`,
		ContentType:        "application/java-archive",
	})
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	u := parseSigned(t, raw)
	if u.Scheme != "https" || u.Host != "s3.example.com" {
		t.Errorf("signed for %s://%s, want https://s3.example.com", u.Scheme, u.Host)
	}

	if want := "/artifacts/blobs/01/23/" + presignTestSHA; u.Path != want {
		t.Errorf("path = %q, want %q", u.Path, want)
	}

	q := u.Query()
	if q.Get("X-Amz-Expires") != "420" {
		t.Errorf("X-Amz-Expires = %q, want 420 (the configured 7m TTL)", q.Get("X-Amz-Expires"))
	}

	if q.Get("response-content-disposition") != `attachment; filename="app-1.0.jar"` {
		t.Errorf("response-content-disposition = %q", q.Get("response-content-disposition"))
	}

	if q.Get("response-content-type") != "application/java-archive" {
		t.Errorf("response-content-type = %q", q.Get("response-content-type"))
	}
}

func TestPresignGet_DefaultsToInternalEndpointAndClampsTTL(t *testing.T) {
	s := newPresignTestStore(t, StorageConfig{Endpoint: "http://minio.internal:9000"})

	raw, err := s.PresignGet(context.Background(), presignTestSHA, 48*time.Hour, PresignGetOptions{})
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	u := parseSigned(t, raw)
	if u.Host != "minio.internal:9000" {
		t.Errorf("host = %q, want the internal endpoint when presignEndpoint is unset", u.Host)
	}

	if got := u.Query().Get("X-Amz-Expires"); got != "3600" {
		t.Errorf("X-Amz-Expires = %q, want 3600 (clamped to 1h)", got)
	}

	if u.Query().Has("response-content-disposition") || u.Query().Has("response-content-type") {
		t.Errorf("unrequested response overrides were signed: %s", u.RawQuery)
	}

	// The legacy entry point keeps working and uses the configured default TTL.
	legacy, err := s.PresignURL(context.Background(), presignTestSHA, 0)
	if err != nil {
		t.Fatalf("PresignURL: %v", err)
	}

	if got := parseSigned(t, legacy).Query().Get("X-Amz-Expires"); got != "300" {
		t.Errorf("legacy X-Amz-Expires = %q, want 300", got)
	}
}

func TestPresignStagingPut(t *testing.T) {
	s := newPresignTestStore(t, StorageConfig{
		Endpoint:        "http://minio.internal:9000",
		PresignEndpoint: "https://s3.example.com",
	})

	id := uuid.NewString()

	put, err := s.PresignStagingPut(context.Background(), id, 1234, 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignStagingPut: %v", err)
	}

	u := parseSigned(t, put.URL)
	if u.Host != "s3.example.com" || u.Path != "/artifacts/staging/"+id {
		t.Errorf("URL = %s%s, want the public host and staging/%s", u.Host, u.Path, id)
	}

	if got := u.Query().Get("X-Amz-Expires"); got != "1800" {
		t.Errorf("X-Amz-Expires = %q, want 1800", got)
	}

	if !strings.Contains(strings.ToLower(u.Query().Get("X-Amz-SignedHeaders")), "content-length") {
		t.Errorf("content-length is not signed (SignedHeaders = %q): the object store would accept any size", u.Query().Get("X-Amz-SignedHeaders"))
	}

	for name := range put.Headers {
		if strings.EqualFold(name, "host") || strings.EqualFold(name, "content-length") {
			t.Errorf("header %s must not be handed to the client", name)
		}
	}

	if !put.Expires.After(time.Now().Add(29 * time.Minute)) {
		t.Errorf("Expires = %v", put.Expires)
	}
}

func TestPresignStagingPut_RejectsNonUUID(t *testing.T) {
	s := newPresignTestStore(t, StorageConfig{Endpoint: "http://minio.internal:9000"})

	for _, id := range []string{"", "../blobs/aa", "not-a-uuid"} {
		if _, err := s.PresignStagingPut(context.Background(), id, 1, time.Minute); err == nil {
			t.Errorf("id %q accepted", id)
		}

		if err := s.DeleteStaging(context.Background(), id); err == nil {
			t.Errorf("DeleteStaging accepted id %q", id)
		}
	}
}

func TestClampStagingTTL(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		0:                time.Minute,
		30 * time.Minute: 30 * time.Minute,
		48 * time.Hour:   12 * time.Hour,
	} {
		if got := ClampStagingTTL(in); got != want {
			t.Errorf("ClampStagingTTL(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestFilesystemStoreCannotPresign(t *testing.T) {
	fs := newTestFilesystemStore(t)

	if _, err := fs.PresignURL(context.Background(), presignTestSHA, time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PresignURL error = %v, want ErrPresignNotSupported", err)
	}

	// Without a public URL there is no relay, so staging is unsupported.
	if fs.StagingSupported() {
		t.Error("filesystem store without PublicURL must not support staging")
	}
}
