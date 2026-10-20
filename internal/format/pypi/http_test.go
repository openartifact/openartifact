// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

type pypiDB struct {
	*formattest.DB

	pkgs []*models.PyPIPackage
}

func (d *pypiDB) CreatePyPIPackage(_ context.Context, p *models.PyPIPackage) error {
	d.pkgs = append(d.pkgs, p)
	return nil
}

func (d *pypiDB) ListPyPIPackages(_ context.Context, _ string) ([]*models.PyPIPackage, error) {
	return d.pkgs, nil
}

func (d *pypiDB) DeletePyPIPackage(_ context.Context, _, _, _, filename string) error {
	for i, p := range d.pkgs {
		if p.Filename == filename {
			d.pkgs = append(d.pkgs[:i], d.pkgs[i+1:]...)
			break
		}
	}

	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *pypiDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &pypiDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func register(h *HTTPHandler) func(chi.Router) { return h.RegisterRepoRoutes }

func uploadRequest(t *testing.T, filename string, data []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", "Demo_Pkg")
	_ = mw.WriteField("version", "1.0")

	fw, err := mw.CreateFormFile("content", filename)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = fw.Write(data)
	_ = mw.Close()

	req := formattest.Request(http.MethodPost, "/repos/myrepo/legacy/", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return req
}

func TestUploadAndDeleteEmitEvents(t *testing.T) {
	h, db, _, pub := newTestHandler(t)

	w := formattest.Do(register(h), uploadRequest(t, "demo-1.0.tar.gz", []byte("payload")))
	if w.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events after upload = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "demo-pkg" || a["version"] != "1.0" || a["path"] != "demo-1.0.tar.gz" {
		t.Errorf("artifact = %v", a)
	}

	if len(db.pkgs) != 1 {
		t.Fatalf("pkgs = %d", len(db.pkgs))
	}

	w = formattest.Do(register(h), formattest.Request(http.MethodDelete, "/repos/myrepo/files/demo-1.0.tar.gz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d", w.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.pkgs = []*models.PyPIPackage{{Name: "demo", Version: "1.0", Filename: "demo-1.0.whl", SHA256Digest: "abc", SizeBytes: 3}}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(register(h), formattest.Request(http.MethodGet, "/repos/myrepo/files/demo-1.0.whl", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["demo-1.0.whl"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}
