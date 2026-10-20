// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

type condaDB struct {
	*formattest.DB

	pkg *models.CondaPackage
}

func (d *condaDB) CreateCondaPackage(_ context.Context, p *models.CondaPackage) error {
	p.ID = "pkg-1"
	d.pkg = p

	return nil
}

func (d *condaDB) GetCondaPackage(_ context.Context, _, _, _, _, _ string) (*models.CondaPackage, error) {
	return d.pkg, nil
}

func (d *condaDB) DeleteCondaPackage(_ context.Context, _ string) error {
	d.pkg = nil
	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *condaDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &condaDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func TestUploadAndDeleteEmitEvents(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	w := formattest.Do(h.RegisterRepoRoutes,
		formattest.Request(http.MethodPost, "/repos/myrepo/linux-64?filename=demo-1.0-py_0.tar.bz2", strings.NewReader("data")))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "demo" || a["version"] != "1.0" || a["path"] != "linux-64/demo-1.0-py_0.tar.bz2" {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.pkg.SHA256]; !ok {
		t.Fatal("blob not stored")
	}

	w = formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/linux-64/demo-1.0-py_0.tar.bz2", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}

	if len(bs.Blobs) != 0 {
		t.Error("blob not removed after delete")
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.pkg = &models.CondaPackage{ID: "p", Name: "demo", Version: "1.0", BuildString: "py_0", BlobKey: "abc", SHA256: "abc", SizeBytes: 3}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/linux-64/demo-1.0-py_0.tar.bz2", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["linux-64/demo-1.0-py_0.tar.bz2"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}
