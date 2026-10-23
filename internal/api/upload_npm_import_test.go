// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/middleware"
)

// npmPublishDB is the in-memory slice of the database an npm publish touches.
type npmPublishDB struct {
	*formattest.DB

	versions []*models.NPMVersion
}

func (d *npmPublishDB) ReserveStorageUsage(context.Context, string, int64, int) (bool, error) {
	return true, nil
}

func (d *npmPublishDB) DecrementStorageUsage(context.Context, string, int64, int) error { return nil }

func (d *npmPublishDB) GetNPMPackageByName(context.Context, string, string) (*models.NPMPackage, error) {
	return nil, nil
}

func (d *npmPublishDB) CreateNPMPackage(_ context.Context, p *models.NPMPackage) (*models.NPMPackage, error) {
	p.ID = "pkg-1"

	return p, nil
}

func (d *npmPublishDB) UpsertBlob(_ context.Context, sha string, _ string, _ int64) (string, error) {
	return "blob-" + sha, nil
}

func (d *npmPublishDB) CreateNPMVersion(_ context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	d.versions = append(d.versions, v)

	return v, nil
}

func (d *npmPublishDB) UpsertNPMDistTag(_ context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	return t, nil
}

func (d *npmPublishDB) MarkPackumentStale(context.Context, string) error { return nil }

// TestNPMPublishHonorsImportCreatedAt: a publish carrying an origin creation
// time (as importmeta resolves it for a manage principal) stores it as the
// npm_versions row's created_at; an ordinary publish leaves it unset so the
// database stamps now.
func TestNPMPublishHonorsImportCreatedAt(t *testing.T) {
	origin := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)

	tests := []struct {
		name string
		ctx  context.Context //nolint:containedctx // test table carries the request context
		want time.Time
	}{
		{"imported", importmeta.WithCreatedAt(context.Background(), origin), origin},
		{"normal upload", context.Background(), time.Time{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &npmPublishDB{DB: formattest.NewDB()}
			h := NewNPMUploadHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost", nil)

			payload := []byte("tarball-bytes")
			body := npmBody("demo", "1.0.0", base64.StdEncoding.EncodeToString(payload), len(payload), "data-middle")

			ctx := middleware.WithPrincipal(tc.ctx, &auth.Principal{Username: "alice"})
			req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/repos/myrepo/demo", strings.NewReader(body))

			w := httptest.NewRecorder()

			router := chi.NewRouter()
			h.Register(router)
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK && w.Code != http.StatusCreated {
				t.Fatalf("publish status = %d: %s", w.Code, w.Body.String())
			}

			if len(db.versions) != 1 || !db.versions[0].CreatedAt.Equal(tc.want) {
				t.Fatalf("versions = %+v, want created_at %v", db.versions, tc.want)
			}
		})
	}
}
