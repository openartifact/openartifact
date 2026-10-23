// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
)

const (
	gcsTestBucket     = "bkt"
	gcsTestSrcKey     = "staging/0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	gcsTestDstKey     = "blobs/ab/cd/abcd"
	gcsTestETag       = `"etag-1"`
	gcsTestGeneration = "1361471441094000"
)

// fakeGCS answers the two calls a compose promotion makes.
type fakeGCS struct {
	mu       sync.Mutex
	etag     string
	compose  *gcsComposeRequest
	composed string
	meta     http.Header
	signed   bool
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.signed = strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") && strings.Contains(r.Header.Get("Authorization"), "Credential=HMACKEY/")

	switch {
	case r.Method == http.MethodHead && r.URL.Path == "/"+gcsTestBucket+"/"+gcsTestSrcKey:
		if m := r.Header.Get("If-Match"); m != "" && m != f.etag {
			w.WriteHeader(http.StatusPreconditionFailed)

			return
		}

		w.Header().Set("ETag", f.etag)
		w.Header().Set(gcsGenerationHeader, gcsTestGeneration)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPut && r.URL.RawQuery == "compose=":
		body, _ := io.ReadAll(r.Body)

		var req gcsComposeRequest
		if err := xml.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		f.compose, f.composed, f.meta = &req, r.URL.Path, r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newFakeGCSStore(t *testing.T, f *fakeGCS) *S3BlobStore {
	t.Helper()

	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	c := newGCSComposer(gcsTestBucket, credentials.NewStaticCredentialsProvider("HMACKEY", "secret", ""))
	c.base = srv.URL

	return &S3BlobStore{bucket: gcsTestBucket, gcs: true, gcsCompose: c}
}

// A GCS promotion is a compose of the staged object pinned to the generation
// whose ETag was verified, never a byte copy.
func TestGCSPromoteComposesPinnedGeneration(t *testing.T) {
	t.Parallel()

	f := &fakeGCS{etag: gcsTestETag}
	s := newFakeGCSStore(t, f)

	err := s.copyStaging(context.Background(), gcsTestSrcKey, gcsTestDstKey, gcsTestETag, map[string]string{"Sha256": "abcd"})
	if err != nil {
		t.Fatal(err)
	}

	if !f.signed {
		t.Error("request not signed with the HMAC key")
	}

	if f.composed != "/"+gcsTestBucket+"/"+gcsTestDstKey {
		t.Errorf("composed %q", f.composed)
	}

	if f.compose == nil || len(f.compose.Component) != 1 ||
		f.compose.Component[0].Name != gcsTestSrcKey || f.compose.Component[0].IfGenerationMatch != gcsTestGeneration {
		t.Errorf("compose request = %+v", f.compose)
	}

	if got := f.meta.Get("X-Goog-Meta-Sha256"); got != "abcd" {
		t.Errorf("x-goog-meta-sha256 = %q", got)
	}
}

func TestGCSPromoteRefusesChangedAndMissingSource(t *testing.T) {
	t.Parallel()

	f := &fakeGCS{etag: `"replaced"`}
	s := newFakeGCSStore(t, f)

	err := s.copyStaging(context.Background(), gcsTestSrcKey, gcsTestDstKey, gcsTestETag, nil)
	if !errors.Is(err, ErrStagingChanged) {
		t.Fatalf("changed source err = %v", err)
	}

	if f.compose != nil {
		t.Fatal("composed although the staged object changed")
	}

	err = s.copyStaging(context.Background(), "staging/other", gcsTestDstKey, gcsTestETag, nil)
	if !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("missing source err = %v", err)
	}
}
