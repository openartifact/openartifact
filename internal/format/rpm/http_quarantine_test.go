// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type quarantineDB struct {
	databasetest.BaseMockDB

	quarantined map[string]bool // artifact key -> quarantined
}

func (d *quarantineDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: "repo-1", Name: name, Format: "rpm"}, nil
}

func (d *quarantineDB) GetComponentByFilename(_ context.Context, repoID, filename string) (*models.Component, error) {
	if repoID != "repo-1" || filename != "hello-1.0-1.x86_64.rpm" {
		return nil, nil
	}

	return &models.Component{ID: "pkg-uuid-1", RepositoryID: repoID, SHA256: strings.Repeat("a", 64), Filename: filename}, nil
}

func (d *quarantineDB) IsArtifactQuarantined(_ context.Context, repoID string, keys []string) (bool, error) {
	if repoID != "repo-1" {
		return false, nil
	}

	for _, k := range keys {
		if d.quarantined[k] {
			return true, nil
		}
	}

	return false, nil
}

type fakeBlobs struct{ storage.BlobStore }

func (fakeBlobs) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("rpm-bytes")), nil
}

func TestPackageDownloadQuarantine(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	const path = "/repos/myrepo/Packages/hello-1.0-1.x86_64.rpm"

	tests := []struct {
		name        string
		quarantined map[string]bool
		wantCode    int
	}{
		{"not quarantined", nil, http.StatusOK},
		{"by package row id", map[string]bool{"pkg-uuid-1": true}, http.StatusForbidden},
		{"by repo-relative path", map[string]bool{"Packages/hello-1.0-1.x86_64.rpm": true}, http.StatusForbidden},
		{"by filename", map[string]bool{"hello-1.0-1.x86_64.rpm": true}, http.StatusForbidden},
		{"by request path", map[string]bool{path: true}, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHTTPHandler(log, &quarantineDB{quarantined: tt.quarantined}, fakeBlobs{})

			router := chi.NewRouter()
			router.Route("/repos/{name}", h.RegisterRepoRoutes)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body.String())
			}

			if tt.wantCode == http.StatusForbidden && !strings.Contains(rec.Body.String(), "ARTIFACT_QUARANTINED") {
				t.Errorf("body %q lacks ARTIFACT_QUARANTINED", rec.Body.String())
			}

			if tt.wantCode == http.StatusOK && rec.Body.String() != "rpm-bytes" {
				t.Errorf("body = %q", rec.Body.String())
			}
		})
	}
}

func TestPackageDownloadMissingPackageNotLeaked(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	// A quarantined key for a package that does not exist must still be 404.
	h := NewHTTPHandler(log, &quarantineDB{quarantined: map[string]bool{"Packages/nope.rpm": true}}, fakeBlobs{})

	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/Packages/nope.rpm", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
