// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/publicurl"
)

// handlerFor is a handler whose BASE_URL is explicit ("" derives the origin),
// holding one published acme.tools 1.0.0, behind publicurl.Middleware. Only the
// peer 10.0.0.1 is a trusted proxy.
func handlerFor(t *testing.T, explicit string) http.Handler {
	t.Helper()

	db := &ansibleDB{DB: formattest.NewDB()}
	db.Repo.Format = FormatName

	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), explicit)

	w, _ := publish(t, h, acme)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	trusted := func(r *http.Request) bool { return r.RemoteAddr == "10.0.0.1:4000" }

	return publicurl.Middleware(explicit, "", trusted)(router)
}

func getFrom(h http.Handler, target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, target, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	mutate(req)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	return w
}

func TestLinksOrigin(t *testing.T) {
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

			h := handlerFor(t, tt.explicit)
			apiRoot := tt.want + "/repos/myrepo/api/v3"

			detail := getFrom(h, api+"collections/acme/tools/versions/1.0.0/", tt.mutate)
			require.Equal(t, http.StatusOK, detail.Code)
			assert.Equal(t, apiRoot+"/artifacts/collections/acme-tools-1.0.0.tar.gz", decode(t, detail)["download_url"])

			list := getFrom(h, api+"collections/acme/tools/versions/", tt.mutate)
			assert.Contains(t, list.Body.String(), `"href":"`+apiRoot+`/collections/acme/tools/versions/1.0.0/"`)

			v3 := getFrom(h, api, tt.mutate)
			assert.Contains(t, v3.Body.String(), `"collections":"`+apiRoot+`/collections/"`)

			if tt.explicit == "" {
				assert.Contains(t, detail.Header().Get("Vary"), "X-Forwarded-Host")
			}
		})
	}
}

func TestETagFollowsOrigin(t *testing.T) {
	t.Parallel()

	h := handlerFor(t, "")
	host := func(name string) func(*http.Request) { return func(r *http.Request) { r.Host = name } }

	const apiDiscovery = "/repos/myrepo/api/"

	for _, target := range []string{apiDiscovery, api, api + "collections/acme/tools/versions/1.0.0/"} {
		a, b := getFrom(h, target, host("a.test")), getFrom(h, target, host("b.test"))
		require.NotEmpty(t, a.Header().Get("ETag"), target)

		if target == apiDiscovery {
			// The discovery document links relatively and names no origin.
			assert.Equal(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
			continue
		}

		assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"), target)

		w := getFrom(h, target, func(r *http.Request) {
			r.Host = "b.test"
			r.Header.Set("If-None-Match", a.Header().Get("ETag"))
		})
		assert.Equal(t, http.StatusOK, w.Code, target)
	}
}
