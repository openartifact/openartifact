// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

const (
	leakMarker = "UPSTREAM-SECRET-TRACE"
	validIndex = "apiVersion: v1\nentries: {}\ngenerated: 2024-01-01T00:00:00Z\n"
)

// flakyGetStore fails its first failures Gets, as a cache row whose blob was
// removed from the store underneath it.
type flakyGetStore struct {
	storage.BlobStore

	failures atomic.Int32
}

func (s *flakyGetStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if s.failures.Add(-1) >= 0 {
		return nil, errors.New("blob gone")
	}

	return s.BlobStore.Get(ctx, id)
}

// upstreamMode is one way an upstream answers a metadata request.
type upstreamMode struct {
	name string
	h    http.HandlerFunc
	// want is the status the client must see; 0 skips the check.
	want int
	// stale reports whether a cached copy past its TTL may stand in.
	stale bool
}

func upstreamFailures(t *testing.T) []upstreamMode {
	t.Helper()

	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })

	leaky := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(code)
			_, _ = io.WriteString(w, leakMarker)
		}
	}

	return []upstreamMode{
		{"500", leaky(http.StatusInternalServerError), http.StatusBadGateway, true},
		{"503", leaky(http.StatusServiceUnavailable), http.StatusBadGateway, true},
		{"401", leaky(http.StatusUnauthorized), http.StatusBadGateway, false},
		{"403", leaky(http.StatusForbidden), http.StatusBadGateway, false},
		{"418", leaky(http.StatusTeapot), http.StatusBadGateway, false},
		{"dropped connection", dropConnection, http.StatusBadGateway, true},
		{"truncated body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100000")
			_, _ = io.WriteString(w, "short")
		}, http.StatusBadGateway, true},
		{"redirect to the metadata address", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}, http.StatusBadGateway, true},
		{"timeout", func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-hang:
			case <-r.Context().Done():
			}
		}, http.StatusBadGateway, true},
	}
}

// modalUpstream answers with whichever handler is stored; primed by the first.
type modalUpstream struct{ mode atomic.Value }

func (m *modalUpstream) set(h http.HandlerFunc) { m.mode.Store(h) }

func (m *modalUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mode.Load().(http.HandlerFunc)(w, r)
}

// shortClient bounds every upstream call so hung-upstream cases end promptly.
func shortClient(f *fixture) {
	f.svc.client = &http.Client{Timeout: 200 * time.Millisecond, CheckRedirect: f.svc.client.CheckRedirect, Transport: f.svc.client.Transport}
}

// metadataCase is one document the proxy fetches from an upstream.
type metadataCase struct {
	name   string
	format string
	// prepare points f at the upstream; the valid handler must make get succeed.
	prepare func(f *fixture)
	valid   http.HandlerFunc
	get     string
	// key is the cache key the document is stored under.
	key string
	// notFound is the status for an upstream 404.
	notFound int
}

func metadataCases() []metadataCase {
	return []metadataCase{
		{
			name: "helm index", format: "helm",
			prepare: func(f *fixture) {
				f.repo.UpstreamURL = f.upstreamBase() + "/charts"
				f.svc.SetBaseURL(oaBase + "/")
			},
			valid: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/yaml")
				_, _ = io.WriteString(w, validIndex)
			},
			get: "index.yaml", key: helmIndexKey, notFound: http.StatusNotFound,
		},
		{
			name: "pypi project page", format: "pypi",
			prepare: func(f *fixture) { f.repo.UpstreamURL = f.upstreamBase() + "/simple/" },
			valid: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", htmlContent)
				_, _ = io.WriteString(w, `<html><body><a href="/files/`+wheelName+`#sha256=`+sum(wheelBody)+`">`+wheelName+`</a></body></html>`)
			},
			get: "simple/requests/", key: pypiProjectKey("requests"), notFound: http.StatusNotFound,
		},
		{
			name: "nuget service index", format: "nuget",
			prepare: func(f *fixture) {
				f.repo.UpstreamURL = f.upstreamBase() + "/v3/index.json"
				f.svc.SetBaseURL(oaBase + "/")
			},
			valid: func(w http.ResponseWriter, r *http.Request) {
				feed := &nugetFeed{base: "http://" + r.Host, cdn: "http://" + r.Host}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, feed.serviceIndex())
			},
			get: "v3/index.json", key: nugetIndexKey, notFound: http.StatusBadGateway,
		},
	}
}

func newModalFixture(t *testing.T, mc metadataCase) (*fixture, *modalUpstream) {
	t.Helper()

	up := &modalUpstream{}
	up.set(mc.valid)

	f := newFixture(t, up.ServeHTTP)
	f.repo.Format = mc.format
	f.repo.Name = mc.format + "-proxy"
	mc.prepare(f)

	return f, up
}

func TestUpstreamMetadata_FailuresMapToGatewayErrorsWithoutLeakingUpstreamText(t *testing.T) {
	for _, mc := range metadataCases() {
		for _, fail := range upstreamFailures(t) {
			t.Run(mc.name+"/"+fail.name, func(t *testing.T) {
				f, up := newModalFixture(t, mc)
				shortClient(f)
				up.set(fail.h)

				w := f.do(http.MethodGet, mc.get)
				if w.Code != fail.want {
					t.Errorf("status = %d, want %d", w.Code, fail.want)
				}

				if strings.Contains(w.Body.String(), leakMarker) || w.Header().Get("ETag") != "" {
					t.Errorf("error response leaks upstream text or carries a validator: %q %v", w.Body.String(), w.Header())
				}

				if f.db.entry(f.repo.ID, mc.key) != nil && fail.want != http.StatusOK {
					t.Errorf("a failed fetch left a cache entry")
				}
			})
		}
	}
}

func TestUpstreamMetadata_NotFoundIsNotFound(t *testing.T) {
	for _, mc := range metadataCases() {
		t.Run(mc.name, func(t *testing.T) {
			f, up := newModalFixture(t, mc)
			up.set(http.NotFound)

			if w := f.do(http.MethodGet, mc.get); w.Code != mc.notFound {
				t.Errorf("status = %d, want %d", w.Code, mc.notFound)
			}
		})
	}
}

func TestUpstreamMetadata_ExpiredCopyServedWhenUpstreamFails(t *testing.T) {
	for _, mc := range metadataCases() {
		for _, fail := range upstreamFailures(t) {
			if !fail.stale {
				continue
			}

			t.Run(mc.name+"/"+fail.name, func(t *testing.T) {
				f, up := newModalFixture(t, mc)
				shortClient(f)

				prime := f.do(http.MethodGet, mc.get)
				if prime.Code != http.StatusOK {
					t.Fatalf("priming = %d %q", prime.Code, prime.Body.String())
				}

				f.db.expire(f.repo.ID, mc.key)
				up.set(fail.h)

				w := f.do(http.MethodGet, mc.get)
				if w.Code != http.StatusOK || w.Header().Get("Warning") == "" {
					t.Errorf("= %d warning %q, want the cached copy with a staleness warning", w.Code, w.Header().Get("Warning"))
				}

				if strings.Contains(w.Body.String(), leakMarker) {
					t.Errorf("upstream error text reached the client")
				}
			})
		}
	}
}

func TestUpstreamMetadata_OversizedBodyIsRefusedAndNotCached(t *testing.T) {
	for _, mc := range metadataCases() {
		t.Run(mc.name, func(t *testing.T) {
			f, up := newModalFixture(t, mc)
			f.svc.maxMeta = 64

			up.set(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("x", 4096))
			})

			if w := f.do(http.MethodGet, mc.get); w.Code != http.StatusBadGateway {
				t.Errorf("oversized (declared length) = %d, want 502", w.Code)
			}

			// No Content-Length: the limit is enforced while reading.
			up.set(func(w http.ResponseWriter, _ *http.Request) {
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, strings.Repeat("x", 4096))
			})

			if w := f.do(http.MethodGet, mc.get); w.Code != http.StatusBadGateway {
				t.Errorf("oversized (streamed) = %d, want 502", w.Code)
			}

			if len(f.db.blobs) != 0 {
				t.Errorf("oversized body was stored: %v", f.db.blobs)
			}
		})
	}
}

func TestUpstreamMetadata_BlobMissingFromStoreIsRefetchedOnce(t *testing.T) {
	for _, mc := range metadataCases() {
		t.Run(mc.name, func(t *testing.T) {
			if mc.format == "nuget" {
				t.Skip("the parsed service index is held in memory, not re-read from the blob")
			}

			f, _ := newModalFixture(t, mc)
			store := &flakyGetStore{BlobStore: f.svc.blobStore}
			f.svc.blobStore = store

			if w := f.do(http.MethodGet, mc.get); w.Code != http.StatusOK {
				t.Fatalf("priming = %d", w.Code)
			}

			hits := f.hits.Load()

			store.failures.Store(1)

			if w := f.do(http.MethodGet, mc.get); w.Code != http.StatusOK {
				t.Errorf("after the blob vanished = %d, want 200 from a refetch", w.Code)
			}

			if f.hits.Load() <= hits {
				t.Errorf("the vanished blob was not refetched from the upstream")
			}
		})
	}
}

func TestUpstreamMetadata_BlobStaysMissingIs500WithoutDetail(t *testing.T) {
	for _, mc := range metadataCases() {
		t.Run(mc.name, func(t *testing.T) {
			if mc.format == "nuget" {
				t.Skip("the parsed service index is held in memory, not re-read from the blob")
			}

			f, _ := newModalFixture(t, mc)
			store := &flakyGetStore{BlobStore: f.svc.blobStore}
			f.svc.blobStore = store

			if w := f.do(http.MethodGet, mc.get); w.Code != http.StatusOK {
				t.Fatalf("priming = %d", w.Code)
			}

			store.failures.Store(1 << 20)

			w := f.do(http.MethodGet, mc.get)
			if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "blob gone") {
				t.Errorf("= %d %q, want a bare 500", w.Code, w.Body.String())
			}
		})
	}
}

func TestOpen_MapsUpstreamOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("not found", func(t *testing.T) {
		f := newFixture(t, http.NotFound)

		if _, err := f.svc.Open(ctx, f.repo, "a/b.xml"); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("blocked repository", func(t *testing.T) {
		f := newFixture(t, okBody("x"))
		f.repo.Blocked = true

		if _, err := f.svc.Open(ctx, f.repo, "a/b.xml"); !errors.Is(err, ErrBlocked) {
			t.Errorf("err = %v, want ErrBlocked", err)
		}
	})

	t.Run("unsafe path", func(t *testing.T) {
		f := newFixture(t, okBody("x"))

		for _, p := range []string{"../etc/passwd", "a/../../b", ""} {
			if _, err := f.svc.Open(ctx, f.repo, p); !errors.Is(err, ErrNotFound) {
				t.Errorf("Open(%q) err = %v, want ErrNotFound", p, err)
			}
		}

		if f.hits.Load() != 0 {
			t.Errorf("an unsafe path reached the upstream")
		}
	})

	for _, fail := range upstreamFailures(t) {
		t.Run(fail.name, func(t *testing.T) {
			f := newFixture(t, fail.h)
			shortClient(f)

			rc, err := f.svc.Open(ctx, f.repo, "a/b.xml")
			if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrBlocked) {
				if rc != nil {
					_ = rc.Close()
				}

				t.Fatalf("err = %v, want a fetch failure", err)
			}
		})
	}

	t.Run("expired copy served when the upstream is down", func(t *testing.T) {
		up := &modalUpstream{}
		up.set(okBody("fresh"))

		f := newFixture(t, up.ServeHTTP)

		read := func() (string, error) {
			rc, err := f.svc.Open(ctx, f.repo, "a/b.xml")
			if err != nil {
				return "", err
			}
			defer rc.Close()

			b, err := io.ReadAll(rc)

			return string(b), err
		}

		if got, err := read(); err != nil || got != "fresh" {
			t.Fatalf("priming = %q, %v", got, err)
		}

		f.db.expire(f.repo.ID, "a/b.xml")
		up.set(status(http.StatusInternalServerError))

		if got, err := read(); err != nil || got != "fresh" {
			t.Errorf("stale read = %q, %v, want the cached copy", got, err)
		}
	})

	t.Run("blob gone from the store is refetched", func(t *testing.T) {
		f := newFixture(t, okBody("body"))
		store := &flakyGetStore{BlobStore: f.svc.blobStore}
		f.svc.blobStore = store

		rc, err := f.svc.Open(ctx, f.repo, "a/b.xml")
		if err != nil {
			t.Fatal(err)
		}

		_ = rc.Close()
		store.failures.Store(1)

		rc, err = f.svc.Open(ctx, f.repo, "a/b.xml")
		if err != nil {
			t.Fatalf("Open after the blob vanished: %v", err)
		}

		_ = rc.Close()

		if f.hits.Load() != 2 {
			t.Errorf("upstream hits = %d, want 2 (one refetch)", f.hits.Load())
		}
	})

	t.Run("blob still missing after the refetch is an error", func(t *testing.T) {
		f := newFixture(t, okBody("body"))
		store := &flakyGetStore{BlobStore: f.svc.blobStore}
		store.failures.Store(1 << 20)
		f.svc.blobStore = store

		if rc, err := f.svc.Open(ctx, f.repo, "a/b.xml"); err == nil {
			_ = rc.Close()
			t.Error("Open succeeded without a readable blob")
		}
	})
}

// nugetOverride serves the standard feed except where override answers.
func nugetOverride(t *testing.T, override func(w http.ResponseWriter, r *http.Request) bool) (*fixture, *nugetFeed) {
	t.Helper()

	feed := &nugetFeed{t: t, nupkg: buildNupkg(t, "lib.a", "1.0.0")}
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if override(w, r) {
			return
		}

		feed.handler(w, r)
	})
	feed.base = f.upstreamBase()
	feed.cdn = feed.base
	f.repo.Format = "nuget"
	f.repo.Name = "nugetorg"
	f.repo.UpstreamURL = feed.base + "/v3/index.json"
	f.svc.SetBaseURL(oaBase + "/")

	return f, feed
}

// garbageStore serves other bytes than were stored, as a corrupted cache blob.
type garbageStore struct {
	storage.BlobStore

	body string
}

func (s *garbageStore) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.body)), nil
}

func TestNuGetProxy_CorruptCachedDocumentIs500WithoutValidators(t *testing.T) {
	f, _ := nugetOverride(t, func(http.ResponseWriter, *http.Request) bool { return false })

	// Prime the service index, then corrupt every blob read after it.
	if w := f.do(http.MethodGet, "v3/index.json"); w.Code != http.StatusOK {
		t.Fatalf("service index = %d", w.Code)
	}

	f.svc.blobStore = &garbageStore{BlobStore: f.svc.blobStore, body: "{not json"}

	w := f.do(http.MethodGet, "v3/registration/lib.a/1.0.0.json")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("a document that cannot be rendered = %d, want 500", w.Code)
	}

	if w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "" || strings.Contains(w.Body.String(), "json") {
		t.Errorf("error answer carries validators or detail: %v %q", w.Header(), w.Body.String())
	}
}

func TestNuGetProxy_UnreadablePageListingIsAGatewayError(t *testing.T) {
	f, _ := nugetOverride(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/reg/lib.a/index.json" {
			return false
		}

		_, _ = io.WriteString(w, `{"items":"nope"}`)

		return true
	})

	w := f.do(http.MethodGet, "v3/registration/lib.a/page/1.0.0/1.0.0.json")
	if w.Code != http.StatusBadGateway {
		t.Errorf("page of an unreadable registration = %d, want 502", w.Code)
	}
}

func TestNuGetProxy_SearchUpstreamFailures(t *testing.T) {
	tests := []struct {
		name string
		h    http.HandlerFunc
		want int
	}{
		{"500", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, leakMarker, http.StatusInternalServerError)
		}, http.StatusBadGateway},
		{"403", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, leakMarker, http.StatusForbidden) }, http.StatusBadGateway},
		{"404", http.NotFound, http.StatusNotFound},
		{"truncated", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "5000")
			_, _ = io.WriteString(w, `{"data":`)
		}, http.StatusBadGateway},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>"+leakMarker) }, http.StatusBadGateway},
		{"corrupt gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte{0x1f, 0x8b, 0x08, 0x00, 0xff, 0xff})
		}, http.StatusBadGateway},
		{"redirect to the metadata address", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
		}, http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := nugetOverride(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/search/query" {
					return false
				}

				tt.h(w, r)

				return true
			})

			w := f.do(http.MethodGet, "v3/query")
			if w.Code != tt.want || strings.Contains(w.Body.String(), leakMarker) {
				t.Errorf("= %d %q, want %d without upstream text", w.Code, w.Body.String(), tt.want)
			}
		})
	}
}

func TestNuGetProxy_CatalogFailureRefusesThePackage(t *testing.T) {
	tests := []struct {
		name string
		h    http.HandlerFunc
		want int
	}{
		{"catalog 500", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, leakMarker, http.StatusInternalServerError)
		}, http.StatusBadGateway},
		{"catalog 403", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, leakMarker, http.StatusForbidden) }, http.StatusBadGateway},
		{"catalog not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, feed := nugetOverride(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/catalog/lib.a.1.0.0.json" {
					return false
				}

				tt.h(w, r)

				return true
			})

			w := f.do(http.MethodGet, "v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg")
			if w.Code == http.StatusOK && tt.want != http.StatusOK {
				t.Errorf("a package whose digest could not be checked was served")
			}

			if w.Code != http.StatusOK && (w.Code != tt.want || strings.Contains(w.Body.String(), leakMarker)) {
				t.Errorf("= %d %q, want %d without upstream text", w.Code, w.Body.String(), tt.want)
			}

			_ = feed
		})
	}
}
