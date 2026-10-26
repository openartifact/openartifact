// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// uploadHintRouter mounts the dispatcher on the real /repos/{name} shape; its
// fallthrough answers "format-handler".
func uploadHintRouter(t *testing.T) http.Handler {
	t.Helper()

	repo := func(id, name, format, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: format, RepoType: typ}
	}

	db := &fakeDB{
		repos: map[string]*models.Repository{
			"apt":     repo("1", "apt", "deb", "hosted"),
			"yum":     repo("2", "yum", "rpm", "hosted"),
			"npmrepo": repo("3", "npmrepo", "npm", "hosted"),
			"conda":   repo("4", "conda", "conda", "hosted"),
			"aptmir":  repo("5", "aptmir", "deb", "proxy"),
		},
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, nil)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "format-handler")
		})
	})

	return r
}

func TestWrongUploadPathOfDebAndRPMNamesTheRealEndpoint(t *testing.T) {
	router := uploadHintRouter(t)

	tests := []struct {
		name, method, path string
		wantCode           int
		wantBody           string // substring
	}{
		{"deb POST", http.MethodPost, "/repos/apt/upload", http.StatusNotFound, "POST /api/v1/repositories/apt/debs"},
		{"deb PUT", http.MethodPut, "/repos/apt/upload", http.StatusNotFound, "POST /api/v1/repositories/apt/debs"},
		{"deb trailing slash", http.MethodPost, "/repos/apt/upload/", http.StatusNotFound, "/api/v1/repositories/apt/debs"},
		{"rpm POST", http.MethodPost, "/repos/yum/upload", http.StatusNotFound, "POST /api/v1/repositories/yum/packages"},
		// Only the exact path and only writes are hinted.
		{"deb GET falls through", http.MethodGet, "/repos/apt/upload", http.StatusOK, "format-handler"},
		{"deb deeper path falls through", http.MethodPut, "/repos/apt/upload/x", http.StatusOK, "format-handler"},
		{"deb other path falls through", http.MethodPost, "/repos/apt/uploads", http.StatusOK, "format-handler"},
		// A package named "upload" is legitimate in formats that publish by PUT /<name>.
		{"npm falls through", http.MethodPut, "/repos/npmrepo/upload", http.StatusOK, "format-handler"},
		{"conda falls through", http.MethodPost, "/repos/conda/upload", http.StatusOK, "format-handler"},
		// Proxies keep answering 405 to writes.
		{"deb proxy stays read-only", http.MethodPost, "/repos/aptmir/upload", http.StatusMethodNotAllowed, ""},
		{"unknown repo falls through", http.MethodPost, "/repos/nope/upload", http.StatusOK, "format-handler"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), tt.method, tt.path, nil)
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true}))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d (body %q)", w.Code, tt.wantCode, w.Body.String())
			}

			if !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", w.Body.String(), tt.wantBody)
			}
		})
	}
}
