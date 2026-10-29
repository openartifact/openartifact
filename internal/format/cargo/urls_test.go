// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/publicurl"
)

// urlRouter serves the handler behind publicurl.Middleware, as cmd/server does.
// explicit is BASE_URL ("" when unset).
func urlRouter(t *testing.T, explicit string, trusted func(*http.Request) bool) http.Handler {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), &cargoDB{DB: formattest.NewDB()}, formattest.NewBlobStore(), explicit)

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

func forwarded(host, proto string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Proto", proto)
	}
}

// config.json's dl and api are absolute (cargo concatenates the dl template and
// never resolves it against the index URL): BASE_URL when set, else the origin
// the client reached, with forwarded headers counted from a trusted proxy only.
func TestConfigJSONUsesDerivedOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		explicit string
		trusted  func(*http.Request) bool
		mutate   func(*http.Request)
		want     string
	}{
		{"explicit BASE_URL", "https://oa.example.com", trustAllPeers, forwarded("evil.test", "http"), "https://oa.example.com"},
		{
			"explicit BASE_URL with a path prefix",
			"https://oa.example.com/artifacts/",
			trustNoPeers,
			nil,
			"https://oa.example.com/artifacts",
		},
		{"Host header", "", trustNoPeers, nil, "http://a.example"},
		{
			"trusted peer forwards host and proto",
			"",
			trustAllPeers,
			forwarded("artifacts.example.com", "https"),
			"https://artifacts.example.com",
		},
		{"untrusted peer cannot forward", "", trustNoPeers, forwarded("evil.test", "https"), "http://a.example"},
	}

	for _, tc := range tests {
		for _, path := range []string{"/repos/myrepo/config.json", "/repos/myrepo/index/config.json"} {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				t.Parallel()

				w := urlGet(t, urlRouter(t, tc.explicit, tc.trusted), path, tc.mutate)
				require.Equal(t, http.StatusOK, w.Code)

				var cfg map[string]any

				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cfg))
				assert.Equal(t, tc.want+"/repos/myrepo/api/v1/crates", cfg["dl"])
				assert.Equal(t, tc.want+"/repos/myrepo", cfg["api"])
			})
		}
	}
}

// The ETag is the hash of the body, which carries the origin: a client holding
// the validator of one origin never gets a 304 for another, and the response
// varies on the headers the origin came from.
func TestConfigJSONETagFollowsOrigin(t *testing.T) {
	t.Parallel()

	h := urlRouter(t, "", trustNoPeers)
	onB := func(r *http.Request) { r.Host = "b.example" }

	a := urlGet(t, h, "/repos/myrepo/config.json", nil)
	b := urlGet(t, h, "/repos/myrepo/config.json", onB)

	require.Equal(t, http.StatusOK, a.Code)
	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Contains(t, a.Header().Values("Vary"), "Host, X-Forwarded-Host, X-Forwarded-Proto")

	same := urlGet(t, h, "/repos/myrepo/config.json", func(r *http.Request) { r.Header.Set("If-None-Match", a.Header().Get("ETag")) })
	assert.Equal(t, http.StatusNotModified, same.Code)

	other := urlGet(t, h, "/repos/myrepo/config.json", func(r *http.Request) {
		onB(r)
		r.Header.Set("If-None-Match", a.Header().Get("ETag"))
	})
	assert.Equal(t, http.StatusOK, other.Code)
	assert.Contains(t, other.Body.String(), "http://b.example/repos/myrepo")
}

// With BASE_URL set the origin never varies, so neither does the validator.
func TestConfigJSONETagStableWithExplicitBaseURL(t *testing.T) {
	t.Parallel()

	h := urlRouter(t, "https://oa.example.com", trustAllPeers)

	a := urlGet(t, h, "/repos/myrepo/config.json", nil)
	b := urlGet(t, h, "/repos/myrepo/config.json", func(r *http.Request) {
		r.Host = "b.example"
		forwarded("evil.test", "http")(r)
	})

	require.Equal(t, http.StatusOK, a.Code)
	assert.Equal(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.NotContains(t, a.Header().Values("Vary"), "Host, X-Forwarded-Host, X-Forwarded-Proto")
}
