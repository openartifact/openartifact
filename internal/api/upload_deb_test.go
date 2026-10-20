// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

type debAPIDB struct {
	*formattest.DB

	pkgs      map[string]*models.Component
	reserved  int
	released  int64
	decrCalls int
}

func (d *debAPIDB) ReserveStorageUsage(_ context.Context, _ string, size int64, _ int) (bool, error) {
	d.reserved++
	_ = size

	return true, nil
}

func (d *debAPIDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.decrCalls++
	d.released += size

	return nil
}

func (d *debAPIDB) ArtifactExists(context.Context, string, string) (bool, error) { return false, nil }

func (d *debAPIDB) GetDebianPackageByFilename(_ context.Context, _, filename string) (*models.Component, error) {
	for _, p := range d.pkgs {
		if p.Filename == filename {
			return p, nil
		}
	}

	return nil, nil
}

func (d *debAPIDB) GetOrCreateDebianDistribution(context.Context, string, string) (*models.DebianDistribution, error) {
	return &models.DebianDistribution{ID: "d1"}, nil
}

func (d *debAPIDB) GetOrCreateDebianComponent(context.Context, string, string) (*models.DebianComponent, error) {
	return &models.DebianComponent{ID: "c1"}, nil
}

func (d *debAPIDB) GetOrCreateDebianArchitecture(context.Context, string, string) (*models.DebianArchitecture, error) {
	return &models.DebianArchitecture{ID: "a1"}, nil
}

func (d *debAPIDB) UpsertBlob(_ context.Context, sha string, _ string, _ int64) (string, error) {
	return "blob-" + sha, nil
}

func (d *debAPIDB) CreateDebPackage(_ context.Context, p *models.Component, _, _ string) error {
	p.ID = "pkg-1"
	d.pkgs[p.ID] = p

	return nil
}

func (d *debAPIDB) SetDebControlFields(context.Context, string, string, string, map[string]string) error {
	return nil
}

func (d *debAPIDB) GetDebPackageByID(_ context.Context, id string) (*models.DebPackage, error) {
	p, ok := d.pkgs[id]
	if !ok {
		return nil, nil
	}

	return &models.DebPackage{ID: p.ID, RepositoryID: p.RepositoryID, BlobID: p.ArtifactID, Name: p.Name, Version: p.Version, Filename: p.Filename}, nil
}

func (d *debAPIDB) GetDebBlobInfo(_ context.Context, _ string) (string, int64, error) {
	return "somesha", 1234, nil
}

func (d *debAPIDB) DeleteComponent(_ context.Context, id string) error {
	delete(d.pkgs, id)
	return nil
}

func buildTestDeb(t *testing.T) []byte {
	t.Helper()

	ctrl := "Package: hello\nVersion: 1.0\nArchitecture: amd64\nMaintainer: A <a@b.c>\nInstalled-Size: 10\nDescription: hi\n"

	var tarBuf bytes.Buffer

	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(ctrl))}); err != nil {
		t.Fatal(err)
	}

	_, _ = tw.Write([]byte(ctrl))
	_ = tw.Close()

	var gz bytes.Buffer

	gw := gzip.NewWriter(&gz)
	_, _ = gw.Write(tarBuf.Bytes())
	_ = gw.Close()

	var out bytes.Buffer

	out.WriteString("!<arch>\n")
	fmt.Fprintf(&out, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", "control.tar.gz", "0", "0", "0", "100644", gz.Len())
	out.Write(gz.Bytes())

	if gz.Len()%2 == 1 {
		out.WriteByte('\n')
	}

	return out.Bytes()
}

func TestDebUploadDeleteEmitEventsAndQuota(t *testing.T) {
	db := &debAPIDB{DB: formattest.NewDB(), pkgs: map[string]*models.Component{}}
	db.Repo.Format = "deb"

	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewDebUploadHandler(formattest.NewLogger(t), db, bs, nil)
	h.SetPublisher(pub)

	router := chi.NewRouter()
	h.Register(router)

	data := buildTestDeb(t)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "hello.deb")
	_, _ = fw.Write(data)
	_ = mw.Close()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories/myrepo/debs", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events after upload = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "hello" || a["path"] != "hello_1.0_amd64.deb" {
		t.Errorf("artifact = %v", a)
	}

	if db.reserved != 1 || db.decrCalls != 0 {
		t.Errorf("quota reserve=%d release=%d", db.reserved, db.decrCalls)
	}

	req = httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/repositories/myrepo/debs/pkg-1", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", w.Code, w.Body.String())
	}

	if db.decrCalls != 1 || db.released != 1234 {
		t.Errorf("delete release calls=%d size=%d", db.decrCalls, db.released)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}
}
