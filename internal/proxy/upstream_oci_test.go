// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ociUpstreamFixture points a docker proxy repository at handler. Requests
// carry no token: the handler sees them as an anonymous client would.
func ociUpstreamFixture(t *testing.T, handler http.HandlerFunc) (*ociFixture, *atomic.Int32) {
	t.Helper()

	f := newOCIFixture(t)

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	f.repo.UpstreamURL = srv.URL

	return f, &hits
}

func ociShortClient(f *ociFixture) { shortClient(&fixture{svc: f.svc}) }

func TestOCIList_UpstreamOutcomesMapToRegistryErrors(t *testing.T) {
	leaky := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, leakMarker)
		}
	}

	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })

	tests := []struct {
		name     string
		h        http.HandlerFunc
		want     int
		wantCode string
	}{
		{"500", leaky(500), 502, "UNAVAILABLE"},
		{"503", leaky(503), 502, "UNAVAILABLE"},
		{"429", leaky(429), 502, "UNAVAILABLE"},
		{"418", leaky(418), 502, "UNAVAILABLE"},
		{"404", leaky(404), 404, "NAME_UNKNOWN"},
		{"403", leaky(403), 404, "NAME_UNKNOWN"},
		{"401 without a usable challenge", leaky(401), 502, "UNAVAILABLE"},
		{"dropped connection", dropConnection, 502, "UNAVAILABLE"},
		{"timeout", func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-hang:
			case <-r.Context().Done():
			}
		}, 502, "UNAVAILABLE"},
		{"redirect to the metadata address", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/v2/", http.StatusFound)
		}, 502, "UNAVAILABLE"},
		{"truncated body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "5000")
			_, _ = io.WriteString(w, `{"name":"x","tags":[`)
		}, 502, "UNAVAILABLE"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>"+leakMarker) }, 502, "UNAVAILABLE"},
		{"json null", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "null") }, 502, "UNAVAILABLE"},
		{"json array", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "[]") }, 502, "UNAVAILABLE"},
		{"oversized listing", func(w http.ResponseWriter, _ *http.Request) {
			chunk := []byte(strings.Repeat(" ", 1<<20))
			for range maxListBody>>20 + 2 {
				_, _ = w.Write(chunk)
			}
		}, 502, "UNAVAILABLE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := ociUpstreamFixture(t, tt.h)
			ociShortClient(f)

			w := f.do(http.MethodGet, "hub/img/tags/list")
			if w.Code != tt.want || ociErrorCode(t, w) != tt.wantCode {
				t.Errorf("tags/list = %d %q, want %d %s", w.Code, w.Body.String(), tt.want, tt.wantCode)
			}

			if strings.Contains(w.Body.String(), leakMarker) {
				t.Errorf("upstream text leaked: %q", w.Body.String())
			}
		})
	}
}

func TestOCIList_ReferrersShareTheListingErrorMapping(t *testing.T) {
	dg := digestOf("subject")

	for status, want := range map[int]int{500: 502, 404: 404, 403: 404, 400: 502} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f, _ := ociUpstreamFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, leakMarker)
			})

			w := f.do(http.MethodGet, "hub/img/referrers/"+dg)
			if w.Code != want || strings.Contains(w.Body.String(), leakMarker) {
				t.Errorf("referrers = %d %q, want %d", w.Code, w.Body.String(), want)
			}
		})
	}
}

func TestOCIList_PrivateUpstreamIsRefused(t *testing.T) {
	f, hits := ociUpstreamFixture(t, func(http.ResponseWriter, *http.Request) {})
	t.Setenv(allowPrivateEnv, "false")

	w := f.do(http.MethodGet, "hub/img/tags/list")
	if w.Code != http.StatusBadGateway || hits.Load() != 0 {
		t.Errorf("= %d with %d upstream hits, want 502 and no connection", w.Code, hits.Load())
	}

	if w := f.do(http.MethodGet, "hub/img/referrers/"+digestOf("s")); w.Code != http.StatusBadGateway {
		t.Errorf("referrers = %d, want 502", w.Code)
	}
}

// challenge answers every /v2/ request with a 401 carrying wwwAuth; the token
// endpoint, when it exists, is counted in tokens.
func challengeUpstream(
	t *testing.T,
	wwwAuth func(base string) string,
	tokenHandler http.HandlerFunc,
) (*ociFixture, *atomic.Int32, *atomic.Int32) {
	t.Helper()

	var tokens atomic.Int32

	var base string

	f, hits := ociUpstreamFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" && tokenHandler != nil {
			tokens.Add(1)
			tokenHandler(w, r)

			return
		}

		if r.Header.Get("Authorization") == "Bearer good" {
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = io.WriteString(w, alpineManifest)

			return
		}

		w.Header().Set("WWW-Authenticate", wwwAuth(base))
		w.WriteHeader(http.StatusUnauthorized)
	})
	base = f.repo.UpstreamURL

	return f, hits, &tokens
}

func goodToken(extra string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"token":"good"`+extra+`}`)
	}
}

func TestOCIAuth_UnusableChallengeIsAGatewayError(t *testing.T) {
	tests := []struct {
		name string
		www  func(base string) string
	}{
		{"no challenge header", func(string) string { return "" }},
		{"unknown scheme", func(string) string { return `Negotiate abc` }},
		{"bearer without realm", func(string) string { return `Bearer service="x"` }},
		{"basic without stored credentials", func(string) string { return `Basic realm="x"` }},
		{"realm is not a url", func(string) string { return `Bearer realm="http://[::1"` }},
		{"realm with a file scheme", func(string) string { return `Bearer realm="file:///etc/passwd"` }},
		{"realm to the metadata address", func(string) string { return `Bearer realm="http://169.254.169.254/token"` }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, tokens := challengeUpstream(t, tt.www, goodToken(""))
			ociShortClient(f)

			w := f.do(http.MethodGet, "hub/img/manifests/1")
			if w.Code != http.StatusBadGateway || ociErrorCode(t, w) != "UNAVAILABLE" {
				t.Errorf("= %d %q, want 502 UNAVAILABLE", w.Code, w.Body.String())
			}

			if tokens.Load() != 0 {
				t.Errorf("the token endpoint was called %d times", tokens.Load())
			}

			if f.db.entry("docker-1", "img/manifests/1") != nil {
				t.Errorf("an unauthenticated pull left a cache entry")
			}
		})
	}
}

func TestOCIAuth_TokenEndpointFailures(t *testing.T) {
	tests := []struct {
		name string
		h    http.HandlerFunc
		want int
	}{
		{"500", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, leakMarker, http.StatusInternalServerError)
		}, 502},
		{"429", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, leakMarker, http.StatusTooManyRequests) }, 502},
		{"403", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, leakMarker, http.StatusForbidden) }, 502},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>"+leakMarker) }, 502},
		{"empty token", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"token":""}`) }, 502},
		{"oversized document", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"token":"`+strings.Repeat("a", maxTokenBody)+`"}`)
		}, 502},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := challengeUpstream(t, func(b string) string { return `Bearer realm="` + b + `/token",service="s"` }, tt.h)

			w := f.do(http.MethodGet, "hub/img/manifests/1")
			if w.Code != tt.want || strings.Contains(w.Body.String(), leakMarker) {
				t.Errorf("= %d %q, want %d without upstream text", w.Code, w.Body.String(), tt.want)
			}
		})
	}
}

func TestOCIAuth_TokenExpiry(t *testing.T) {
	realm := func(b string) string { return `Bearer realm="` + b + `/token",service="s"` }

	t.Run("access_token is accepted and reused within its lifetime", func(t *testing.T) {
		f, hits, tokens := challengeUpstream(t, realm, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"access_token":"good","expires_in":3600}`)
		})

		for _, ref := range []string{"1", "2"} {
			if w := f.do(http.MethodGet, "hub/img/manifests/"+ref); w.Code != http.StatusOK {
				t.Fatalf("pull %s = %d %q", ref, w.Code, w.Body.String())
			}
		}

		if tokens.Load() != 1 {
			t.Errorf("token requests = %d, want 1 (the token is reused)", tokens.Load())
		}

		_ = hits
	})

	t.Run("a lifetime inside the safety margin is not reused", func(t *testing.T) {
		f, _, tokens := challengeUpstream(t, realm, goodToken(`,"expires_in":5`))

		for _, ref := range []string{"1", "2"} {
			if w := f.do(http.MethodGet, "hub/img/manifests/"+ref); w.Code != http.StatusOK {
				t.Fatalf("pull %s = %d", ref, w.Code)
			}
		}

		if tokens.Load() != 2 {
			t.Errorf("token requests = %d, want 2", tokens.Load())
		}
	})

	t.Run("an excessive lifetime is capped", func(t *testing.T) {
		f, _, _ := challengeUpstream(t, realm, goodToken(`,"expires_in":999999999`))

		if w := f.do(http.MethodGet, "hub/img/manifests/1"); w.Code != http.StatusOK {
			t.Fatalf("pull = %d", w.Code)
		}

		f.svc.tokens.mu.Lock()
		defer f.svc.tokens.mu.Unlock()

		if len(f.svc.tokens.m) != 1 {
			t.Fatalf("cached tokens = %d, want 1", len(f.svc.tokens.m))
		}

		for _, tok := range f.svc.tokens.m {
			if until := time.Until(tok.expires); until > maxTokenTTL || until <= 0 {
				t.Errorf("token lives %v, want within (0, %v]", until, maxTokenTTL)
			}
		}
	})
}

func TestTokenCache_ExpiryAndEviction(t *testing.T) {
	var c tokenCache

	c.put("live", "Bearer a", time.Minute)
	c.put("dead", "Bearer b", -time.Second)

	if got := c.get("live"); got != "Bearer a" {
		t.Errorf("live token = %q", got)
	}

	if got := c.get("dead"); got != "" {
		t.Errorf("expired token = %q, want none", got)
	}

	if _, ok := c.m["dead"]; ok {
		t.Errorf("an expired token stayed in the cache after a lookup")
	}

	c.drop("live")

	if got := c.get("live"); got != "" {
		t.Errorf("dropped token = %q", got)
	}

	// At capacity, expired tokens go first and the live ones survive.
	c = tokenCache{}

	for i := range maxCachedTokens - 1 {
		c.put(fmt.Sprint("dead-", i), "x", -time.Second)
	}

	c.put("live", "Bearer a", time.Minute)
	c.put("new", "Bearer n", time.Minute)

	if c.get("live") != "Bearer a" || c.get("new") != "Bearer n" || len(c.m) != 2 {
		t.Errorf("after eviction of expired tokens: %d entries", len(c.m))
	}

	// With nothing expired the cache is emptied rather than growing.
	c = tokenCache{}

	for i := range maxCachedTokens {
		c.put(fmt.Sprint("k-", i), "x", time.Hour)
	}

	c.put("one-more", "Bearer z", time.Hour)

	if len(c.m) != 1 || c.get("one-more") != "Bearer z" {
		t.Errorf("cache holds %d entries after overflow, want only the newest", len(c.m))
	}
}

func TestOCIAuth_BasicChallengeFailuresAreGatewayErrors(t *testing.T) {
	f, _, _ := challengeUpstream(t, func(string) string { return `Basic realm="x"` }, nil)
	f.repo.UpstreamUsername = "robot"
	f.repo.UpstreamPasswordEnc = "s3cret"

	// The stored credentials are sent once; an upstream that still refuses
	// them is a denial, not a gateway fault.
	if w := f.do(http.MethodGet, "hub/img/manifests/1"); w.Code != http.StatusNotFound || ociErrorCode(t, w) != "MANIFEST_UNKNOWN" {
		t.Errorf("refused credentials = %d %q, want 404 MANIFEST_UNKNOWN", w.Code, w.Body.String())
	}
}

func TestOCIStore_UpstreamBodiesAreVerifiedBeforeCaching(t *testing.T) {
	digest512 := func(s string) string {
		sum := sha512.Sum512([]byte(s))
		return "sha512:" + hex.EncodeToString(sum[:])
	}

	manifest := func(w http.ResponseWriter, body string, hdr map[string]string) {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")

		for k, v := range hdr {
			w.Header().Set(k, v)
		}

		_, _ = io.WriteString(w, body)
	}

	tests := []struct {
		name string
		path string
		key  string
		h    http.HandlerFunc
		want int
	}{
		{"manifest with a matching sha512 header", "hub/img/manifests/t", "img/manifests/t", func(w http.ResponseWriter, _ *http.Request) {
			manifest(w, alpineManifest, map[string]string{"Docker-Content-Digest": digest512(alpineManifest)})
		}, http.StatusOK},
		{
			"manifest with a mismatching sha512 header",
			"hub/img/manifests/t",
			"img/manifests/t",
			func(w http.ResponseWriter, _ *http.Request) {
				manifest(w, alpineManifest, map[string]string{"Docker-Content-Digest": digest512("other")})
			},
			http.StatusBadGateway,
		},
		{"manifest that is not json", "hub/img/manifests/t", "img/manifests/t", func(w http.ResponseWriter, _ *http.Request) {
			manifest(w, "<html>"+leakMarker+"</html>", nil)
		}, http.StatusBadGateway},
		{"manifest that is json null", "hub/img/manifests/t", "img/manifests/t", func(w http.ResponseWriter, _ *http.Request) {
			manifest(w, "null", nil)
		}, http.StatusBadGateway},
		{"truncated manifest", "hub/img/manifests/t", "img/manifests/t", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "5000")
			_, _ = io.WriteString(w, alpineManifest)
		}, http.StatusBadGateway},
		{"oversized manifest", "hub/img/manifests/t", "img/manifests/t", func(w http.ResponseWriter, _ *http.Request) {
			chunk := []byte(strings.Repeat(" ", 1<<20))
			for range maxManifestBody>>20 + 2 {
				_, _ = w.Write(chunk)
			}
		}, http.StatusBadGateway},
		{"manifest 500", "hub/img/manifests/t", "img/manifests/t", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, leakMarker, http.StatusInternalServerError)
		}, http.StatusBadGateway},
		{"manifest 400", "hub/img/manifests/t", "img/manifests/t", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, leakMarker, http.StatusBadRequest)
		}, http.StatusBadGateway},
		{
			"blob with a mismatching sha512 digest",
			"hub/img/blobs/" + digest512("wanted"),
			"img/blobs/" + digest512("wanted"),
			func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "tampered")
			},
			http.StatusBadGateway,
		},
		{
			"blob with a matching sha512 digest",
			"hub/img/blobs/" + digest512("wanted"),
			"img/blobs/" + digest512("wanted"),
			func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "wanted")
			},
			http.StatusOK,
		},
		{
			"truncated blob",
			"hub/img/blobs/" + digestOf("wanted"),
			"img/blobs/" + digestOf("wanted"),
			func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "100")
				_, _ = io.WriteString(w, "wanted")
			},
			http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := ociUpstreamFixture(t, tt.h)

			w := f.do(http.MethodGet, tt.path)
			if w.Code != tt.want || strings.Contains(w.Body.String(), leakMarker) {
				t.Fatalf("= %d %q, want %d without upstream text", w.Code, w.Body.String(), tt.want)
			}

			if cached := f.db.entry("docker-1", tt.key) != nil; cached != (tt.want == http.StatusOK) {
				t.Errorf("cache entry present = %v for a %d answer", cached, w.Code)
			}
		})
	}
}

func TestOCIStore_BlobOverTheSizeLimitIsRefused(t *testing.T) {
	dg := digestOf("0123456789")

	for name, h := range map[string]http.HandlerFunc{
		"declared": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "0123456789") },
		"streamed": func(w http.ResponseWriter, _ *http.Request) {
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "0123456789")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := ociUpstreamFixture(t, h)
			f.svc.maxBody = 4

			if w := f.do(http.MethodGet, "hub/img/blobs/"+dg); w.Code != http.StatusBadGateway {
				t.Errorf("oversized blob = %d, want 502", w.Code)
			}

			if f.db.entry("docker-1", "img/blobs/"+dg) != nil || len(f.db.blobs) != 0 {
				t.Errorf("an oversized blob was cached")
			}
		})
	}
}

func TestOCIStore_UpstreamTimeoutIs502(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })

	f, _ := ociUpstreamFixture(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	})
	ociShortClient(f)

	w := f.do(http.MethodGet, "hub/img/manifests/t")
	if w.Code != http.StatusBadGateway || ociErrorCode(t, w) != "UNAVAILABLE" {
		t.Errorf("= %d %q, want 502 UNAVAILABLE", w.Code, w.Body.String())
	}
}
