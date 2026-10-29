// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
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

	return &models.DebPackage{
		ID:           p.ID,
		RepositoryID: p.RepositoryID,
		BlobID:       p.ArtifactID,
		Name:         p.Name,
		Version:      p.Version,
		Filename:     p.Filename,
	}, nil
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

	return buildTestDebWithLicense(t, "")
}

// buildTestDebWithLicense builds a deb whose control stanza carries a
// `License:` field (left out when license is empty).
func buildTestDebWithLicense(t *testing.T, license string) []byte {
	t.Helper()

	ctrl := "Package: hello\nVersion: 1.0\nArchitecture: amd64\nMaintainer: A <a@b.c>\nInstalled-Size: 10\n"
	if license != "" {
		ctrl += "License: " + license + "\n"
	}

	ctrl += "Description: hi\n"

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

// TestDebUploadImportCreatedAt checks that an authorized origin timestamp is
// stored on the package row and that the response names the new package id,
// while an upload without one leaves the stamp to the database.
func TestDebUploadImportCreatedAt(t *testing.T) {
	origin := time.Date(2019, 5, 6, 7, 8, 9, 0, time.UTC)

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
			db := &debAPIDB{DB: formattest.NewDB(), pkgs: map[string]*models.Component{}}
			db.Repo.Format = "deb"

			h := NewDebUploadHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), nil)
			router := chi.NewRouter()
			h.Register(router)

			var body bytes.Buffer

			mw := multipart.NewWriter(&body)
			fw, _ := mw.CreateFormFile("file", "hello.deb")
			_, _ = fw.Write(buildTestDeb(t))
			_ = mw.Close()

			req := httptest.NewRequestWithContext(tc.ctx, http.MethodPost, "/api/v1/repositories/myrepo/debs", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
			}

			if got := db.pkgs["pkg-1"].CreatedAt; !got.Equal(tc.want) {
				t.Errorf("created_at = %v, want %v", got, tc.want)
			}

			var resp struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Data.ID != "pkg-1" {
				t.Errorf("response id = %q (err %v), body %s", resp.Data.ID, err, w.Body.String())
			}
		})
	}
}

// uploadDebWithLicenseStore is the part of the database uploadDebWithLicense uses needs.
type uploadDebWithLicenseStore interface {
	DebUploadStore
}

func uploadDebWithLicense(
	t *testing.T,
	db uploadDebWithLicenseStore,
	bs *formattest.BlobStore,
	pub *formattest.Publisher,
	license string,
) *httptest.ResponseRecorder {
	t.Helper()

	h := NewDebUploadHandler(formattest.NewLogger(t), db, bs, nil)
	h.SetPublisher(pub)

	router := chi.NewRouter()
	h.Register(router)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "hello.deb")
	_, _ = fw.Write(buildTestDebWithLicense(t, license))
	_ = mw.Close()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories/myrepo/debs", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w
}

func TestDebUploadLicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			db := &debAPIDB{DB: formattest.NewDB(), pkgs: map[string]*models.Component{}}
			db.Repo.Format = "deb"

			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := uploadDebWithLicense(t, c.DB(db), bs, pub, "GPL-3.0")
			c.AssertResponse(t, w.Code, w.Body.Bytes())

			if c.Stopped() {
				if len(db.pkgs) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.reserved != 0 {
					t.Errorf(
						"rejected upload stored data: pkgs=%d blobs=%d events=%d reserved=%d",
						len(db.pkgs),
						len(bs.Blobs),
						len(pub.Events()),
						db.reserved,
					)
				}

				return
			}

			if w.Code != http.StatusOK {
				t.Fatalf("admitted upload status = %d: %s", w.Code, w.Body.String())
			}

			if len(db.pkgs) != 1 || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
				t.Errorf("admitted upload not stored: pkgs=%d blobs=%d events=%d", len(db.pkgs), len(bs.Blobs), len(pub.Events()))
			}
		})
	}
}

func TestDebUploadNoLicenseAdmittedUnderBlockPolicy(t *testing.T) {
	db := &debAPIDB{DB: formattest.NewDB(), pkgs: map[string]*models.Component{}}
	db.Repo.Format = "deb"

	c := policytest.Case{Policies: []*models.LicensePolicy{policytest.Policy(licensepolicy.ActionBlock, []string{"MIT"}, nil)}}

	w := uploadDebWithLicense(t, c.DB(db), formattest.NewBlobStore(), &formattest.Publisher{}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	if len(db.pkgs) != 1 {
		t.Errorf("unlicensed package not stored")
	}
}
