// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

const filesURL = "/repos/myrepo/v2/conans/zlib/1.3/_/_/revisions/0/files/conan_package.tgz"

type conanDB struct {
	*formattest.DB

	pkg *models.ConanPackage
}

func (d *conanDB) CreateConanPackage(_ context.Context, p *models.ConanPackage) error {
	p.ID = "pkg-1"
	d.pkg = p

	return nil
}

func (d *conanDB) GetConanPackage(_ context.Context, _, _, _, _, _ string) (*models.ConanPackage, error) {
	return d.pkg, nil
}

func (d *conanDB) DeleteConanPackage(_ context.Context, _ string) error {
	d.pkg = nil
	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *conanDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &conanDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func TestUploadAndDeleteEmitEvents(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, filesURL, strings.NewReader("data")))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "zlib" || a["version"] != "1.3" || a["path"] != "zlib/1.3/_/_/conan_package.tgz" {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.pkg.BlobKey]; !ok {
		t.Fatal("blob not stored")
	}

	w = formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/v2/conans/zlib/1.3/_/_", nil))
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
	db.pkg = &models.ConanPackage{ID: "p", Name: "zlib", BlobKey: "abc"}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, filesURL, nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["zlib/1.3/_/_/conan_package.tgz"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}
