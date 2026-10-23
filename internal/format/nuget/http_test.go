// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/internal/webhook"
)

type nugetDB struct {
	*formattest.DB
	quotaFake

	pkg *models.NuGetPackage

	// createErr, when set, is returned by CreateNuGetPackage.
	createErr error
}

func (d *nugetDB) CreateNuGetPackage(_ context.Context, p *models.NuGetPackage) error {
	if d.createErr != nil {
		return d.createErr
	}

	d.pkg = p
	return nil
}

func (d *nugetDB) GetNuGetPackage(_ context.Context, _, _, _ string) (*models.NuGetPackage, error) {
	return d.pkg, nil
}

func (d *nugetDB) DeleteNuGetPackage(_ context.Context, _, _, _ string) error {
	d.pkg = nil
	return nil
}

func (d *nugetDB) IncrementNuGetDownloadCount(_ context.Context, _, _, _ string) error { return nil }

func newTestHandler(t *testing.T) (*HTTPHandler, *nugetDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &nugetDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func register(h *HTTPHandler) func(chi.Router) { return h.RegisterRepoRoutes }

func TestPushAndDeleteEmitEvents(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("package", "x.nupkg")
	_, _ = fw.Write(createNupkg(t, "Newtonsoft.Json.nuspec", sampleNuspec))
	_ = mw.Close()

	req := formattest.Request(http.MethodPut, "/repos/myrepo/v3/package", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	if w := formattest.Do(register(h), req); w.Code != http.StatusCreated {
		t.Fatalf("push status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "newtonsoft.json" || a["version"] != "13.0.3" {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.pkg.BlobKey]; !ok {
		t.Fatal("blob not stored under the package's BlobKey")
	}

	if w := formattest.Do(register(h), formattest.Request(http.MethodDelete, "/repos/myrepo/v3/package/Newtonsoft.Json/13.0.3", nil)); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.pkg = &models.NuGetPackage{PackageID: "foo", Version: "1.0.0", BlobKey: "abc", PackageSize: 3}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(register(h), formattest.Request(http.MethodGet, "/repos/myrepo/v3/package/foo/1.0.0/foo.1.0.0.nupkg", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["foo.1.0.0.nupkg"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}

func nuspecWithLicense(licenseXML string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<package>
  <metadata>
    <id>Newtonsoft.Json</id>
    <version>13.0.3</version>
    <authors>James Newton-King</authors>
    <description>Json.NET</description>
    ` + licenseXML + `
  </metadata>
</package>`
}

func pushNuspec(t *testing.T, db database.Database, bs *formattest.BlobStore, pub *formattest.Publisher, nuspec string) *httptest.ResponseRecorder {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("package", "x.nupkg")
	_, _ = fw.Write(createNupkg(t, "Newtonsoft.Json.nuspec", nuspec))
	_ = mw.Close()

	req := formattest.Request(http.MethodPut, "/repos/myrepo/v3/package", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return formattest.Do(register(h), req)
}

func assertNupkgStored(t *testing.T, c policytest.Case, status int, db *nugetDB, bs *formattest.BlobStore, pub *formattest.Publisher) {
	t.Helper()

	if c.Stopped() {
		if db.pkg != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
			t.Fatalf("rejected upload stored something: pkg=%v blobs=%d events=%d", db.pkg, len(bs.Blobs), len(pub.Events()))
		}

		return
	}

	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}

	if db.pkg == nil || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
		t.Fatalf("admitted upload not stored: pkg=%v blobs=%d events=%d", db.pkg, len(bs.Blobs), len(pub.Events()))
	}
}

func TestPushLicensePolicy(t *testing.T) {
	nuspec := nuspecWithLicense(`<license type="expression">GPL-3.0</license>`)

	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			inner := &nugetDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := pushNuspec(t, c.DB(inner), bs, pub, nuspec)
			c.AssertResponse(t, w.Code, w.Body.Bytes())
			assertNupkgStored(t, c, w.Code, inner, bs, pub)
		})
	}
}

func TestPushLicensePolicyNoEnforceableLicenseAdmitted(t *testing.T) {
	nuspecs := map[string]string{
		"no license":      nuspecWithLicense(""),
		"licenseUrl only": nuspecWithLicense(`<licenseUrl>https://example.com/eula</licenseUrl>`),
		"license file":    nuspecWithLicense(`<license type="file">LICENSE.txt</license>`),
	}

	for name, nuspec := range nuspecs {
		t.Run(name, func(t *testing.T) {
			c := policytest.Case{
				Want:     policytest.Admitted,
				Policies: []*models.LicensePolicy{policytest.Policy("block", []string{"MIT"}, nil)},
			}
			inner := &nugetDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := pushNuspec(t, c.DB(inner), bs, pub, nuspec)
			c.AssertResponse(t, w.Code, w.Body.Bytes())
			assertNupkgStored(t, c, w.Code, inner, bs, pub)
		})
	}
}

// licenseUrl is deprecated but still what older packages carry: the ones that
// name a license (licenses.nuget.org, canonical URLs) are enforced.
func TestPushLicensePolicyLicenseURL(t *testing.T) {
	urls := map[string]string{
		"licenses.nuget.org": "https://licenses.nuget.org/GPL-3.0-only",
		"canonical gnu.org":  "https://www.gnu.org/licenses/gpl-3.0.html",
	}

	for name, url := range urls {
		for _, c := range policytest.Cases("GPL-3.0-only") {
			t.Run(name+"/"+c.Name, func(t *testing.T) {
				inner := &nugetDB{DB: formattest.NewDB()}
				bs := formattest.NewBlobStore()
				pub := &formattest.Publisher{}

				w := pushNuspec(t, c.DB(inner), bs, pub, nuspecWithLicense(`<licenseUrl>`+url+`</licenseUrl>`))
				c.AssertResponse(t, w.Code, w.Body.Bytes())
				assertNupkgStored(t, c, w.Code, inner, bs, pub)
			})
		}
	}
}

func TestPushRejectsAmbiguousNuspec(t *testing.T) {
	nupkg := archivetest.Zip(t,
		archivetest.File("Newtonsoft.Json.nuspec", nuspecWithLicense(`<license type="expression">MIT</license>`)),
		archivetest.File("Other.nuspec", nuspecWithLicense(`<license type="expression">GPL-3.0-only</license>`)),
	)

	inner := &nugetDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}

	h := NewHTTPHandler(formattest.NewLogger(t), inner, bs, "http://localhost")
	h.SetPublisher(pub)

	w := pushBytes(t, h, nupkg)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}

	if inner.pkg != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
		t.Fatal("rejected push stored something")
	}
}
