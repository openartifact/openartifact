// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

const (
	metaKey    = "g/a/maven-metadata.xml"
	staleBody  = "<metadata>v1</metadata>"
	freshBody  = "<metadata>v2</metadata>"
	staleValue = `110 - "Response is Stale"`
)

// staleFixture caches metaKey, then lets the test switch the upstream into a
// failure mode via the returned handler setter and expire the entry.
type staleFixture struct {
	*fixture

	mode atomic.Value // func(http.ResponseWriter, *http.Request)
}

func newStaleFixture(t *testing.T) *staleFixture {
	t.Helper()

	sf := &staleFixture{}
	sf.mode.Store(okBody(staleBody))
	sf.fixture = newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		sf.mode.Load().(http.HandlerFunc)(w, r)
	})

	if w := sf.do(http.MethodGet, metaKey); w.Code != http.StatusOK || w.Body.String() != staleBody {
		t.Fatalf("priming fetch: status %d body %q", w.Code, w.Body.String())
	}

	return sf
}

func (sf *staleFixture) setUpstream(h http.HandlerFunc) { sf.mode.Store(h) }

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

// dropConnection closes the TCP connection without answering.
func dropConnection(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

// truncated promises more bytes than it sends.
func truncated(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Length", "1000")
	_, _ = io.WriteString(w, "<metadata")

	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func TestServe_ServesStaleWhenUpstreamFails(t *testing.T) {
	tests := []struct {
		name     string
		upstream http.HandlerFunc
	}{
		{"500", status(http.StatusInternalServerError)},
		{"502", status(http.StatusBadGateway)},
		{"503", status(http.StatusServiceUnavailable)},
		{"connection dropped", dropConnection},
		{"truncated body", truncated},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sf := newStaleFixture(t)
			sf.db.expire("repo-1", metaKey)
			sf.setUpstream(tc.upstream)

			w := sf.do(http.MethodGet, metaKey)
			if w.Code != http.StatusOK || w.Body.String() != staleBody {
				t.Fatalf("status %d body %q, want 200 with the cached copy", w.Code, w.Body.String())
			}

			if got := w.Header().Get("Warning"); got != staleValue {
				t.Errorf("Warning = %q, want %q", got, staleValue)
			}

			// The transport may transparently retry a dropped GET.
			if got := sf.hits.Load(); got < 2 {
				t.Errorf("upstream requests = %d, want the revalidation attempted", got)
			}
		})
	}
}

func TestServe_StaleOnUnreachableUpstream(t *testing.T) {
	sf := newStaleFixture(t)
	sf.db.expire("repo-1", metaKey)
	sf.repo.UpstreamURL = "http://127.0.0.1:1/"

	w := sf.do(http.MethodGet, metaKey)
	if w.Code != http.StatusOK || w.Header().Get("Warning") != staleValue || w.Body.String() != staleBody {
		t.Fatalf("status %d warning %q body %q", w.Code, w.Header().Get("Warning"), w.Body.String())
	}
}

// A host that does not resolve is an unreachable upstream (resolver outage):
// the stale copy stands in. A blocked address is a refusal and must not be
// masked by one.
func TestServe_StaleOnDNSFailureButNotOnBlockedAddress(t *testing.T) {
	tests := []struct {
		name     string
		upstream string
		want     int
	}{
		{"unresolvable host", "http://oa-unresolvable.invalid/", http.StatusOK},
		{"blocked address", "http://169.254.169.254/", http.StatusBadGateway},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sf := newStaleFixture(t)
			sf.db.expire("repo-1", metaKey)
			sf.repo.UpstreamURL = tc.upstream

			w := sf.do(http.MethodGet, metaKey)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}

			if stale := w.Header().Get("Warning") == staleValue; stale != (tc.want == http.StatusOK) {
				t.Errorf("Warning = %q", w.Header().Get("Warning"))
			}
		})
	}
}

func TestServe_StaleHeadHasNoBody(t *testing.T) {
	sf := newStaleFixture(t)
	sf.db.expire("repo-1", metaKey)
	sf.setUpstream(status(http.StatusInternalServerError))

	w := sf.do(http.MethodHead, metaKey)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Warning") != staleValue {
		t.Errorf("HEAD: status %d len %d warning %q", w.Code, w.Body.Len(), w.Header().Get("Warning"))
	}
}

func TestServe_FreshEntryIsNotMarkedStale(t *testing.T) {
	sf := newStaleFixture(t)
	sf.setUpstream(status(http.StatusInternalServerError))

	w := sf.do(http.MethodGet, metaKey)
	if w.Code != http.StatusOK || w.Header().Get("Warning") != "" {
		t.Errorf("status %d warning %q, want a plain cache hit", w.Code, w.Header().Get("Warning"))
	}
}

func TestServe_UpstreamNotFoundDoesNotServeStale(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusGone} {
		for _, negTTL := range []int{60, 0} {
			t.Run(fmt.Sprintf("%d/negttl=%d", code, negTTL), func(t *testing.T) {
				sf := newStaleFixture(t)
				sf.repo.NegativeCacheTTLSeconds = negTTL
				sf.db.expire("repo-1", metaKey)
				sf.setUpstream(status(code))

				if w := sf.do(http.MethodGet, metaKey); w.Code != http.StatusNotFound || w.Header().Get("Warning") != "" {
					t.Fatalf("status %d warning %q, want a plain 404", w.Code, w.Header().Get("Warning"))
				}

				// The removal sticks: a later upstream outage must not
				// resurrect the file from the dropped copy.
				sf.setUpstream(status(http.StatusInternalServerError))

				w := sf.do(http.MethodGet, metaKey)

				want := http.StatusBadGateway
				if negTTL > 0 {
					want = http.StatusNotFound // negatively cached
				}

				if w.Code != want {
					t.Errorf("after outage: status %d, want %d", w.Code, want)
				}
			})
		}
	}
}

func TestServe_ClientErrorsDoNotServeStale(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			sf := newStaleFixture(t)
			sf.db.expire("repo-1", metaKey)
			sf.setUpstream(status(code))

			if w := sf.do(http.MethodGet, metaKey); w.Code != http.StatusBadGateway {
				t.Errorf("status %d, want 502 (a credentials problem must stay visible)", w.Code)
			}
		})
	}
}

func TestServe_StaleBoundedByMaxStale(t *testing.T) {
	tests := []struct {
		name     string
		maxStale int
		expired  time.Duration
		want     int
	}{
		{"within window", 3600, 10 * time.Minute, http.StatusOK},
		{"beyond window", 3600, 2 * time.Hour, http.StatusBadGateway},
		{"disabled", 0, time.Minute, http.StatusBadGateway},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sf := newStaleFixture(t)
			sf.repo.MaxStaleSeconds = tc.maxStale
			sf.db.expireAgo("repo-1", metaKey, tc.expired)
			sf.setUpstream(status(http.StatusInternalServerError))

			if w := sf.do(http.MethodGet, metaKey); w.Code != tc.want {
				t.Errorf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestServe_RevalidationReplacesStaleEntry(t *testing.T) {
	sf := newStaleFixture(t)
	sf.db.expire("repo-1", metaKey)
	sf.setUpstream(okBody(freshBody))

	w := sf.do(http.MethodGet, metaKey)
	if w.Code != http.StatusOK || w.Body.String() != freshBody || w.Header().Get("Warning") != "" {
		t.Fatalf("status %d body %q warning %q, want the refetched copy without a warning", w.Code, w.Body.String(), w.Header().Get("Warning"))
	}

	e := sf.db.entry("repo-1", metaKey)
	if e == nil || e.BlobKey != sum(freshBody) || !e.ExpiresAt.After(time.Now()) {
		t.Fatalf("cache entry not replaced by the refetch: %+v", e)
	}

	// The outage after the refresh is covered by the new, fresh entry.
	sf.setUpstream(status(http.StatusInternalServerError))

	if w := sf.do(http.MethodGet, metaKey); w.Body.String() != freshBody || w.Header().Get("Warning") != "" {
		t.Errorf("body %q warning %q, want the fresh copy served as a plain hit", w.Body.String(), w.Header().Get("Warning"))
	}
}

func TestServe_StaleEntryWithLostBlobIs502(t *testing.T) {
	sf := newStaleFixture(t)
	store := &missingBlobStore{BlobStore: sf.svc.blobStore}
	sf.svc.blobStore = store

	sf.db.expire("repo-1", metaKey)
	sf.setUpstream(status(http.StatusInternalServerError))
	store.missing.Store(true)

	if w := sf.do(http.MethodGet, metaKey); w.Code == http.StatusOK {
		t.Errorf("status %d: served a stale entry whose blob is gone", w.Code)
	}
}

func TestServe_ServesStaleWhenUpstreamNeverAnswers(t *testing.T) {
	sf := newStaleFixture(t)
	sf.svc.client = netutil.SafeHTTPClientWithHeaderTimeout(fetchTimeout, 200*time.Millisecond)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	accepted := make(chan struct{}, 8)

	sf.setUpstream(func(_ http.ResponseWriter, r *http.Request) {
		accepted <- struct{}{}

		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	sf.db.expire("repo-1", metaKey)

	start := time.Now()
	w := sf.do(http.MethodGet, metaKey)

	if w.Code != http.StatusOK || w.Body.String() != staleBody || w.Header().Get("Warning") != staleValue {
		t.Fatalf("status %d body %q warning %q, want the stale copy", w.Code, w.Body.String(), w.Header().Get("Warning"))
	}

	select {
	case <-accepted:
	default:
		t.Error("upstream was never asked to revalidate")
	}

	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v: the header timeout did not cut the hung upstream short", d)
	}
}
