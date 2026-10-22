// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

// condaDB is an in-memory conda_packages table: lookups match on the full
// file key (name, version, build, platform, extension), like the real
// queries, and creating a key twice yields database.ErrDuplicate.
type condaDB struct {
	*formattest.DB
	quotaFake

	// pkg is the most recently stored package, nil after it was deleted.
	pkg  *models.CondaPackage
	pkgs []*models.CondaPackage
}

func (d *condaDB) find(name, version, build, platform, ext string) *models.CondaPackage {
	for _, p := range d.pkgs {
		if p.Name == name && p.Version == version && p.BuildString == build && p.Platform == platform && p.Extension == ext {
			return p
		}
	}

	return nil
}

func (d *condaDB) CreateCondaPackage(_ context.Context, p *models.CondaPackage) error {
	if p.Extension == "" {
		p.Extension = models.CondaExtTarBz2
	}

	if d.find(p.Name, p.Version, p.BuildString, p.Platform, p.Extension) != nil {
		return database.ErrDuplicate
	}

	p.ID = fmt.Sprintf("pkg-%d", len(d.pkgs)+1)
	d.pkg = p
	d.pkgs = append(d.pkgs, p)

	return nil
}

func (d *condaDB) GetCondaPackageFile(_ context.Context, _, name, version, build, platform, ext string) (*models.CondaPackage, error) {
	return d.find(name, version, build, platform, ext), nil
}

func (d *condaDB) ListCondaPackages(_ context.Context, _, platform string) ([]*models.CondaPackage, error) {
	var out []*models.CondaPackage

	for _, p := range d.pkgs {
		if p.Platform == platform {
			out = append(out, p)
		}
	}

	return out, nil
}

func (d *condaDB) CondaRepodataStamp(_ context.Context, _, platform string) (string, error) {
	var b strings.Builder

	for _, p := range d.pkgs {
		if p.Platform == platform {
			b.WriteString(p.ID + p.Filename() + ";")
		}
	}

	return b.String(), nil
}

func (d *condaDB) DeleteCondaPackage(_ context.Context, id string) error {
	d.pkgs = slices.DeleteFunc(d.pkgs, func(p *models.CondaPackage) bool { return p.ID == id })

	if d.pkg != nil && d.pkg.ID == id {
		d.pkg = nil
	}

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
	db.pkgs = []*models.CondaPackage{{ID: "p", Name: "demo", Version: "1.0", BuildString: "py_0", Platform: "linux-64", Extension: models.CondaExtTarBz2, BlobKey: "abc", SHA256: "abc", SizeBytes: 3}}
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

func TestDownloadContentDispositionEscapesFilename(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.pkgs = []*models.CondaPackage{{ID: "p", Name: `ev"il;x=y`, Version: "1.0", BuildString: "py_0", Platform: "linux-64", Extension: models.CondaExtTarBz2, BlobKey: "abc", SHA256: "abc", SizeBytes: 3}}
	bs.Blobs["abc"] = []byte("xyz")

	name := `ev"il;x=y-1.0-py_0.tar.bz2`
	req := formattest.Request(http.MethodGet, "/repos/myrepo/linux-64/"+url.PathEscape(name), nil)
	req.URL.RawPath = "" // route on the decoded path so chi hands the raw name to the handler

	w := formattest.Do(h.RegisterRepoRoutes, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	disp, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("Content-Disposition %q: %v", w.Header().Get("Content-Disposition"), err)
	}

	if disp != "attachment" || params["filename"] != name || len(params) != 1 {
		t.Fatalf("disposition = %q params = %v", disp, params)
	}
}
