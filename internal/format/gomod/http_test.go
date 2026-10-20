// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

const testModule = "github.com/example/mymodule"

type gomodDB struct {
	*formattest.DB

	mod *models.GoModule
}

func (d *gomodDB) CreateGoModule(_ context.Context, m *models.GoModule) error {
	d.mod = m
	return nil
}

func (d *gomodDB) GetGoModule(_ context.Context, _, _, _ string) (*models.GoModule, error) {
	return d.mod, nil
}

func (d *gomodDB) ListGoModuleVersions(_ context.Context, _, _ string) ([]*models.GoModule, error) {
	if d.mod == nil {
		return nil, nil
	}

	return []*models.GoModule{d.mod}, nil
}

func newTestRouter(t *testing.T) (http.Handler, *gomodDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &gomodDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(sub chi.Router) {
		h.RegisterRepoRoutes(sub)
		sub.Put("/*", h.HandlePut)
	})

	return r, db, bs, pub
}

func serve(router http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, formattest.Request(method, target, bytes.NewReader(body)))

	return w
}

func TestUploadEmitsEventAndServesMultiSegmentModule(t *testing.T) {
	router, db, bs, pub := newTestRouter(t)

	zipData := createGoModZip(t, map[string]string{"mymodule@v1.0.0/go.mod": sampleGoMod})

	if w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/v1.0.0", zipData); w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != testModule || a["version"] != "v1.0.0" {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.mod.ZipBlobKey]; !ok {
		t.Fatal("zip blob not stored")
	}

	if w := serve(router, http.MethodGet, "/repos/myrepo/"+testModule+"/@v/list", nil); w.Code != http.StatusOK || w.Body.String() != "v1.0.0" {
		t.Fatalf("list = %d %q", w.Code, w.Body.String())
	}
}

func TestZipQuarantine(t *testing.T) {
	router, db, bs, _ := newTestRouter(t)
	db.mod = &models.GoModule{ModulePath: testModule, Version: "v1.0.0", ZipBlobKey: "abc", ZipSizeBytes: 3, GoModContent: "module x"}
	bs.Blobs["abc"] = []byte("xyz")

	url := "/repos/myrepo/" + testModule + "/@v/v1.0.0.zip"

	if w := serve(router, http.MethodGet, url, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	db.Keys[testModule+"/@v/v1.0.0.zip"] = true

	if w := serve(router, http.MethodGet, url, nil); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
