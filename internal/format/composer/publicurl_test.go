// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/publicurl"
	"github.com/openartifact/openartifact/pkg/composer/composertest"
)

// handlerFor is a handler whose BASE_URL is explicit ("" derives the origin),
// holding one published acme/tools 1.0.0. Only the peer 10.0.0.1 is a trusted
// proxy.
func handlerFor(t *testing.T, explicit string) (http.Handler, string) {
	t.Helper()

	db := &composerDB{DB: formattest.NewDB()}
	db.Repo.Format = FormatName

	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), explicit)
	require.Equal(t, http.StatusCreated, upload(h, "acme/tools", "1.0.0", composertest.Build(t, acme)).Code)

	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	trusted := func(r *http.Request) bool { return r.RemoteAddr == "10.0.0.1:4000" }

	return publicurl.Middleware(explicit, "", trusted)(router), db.rows[0].SHA256
}

func getFrom(h http.Handler, target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, target, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	mutate(req)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return w
}

func TestRootFieldsArePathAbsoluteAndOriginFree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, explicit, prefix string
	}{
		{"no BASE_URL", "", ""},
		{"BASE_URL", "https://oa.example", ""},
		{"BASE_URL with a path prefix", "https://oa.example/artifacts", "/artifacts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, _ := handlerFor(t, tt.explicit)
			a := getFrom(h, root+"packages.json", func(r *http.Request) { r.Host = "a.test" })
			b := getFrom(h, root+"packages.json", func(r *http.Request) { r.Host = "b.test" })

			require.Equal(t, http.StatusOK, a.Code)

			doc := decode(t, a)
			assert.Equal(t, tt.prefix+"/repos/myrepo/p2/%package%.json", doc["metadata-url"])
			assert.Equal(t, tt.prefix+"/repos/myrepo/list.json", doc["list"])
			assert.Equal(t, tt.prefix+"/repos/myrepo/search.json?q=%query%&type=%type%", doc["search"])

			// Names no origin, so the validator is the same through every host.
			assert.Equal(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
			assert.NotContains(t, a.Header().Get("Vary"), "Host")
		})
	}
}

func TestDistURLOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, explicit string
		mutate         func(*http.Request)
		want           string
	}{
		{
			"explicit BASE_URL",
			"https://oa.example/artifacts",
			func(r *http.Request) { r.Host = "ignored.test" },
			"https://oa.example/artifacts",
		},
		{"host only", "", func(r *http.Request) { r.Host = "oa.test:8080" }, "http://oa.test:8080"},
		{"trusted forwarded", "", func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:4000"
			r.Host = "internal"
			r.Header.Set("X-Forwarded-Host", "registry.example")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "https://registry.example"},
		{"untrusted forwarded ignored", "", func(r *http.Request) {
			r.Host = "real.test"
			r.Header.Set("X-Forwarded-Host", "evil.example")
		}, "http://real.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, sha := handlerFor(t, tt.explicit)
			w := getFrom(h, root+"p2/acme/tools.json", tt.mutate)
			vs := versions(t, w, "acme/tools")
			require.Len(t, vs, 1)

			dist, _ := vs[0]["dist"].(map[string]any)
			assert.Equal(t, tt.want+"/repos/myrepo/dists/acme/tools/1.0.0/"+sha+".zip", dist["url"])

			if tt.explicit == "" {
				assert.Contains(t, w.Header().Get("Vary"), "X-Forwarded-Host")
			}
		})
	}
}

func TestMetadataETagFollowsOrigin(t *testing.T) {
	t.Parallel()

	h, _ := handlerFor(t, "")
	at := func(host string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		return getFrom(h, root+"p2/acme/tools.json", func(r *http.Request) {
			r.Host = host
			for _, m := range mutate {
				m(r)
			}
		})
	}

	a, b := at("a.test"), at("b.test")
	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, a.Header().Get("ETag"), at("a.test").Header().Get("ETag"))

	// The ETag of origin a never revalidates the document of origin b.
	w := at("b.test", func(r *http.Request) { r.Header.Set("If-None-Match", a.Header().Get("ETag")) })
	assert.Equal(t, http.StatusOK, w.Code)

	w = at("a.test", func(r *http.Request) { r.Header.Set("If-None-Match", a.Header().Get("ETag")) })
	assert.Equal(t, http.StatusNotModified, w.Code)
}

func TestSearchURLOrigin(t *testing.T) {
	t.Parallel()

	h, _ := handlerFor(t, "")
	a := getFrom(h, root+"search.json?q=tools", func(r *http.Request) { r.Host = "a.test" })
	b := getFrom(h, root+"search.json?q=tools", func(r *http.Request) { r.Host = "b.test" })

	assert.Contains(t, a.Body.String(), "http://a.test/repos/myrepo/p2/acme/tools.json")
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
}
