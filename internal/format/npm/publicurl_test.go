// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/publicurl"
	"github.com/openartifact/openartifact/pkg/logger"
)

// packumentAt serves GET /repos/myrepo/lodash through publicurl.Middleware and
// returns the recorder. Only the peer 10.0.0.1 is a trusted proxy; a request
// starts from 203.0.113.9.
func packumentAt(t *testing.T, explicit string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	db := &tarballMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "myrepo", Public: true},
		pkg:  &models.NPMPackage{ID: "pkg-1", FullName: "lodash", UpdatedAt: time.Unix(1700000000, 0)},
		versions: []*models.NPMVersion{
			{
				ID:              "v1",
				PackageID:       "pkg-1",
				Version:         "1.0.0",
				TarballFilename: "lodash-1.0.0.tgz",
				ManifestJSON:    []byte(`{"name":"lodash","version":"1.0.0"}`),
			},
		},
	}

	log, err := logger.New("info", "json", "stdout")
	require.NoError(t, err)

	h := NewHTTPHandler(log, db, &tarballMockBlobStore{}, explicit)
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

	trusted := func(r *http.Request) bool { return r.RemoteAddr == "10.0.0.1:4000" }

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/lodash", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	mutate(req)

	w := httptest.NewRecorder()
	publicurl.Middleware(explicit, "", trusted)(router).ServeHTTP(w, req)

	return w
}

func TestPackument_TarballOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		explicit string
		mutate   func(*http.Request)
		want     string
	}{
		{"explicit BASE_URL wins", "https://oa.example/prefix", func(r *http.Request) { r.Host = "other.test" },
			"https://oa.example/prefix/repos/myrepo/lodash/-/lodash-1.0.0.tgz"},
		{"host only", "", func(r *http.Request) { r.Host = "oa.test:8080" },
			"http://oa.test:8080/repos/myrepo/lodash/-/lodash-1.0.0.tgz"},
		{"forwarded from a trusted peer", "", func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:4000"
			r.Host = "internal:8080"
			r.Header.Set("X-Forwarded-Host", "registry.example")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "https://registry.example/repos/myrepo/lodash/-/lodash-1.0.0.tgz"},
		{"forwarded from an untrusted peer is ignored", "", func(r *http.Request) {
			r.Host = "real.test"
			r.Header.Set("X-Forwarded-Host", "evil.example")
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "http://real.test/repos/myrepo/lodash/-/lodash-1.0.0.tgz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := packumentAt(t, tt.explicit, tt.mutate)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), `"tarball":"`+tt.want+`"`)

			if tt.explicit == "" {
				assert.Contains(t, w.Header().Values("Vary"), "Host, X-Forwarded-Host, X-Forwarded-Proto")
				assert.Contains(t, w.Header().Values("Vary"), "Accept")
			}
		})
	}
}

func TestPackument_ETagFollowsOrigin(t *testing.T) {
	t.Parallel()

	a := packumentAt(t, "", func(r *http.Request) { r.Host = "a.test" })
	b := packumentAt(t, "", func(r *http.Request) { r.Host = "b.test" })
	a2 := packumentAt(t, "", func(r *http.Request) { r.Host = "a.test" })

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, a.Header().Get("ETag"), a2.Header().Get("ETag"))

	// An ETag earned at origin a is no validator at origin b.
	w := packumentAt(t, "", func(r *http.Request) {
		r.Host = "b.test"
		r.Header.Set("If-None-Match", a.Header().Get("ETag"))
	})
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "http://b.test/repos/myrepo/lodash/-/lodash-1.0.0.tgz")
}
