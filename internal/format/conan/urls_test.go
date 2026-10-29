// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/publicurl"
)

func trustAllPeers(*http.Request) bool { return true }

func trustNoPeers(*http.Request) bool { return false }

func forwarded(host, proto string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Proto", proto)
	}
}

// urlDo sends one request through the handler behind publicurl.Middleware, as
// cmd/server does; explicit is BASE_URL ("" when unset).
func (e *env) urlDo(
	explicit string, trusted func(*http.Request) bool, method, target, body string, mutate func(*http.Request),
) *httptest.ResponseRecorder {
	e.t.Helper()

	router := chi.NewRouter()
	router.Route("/repos/{name}", e.h.RegisterRepoRoutes)

	req := httptest.NewRequestWithContext(e.t.Context(), method, target, strings.NewReader(body))
	req.Host = "a.example"
	req.RemoteAddr = "192.0.2.1:1234"

	if mutate != nil {
		mutate(req)
	}

	w := httptest.NewRecorder()
	publicurl.Middleware(explicit, "http://fallback.test", trusted)(router).ServeHTTP(w, req)

	return w
}

// download_urls and upload_urls hand out absolute file URLs (a Conan 1 client
// resolves a relative one against a URL without trailing slash), built on
// BASE_URL when set, else on the origin the client reached; forwarded headers
// count from a trusted proxy only.
func TestV1URLMapsUseDerivedOrigin(t *testing.T) {
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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			e.h.BaseURL = ""

			e.v1Upload(v1Recipe, v1RecipeFiles())

			e.h.BaseURL = tc.explicit

			want := tc.want + "/repos/myrepo/v1/files/zlib/1.3/_/_/0/export/conanfile.py"

			dl := e.urlDo(tc.explicit, tc.trusted, http.MethodGet, v1Recipe+"/download_urls", "", tc.mutate)
			require.Equal(t, http.StatusOK, dl.Code)
			assert.Equal(t, want, decode[map[string]string](t, dl)["conanfile.py"])

			digest := e.urlDo(tc.explicit, tc.trusted, http.MethodGet, v1Recipe+"/digest", "", tc.mutate)
			require.Equal(t, http.StatusOK, digest.Code)
			assert.Equal(t, tc.want+"/repos/myrepo/v1/files/zlib/1.3/_/_/0/export/conanmanifest.txt",
				decode[map[string]string](t, digest)["conanmanifest.txt"])

			up := e.urlDo(tc.explicit, tc.trusted, http.MethodPost, v1Recipe+"/upload_urls", `{"conanfile.py":3}`, tc.mutate)
			require.Equal(t, http.StatusOK, up.Code)
			assert.Equal(t, want, decode[map[string]string](t, up)["conanfile.py"])
		})
	}
}

// The URL maps are answered fresh and without validator: a derived origin is
// advertised through Vary and never stored.
func TestV1URLMapsVaryOnDerivedOrigin(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	e.h.BaseURL = ""

	e.v1Upload(v1Recipe, v1RecipeFiles())

	w := e.urlDo("", trustNoPeers, http.MethodGet, v1Recipe+"/download_urls", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Values("Vary"), "Host, X-Forwarded-Host, X-Forwarded-Proto")
	assert.Empty(t, w.Header().Get("ETag"))

	b := e.urlDo("", trustNoPeers, http.MethodGet, v1Recipe+"/download_urls", "", func(r *http.Request) { r.Host = "b.example" })
	assert.Contains(t, b.Body.String(), "http://b.example/repos/myrepo/v1/files/")
	assert.NotContains(t, b.Body.String(), "a.example")
}
