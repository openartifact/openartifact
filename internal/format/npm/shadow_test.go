// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type shadowMockDB struct{ tarballMockDB }

func (*shadowMockDB) GetNPMDistTags(context.Context, string) (map[string]string, error) {
	return map[string]string{"latest": "1.0.0"}, nil
}

// FMTA-14: package names that collide with another format's static route are
// answered by npm in an npm repository, and by the other format elsewhere.
func TestShadowGuard_NPMPackageNamedCharts(t *testing.T) {
	for _, tc := range []struct {
		format, path string
		wantNPM      bool
	}{
		{"npm", "/repos/r/charts", true},
		{"npm", "/repos/r/config.json", true},
		{"npm", "/repos/r/charts/1.0.0", true},
		{"helm", "/repos/r/charts", false},
		{"cargo", "/repos/r/config.json", false},
	} {
		db := &shadowMockDB{
			repo: &models.Repository{ID: "repo-1", Name: "r", Format: tc.format},
			pkg:  &models.NPMPackage{ID: "pkg-1", FullName: "charts", UpdatedAt: time.Unix(1700000000, 0)},
			versions: []*models.NPMVersion{{
				ID: "v1", PackageID: "pkg-1", Version: "1.0.0", TarballFilename: "charts-1.0.0.tgz",
				ManifestJSON: []byte(`{"name":"charts","version":"1.0.0"}`),
			}}}

		log, err := logger.New("error", "json", "stdout")
		if err != nil {
			t.Fatal(err)
		}

		h := NewHTTPHandler(log, db, &tarballMockBlobStore{}, "http://localhost:8080")
		router := chi.NewRouter()
		router.Route("/repos/{name}", func(r chi.Router) {
			r.Use(h.ShadowGuard)
			// Stand-ins for the other formats' static routes.
			other := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OTHER")) }
			r.Get("/charts", other)
			r.Get("/charts/{filename}", other)
			r.Get("/config.json", other)
			h.RegisterRepoRoutes(r)
		})

		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, nil))

		isOther := strings.Contains(w.Body.String(), "OTHER")
		if tc.wantNPM && (isOther || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"charts"`)) {
			t.Errorf("%s %s: got %d %q, want the npm document", tc.format, tc.path, w.Code, w.Body.String())
		}

		if !tc.wantNPM && !isOther {
			t.Errorf("%s %s: got %d %q, want the other format's handler", tc.format, tc.path, w.Code, w.Body.String())
		}
	}
}
