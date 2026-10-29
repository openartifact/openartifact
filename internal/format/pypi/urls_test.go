// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/publicurl"
)

// urlRouter serves the handler behind publicurl.Middleware, as cmd/server does.
func urlRouter(t *testing.T, explicit string, trusted func(*http.Request) bool) http.Handler {
	t.Helper()

	db := &benchPyPIDB{
		DB:       formattest.NewDB(),
		all:      []*models.PyPIPackage{benchPyPIFile("demo", "1.0", ".tar.gz")},
		versions: []*models.PyPIPackage{benchPyPIFile("demo", "1.0", "-py3-none-any.whl")},
	}
	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), explicit)

	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	return publicurl.Middleware(explicit, "http://fallback.test", trusted)(router)
}

func urlGet(t *testing.T, h http.Handler, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
	req.Host = "a.example"
	req.RemoteAddr = "192.0.2.1:1234"

	if mutate != nil {
		mutate(req)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return w
}

func trustAllPeers(*http.Request) bool { return true }

func trustNoPeers(*http.Request) bool { return false }

// The simple pages hold references relative to the page URL and no origin, so
// their bodies and ETags are the same on every host.
func TestSimplePagesAreRelative(t *testing.T) {
	t.Parallel()

	h := urlRouter(t, "", trustNoPeers)

	root := urlGet(t, h, "/repos/myrepo/simple/", nil)
	require.Equal(t, http.StatusOK, root.Code)
	assert.Contains(t, root.Body.String(), `<a href="demo/">demo</a>`)

	page := urlGet(t, h, "/repos/myrepo/simple/demo/", nil)
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), `<a href="../../files/demo-1.0-py3-none-any.whl#sha256=`)
	assert.NotContains(t, page.Body.String(), "example")

	// Resolved against the page URL, the reference reaches the file route.
	base, err := url.Parse("http://a.example/repos/myrepo/simple/demo/")
	require.NoError(t, err)

	ref, err := url.Parse("../../files/demo-1.0-py3-none-any.whl")
	require.NoError(t, err)
	assert.Equal(t, "http://a.example/repos/myrepo/files/demo-1.0-py3-none-any.whl", base.ResolveReference(ref).String())

	other := urlGet(t, h, "/repos/myrepo/simple/demo/", func(r *http.Request) { r.Host = "b.example" })
	assert.Equal(t, page.Body.String(), other.Body.String())
	assert.Equal(t, page.Header().Get("ETag"), other.Header().Get("ETag"))
	assert.NotEmpty(t, page.Header().Get("ETag"))
	assert.NotContains(t, page.Header().Get("Vary"), "Host")
}

// The legacy JSON API keeps absolute URLs from the public origin.
func TestLegacyJSONURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		explicit string
		trusted  func(*http.Request) bool
		mutate   func(*http.Request)
		want     string
	}{
		{"explicit BASE_URL", "https://oa.example.com", trustAllPeers, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Host", "evil.test")
		}, "https://oa.example.com"},
		{"host only", "", trustNoPeers, nil, "http://a.example"},
		{"forwarded from a trusted peer", "", trustAllPeers, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Host", "pub.example")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "https://pub.example"},
		{"forwarded ignored from an untrusted peer", "", trustNoPeers, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Host", "pub.example")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "http://a.example"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := urlGet(t, urlRouter(t, tc.explicit, tc.trusted), "/repos/myrepo/json/demo", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code)

			var doc struct {
				Releases map[string][]struct {
					URL string `json:"url"`
				} `json:"releases"`
			}

			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
			require.Len(t, doc.Releases["1.0"], 1)
			assert.Equal(t, tc.want+"/repos/myrepo/files/demo-1.0-py3-none-any.whl", doc.Releases["1.0"][0].URL)

			if tc.explicit == "" {
				assert.Contains(t, w.Header().Get("Vary"), "Host")
			}
		})
	}
}
