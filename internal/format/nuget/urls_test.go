// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/publicurl"
)

// urlRouter serves a repository holding Contoso.Lib 1.0.0 behind
// publicurl.Middleware, as cmd/server does. explicit is BASE_URL ("" when unset).
func urlRouter(t *testing.T, explicit string, trusted func(*http.Request) bool) http.Handler {
	t.Helper()

	db := &memDB{DB: formattest.NewDB(), pkgs: []*models.NuGetPackage{{
		ID:               "nuget-1",
		PackageID:        "contoso.lib",
		Version:          "1.0.0",
		DependencyGroups: []byte("[]"),
		CreatedAt:        time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
	}}}
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

func forwarded(host, proto string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Proto", proto)
	}
}

type originCase struct {
	name     string
	explicit string
	trusted  func(*http.Request) bool
	mutate   func(*http.Request)
	want     string
}

// originCases are the ways the origin of the URLs is decided.
func originCases() []originCase {
	return []originCase{
		{"explicit BASE_URL", "https://oa.example.com", trustAllPeers, forwarded("evil.test", "http"), "https://oa.example.com"},
		{
			"explicit BASE_URL keeps its path prefix",
			"https://oa.example.com/artifacts/",
			trustNoPeers,
			nil,
			"https://oa.example.com/artifacts",
		},
		{"Host header", "", trustNoPeers, nil, "http://a.example"},
		{
			"forwarded headers from a trusted peer",
			"",
			trustAllPeers,
			forwarded("artifacts.example.com", "https"),
			"https://artifacts.example.com",
		},
		{"forwarded headers from an untrusted peer are ignored", "", trustNoPeers, forwarded("evil.test", "https"), "http://a.example"},
	}
}

func serviceIndexIDs(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()

	require.Equal(t, http.StatusOK, w.Code)

	var doc struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))

	ids := make(map[string]string, len(doc.Resources))
	for _, r := range doc.Resources {
		ids[r.Type] = r.ID
	}

	return ids
}

// The service index @ids are full URLs (NuGet resource URLs are absolute), built
// on the origin the client reached unless BASE_URL overrides it.
func TestServiceIndexUsesDerivedOrigin(t *testing.T) {
	t.Parallel()

	for _, c := range originCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			w := urlGet(t, urlRouter(t, c.explicit, c.trusted), "/repos/myrepo/v3/index.json", c.mutate)
			ids := serviceIndexIDs(t, w)

			root := c.want + "/repos/myrepo/v3"
			assert.Equal(t, root+"/registration/", ids["RegistrationsBaseUrl"])
			assert.Equal(t, root+"/package/", ids["PackageBaseAddress/3.0.0"])
			assert.Equal(t, root+"/query", ids["SearchQueryService"])
			assert.Equal(t, root+"/autocomplete", ids["SearchAutocompleteService"])
			assert.Equal(t, root+"/package", ids["PackagePublish/2.0.0"])
		})
	}
}

// Every URL of the registration index, a leaf and a search result is on the
// request's origin, and the index's validator and cache key differ per origin:
// a client holding an ETag for one origin never gets a 304 for another.
func TestRegistrationUsesDerivedOriginAndETagFollowsIt(t *testing.T) {
	t.Parallel()

	h := urlRouter(t, "", trustNoPeers)
	const path = "/repos/myrepo/v3/registration/contoso.lib/index.json"

	a := urlGet(t, h, path, nil)
	require.Equal(t, http.StatusOK, a.Code)
	assert.Contains(t, a.Body.String(), "http://a.example/repos/myrepo/v3/registration/contoso.lib/1.0.0.json")
	assert.Contains(t, a.Body.String(), "http://a.example/repos/myrepo/v3/package/contoso.lib/1.0.0/contoso.lib.1.0.0.nupkg")
	assert.NotContains(t, a.Body.String(), "b.example")
	assert.Contains(t, a.Header().Values("Vary"), "Host, X-Forwarded-Host, X-Forwarded-Proto")

	onB := func(r *http.Request) { r.Host = "b.example" }

	b := urlGet(t, h, path, onB)
	require.Equal(t, http.StatusOK, b.Code)
	assert.Contains(t, b.Body.String(), "http://b.example/repos/myrepo/v3/registration/contoso.lib/1.0.0.json")
	assert.NotContains(t, b.Body.String(), "a.example")
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))

	// The same origin revalidates; another origin does not.
	same := urlGet(t, h, path, func(r *http.Request) { r.Header.Set("If-None-Match", a.Header().Get("ETag")) })
	assert.Equal(t, http.StatusNotModified, same.Code)

	other := urlGet(t, h, path, func(r *http.Request) {
		onB(r)
		r.Header.Set("If-None-Match", a.Header().Get("ETag"))
	})
	assert.Equal(t, http.StatusOK, other.Code)
	assert.Contains(t, other.Body.String(), "http://b.example/")

	leaf := urlGet(t, h, "/repos/myrepo/v3/registration/contoso.lib/1.0.0.json", onB)
	require.Equal(t, http.StatusOK, leaf.Code)
	assert.Contains(t, leaf.Body.String(), "http://b.example/repos/myrepo/v3/package/contoso.lib/1.0.0/contoso.lib.1.0.0.nupkg")
	assert.Contains(t, leaf.Body.String(), "http://b.example/repos/myrepo/v3/registration/contoso.lib/index.json")

	search := urlGet(t, h, "/repos/myrepo/v3/query?q=contoso", onB)
	require.Equal(t, http.StatusOK, search.Code)
	assert.Contains(t, search.Body.String(), "http://b.example/repos/myrepo/v3/registration/contoso.lib/index.json")
}

// An explicit BASE_URL fixes every URL whatever the request says.
func TestRegistrationExplicitBaseURLWins(t *testing.T) {
	t.Parallel()

	h := urlRouter(t, "https://oa.example.com", trustAllPeers)

	for _, host := range []string{"a.example", "b.example"} {
		w := urlGet(t, h, "/repos/myrepo/v3/registration/contoso.lib/index.json", func(r *http.Request) {
			r.Host = host
			forwarded("evil.test", "http")(r)
		})
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "https://oa.example.com/repos/myrepo/v3/registration/contoso.lib/1.0.0.json")
		assert.NotContains(t, w.Body.String(), "evil.test")
		assert.NotContains(t, w.Header().Values("Vary"), "Host, X-Forwarded-Host, X-Forwarded-Proto")
	}
}
