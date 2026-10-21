// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeSigner makes a local store look like an S3 store that signs URLs.
type fakeSigner struct{ storage.BlobStore }

func (fakeSigner) PresignGet(_ context.Context, sha string, _ time.Duration, _ storage.PresignGetOptions) (string, error) {
	return "https://s3.example/blobs/" + sha + "?X-Amz-Signature=sig", nil
}

// presignedFixture is a maven proxy repository whose cache redirects.
func presignedFixture(t *testing.T, body string) *fixture {
	t.Helper()

	f := newFixture(t, okBody(body))

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	f.svc.blobStore = presign.Wrap(fakeSigner{f.svc.blobStore}, f.db, presign.Settings{Downloads: true, TTL: time.Minute}, nil, log)

	return f
}

func TestServe_CachedBlobRedirectsToObjectStore(t *testing.T) {
	f := presignedFixture(t, "jar-bytes")
	const key = "junit/junit/4.13.2/junit-4.13.2.jar"

	// Fill the cache (the first request is a miss and also redirects: the
	// blob is already in the store once the fetch finished).
	for i, want := range []string{"miss", "hit"} {
		w := f.do(http.MethodGet, key)
		if w.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s: status = %d, want 307; body %q", want, w.Code, w.Body.String())
		}

		if loc := w.Header().Get("Location"); loc != "https://s3.example/blobs/"+sum("jar-bytes")+"?X-Amz-Signature=sig" {
			t.Errorf("%s: Location = %q", want, loc)
		}

		if i == 1 && f.hits.Load() != 1 {
			t.Errorf("upstream requests = %d, want 1: the hit must not refetch", f.hits.Load())
		}
	}

	if w := f.do(http.MethodHead, key); w.Code == http.StatusTemporaryRedirect {
		t.Error("HEAD was redirected")
	}
}

func TestServe_MetadataIsNeverRedirected(t *testing.T) {
	f := presignedFixture(t, "<metadata/>")

	for _, key := range []string{
		"junit/junit/maven-metadata.xml",
		"junit/junit/4.13.2/junit-4.13.2.jar.sha1",
		"junit/junit/4.13.2/junit-4.13.2.jar.asc",
	} {
		for range 2 {
			w := f.do(http.MethodGet, key)
			if w.Code != http.StatusOK || w.Header().Get("Location") != "" || w.Body.String() != "<metadata/>" {
				t.Fatalf("%s: status %d, Location %q, body %q; want the streamed file", key, w.Code, w.Header().Get("Location"), w.Body.String())
			}
		}
	}
}

func TestServe_CachedBlobMissingFromStoreFallsBackToRefetch(t *testing.T) {
	f := presignedFixture(t, "jar-bytes")
	const key = "junit/junit/4.13.2/junit-4.13.2.jar"

	if w := f.do(http.MethodGet, key); w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("priming: status = %d", w.Code)
	}

	// The cache row outlives its blob: never hand out a URL to a missing object.
	if err := f.svc.blobStore.Delete(context.Background(), sum("jar-bytes")); err != nil {
		t.Fatalf("delete blob: %v", err)
	}

	w := f.do(http.MethodGet, key)
	if w.Code != http.StatusOK || w.Body.String() != "jar-bytes" || strings.Contains(w.Header().Get("Location"), "s3.example") {
		t.Fatalf("status %d body %q Location %q, want the refetched blob streamed", w.Code, w.Body.String(), w.Header().Get("Location"))
	}

	if f.hits.Load() != 2 {
		t.Errorf("upstream requests = %d, want 2 (refetch)", f.hits.Load())
	}
}
