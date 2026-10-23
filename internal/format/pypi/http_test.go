// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"bytes"
	"context"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/internal/webhook"
)

type pypiDB struct {
	*formattest.DB
	quotaFake

	pkgs      []*models.PyPIPackage
	createErr error
}

func (d *pypiDB) CreatePyPIPackage(_ context.Context, p *models.PyPIPackage) error {
	if d.createErr != nil {
		return d.createErr
	}

	d.pkgs = append(d.pkgs, p)
	return nil
}

func (d *pypiDB) ListPyPIPackages(_ context.Context, _ string) ([]*models.PyPIPackage, error) {
	return d.pkgs, nil
}

func (d *pypiDB) ListPyPIPackageNames(_ context.Context, _ string) ([]string, error) {
	return distinctPyPINames(d.pkgs), nil
}

// distinctPyPINames mirrors the ListPyPIPackageNames query: the distinct stored
// names, sorted.
func distinctPyPINames(pkgs []*models.PyPIPackage) []string {
	names := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		names = append(names, p.Name)
	}

	slices.Sort(names)

	return slices.Compact(names)
}

func (d *pypiDB) GetPyPIPackageByFilename(_ context.Context, _, filename string) (*models.PyPIPackage, error) {
	var found []*models.PyPIPackage

	for _, p := range d.pkgs {
		if p.Filename == filename {
			found = append(found, p)
		}
	}

	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	default:
		return nil, database.ErrAmbiguousPyPIFilename
	}
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

	w := formattest.Do(register(h), uploadRequest(t, "demo_pkg-1.0.tar.gz", []byte("payload")))
	if w.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events after upload = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "demo-pkg" || a["version"] != "1.0" || a["path"] != "demo_pkg-1.0.tar.gz" {
		t.Errorf("artifact = %v", a)
	}

	if len(db.pkgs) != 1 {
		t.Fatalf("pkgs = %d", len(db.pkgs))
	}

	w = formattest.Do(register(h), formattest.Request(http.MethodDelete, "/repos/myrepo/files/demo_pkg-1.0.tar.gz", nil))
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

type pypiField struct{ key, value string }

func uploadWithFields(t *testing.T, db database.Database, bs *formattest.BlobStore, pub *formattest.Publisher, fields ...pypiField) *httptest.ResponseRecorder {
	t.Helper()

	// An sdist whose PKG-INFO declares no license: the form fields are the only
	// license information, as for packages built without license metadata.
	sdist := archivetest.TarGz(t, archivetest.File("demo_pkg-1.0/PKG-INFO", "Metadata-Version: 2.1\nName: Demo_Pkg\nVersion: 1.0\n"))

	return uploadFile(t, db, bs, pub, "demo_pkg-1.0.tar.gz", sdist, fields...)
}

// uploadFile uploads content as filename the way twine does, with the given
// extra form fields (name and version are always sent).
func uploadFile(t *testing.T, db database.Database, bs *formattest.BlobStore, pub *formattest.Publisher, filename string, content []byte, fields ...pypiField) *httptest.ResponseRecorder {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", "Demo_Pkg")
	_ = mw.WriteField("version", "1.0")

	for _, f := range fields {
		_ = mw.WriteField(f.key, f.value)
	}

	fw, err := mw.CreateFormFile("content", filename)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = fw.Write(content)
	_ = mw.Close()

	req := formattest.Request(http.MethodPost, "/repos/myrepo/legacy/", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return formattest.Do(register(h), req)
}

func assertPkgStored(t *testing.T, c policytest.Case, status int, db *pypiDB, bs *formattest.BlobStore, pub *formattest.Publisher) {
	t.Helper()

	if c.Stopped() {
		if len(db.pkgs) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
			t.Fatalf("rejected upload stored something: pkgs=%d blobs=%d events=%d", len(db.pkgs), len(bs.Blobs), len(pub.Events()))
		}

		return
	}

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if len(db.pkgs) != 1 || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
		t.Fatalf("admitted upload not stored: pkgs=%d blobs=%d events=%d", len(db.pkgs), len(bs.Blobs), len(pub.Events()))
	}
}

func TestUploadLicensePolicy(t *testing.T) {
	forms := map[string][]pypiField{
		"license field":      {{"license", "GPL-3.0"}},
		"license expression": {{"license_expression", "GPL-3.0"}},
		"classifier only": {
			{"classifiers", "Programming Language :: Python :: 3"},
			{"classifiers", "License :: OSI Approved :: GNU General Public License v3 (GPLv3)"},
		},
	}

	for formName, fields := range forms {
		for _, c := range policytest.Cases("GPL-3.0") {
			t.Run(formName+"/"+c.Name, func(t *testing.T) {
				inner := &pypiDB{DB: formattest.NewDB()}
				bs := formattest.NewBlobStore()
				pub := &formattest.Publisher{}

				w := uploadWithFields(t, c.DB(inner), bs, pub, fields...)
				c.AssertResponse(t, w.Code, w.Body.Bytes())
				assertPkgStored(t, c, w.Code, inner, bs, pub)
			})
		}
	}
}

func TestUploadLicensePolicyNoEnforceableLicenseAdmitted(t *testing.T) {
	longText := "GNU GENERAL PUBLIC LICENSE Version 3 " + strings.Repeat("blah ", 40)

	forms := map[string][]pypiField{
		"no license":         nil,
		"license text field": {{"license", longText}},
		"multiline license":  {{"license", "GPL-3.0\nsecond line"}},
	}

	for name, fields := range forms {
		t.Run(name, func(t *testing.T) {
			c := policytest.Case{
				Want:     policytest.Admitted,
				Policies: []*models.LicensePolicy{policytest.Policy("block", []string{"MIT"}, nil)},
			}
			inner := &pypiDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := uploadWithFields(t, c.DB(inner), bs, pub, fields...)
			c.AssertResponse(t, w.Code, w.Body.Bytes())
			assertPkgStored(t, c, w.Code, inner, bs, pub)
		})
	}
}

// FMTA-7: PEP 503 normalization collapses runs of separators.
func TestNormalizePyPIName(t *testing.T) {
	for in, want := range map[string]string{
		"Foo_Bar":    "foo-bar",
		"foo__bar":   "foo-bar",
		"foo_.bar":   "foo-bar",
		"Foo-.-Bar":  "foo-bar",
		"zope.Event": "zope-event",
		"plain":      "plain",
	} {
		if got := normalizePyPIName(in); got != want {
			t.Errorf("normalizePyPIName(%q) = %q, want %q", in, got, want)
		}
	}
}

func uploadAs(t *testing.T, h *HTTPHandler, name, version, filename string) int {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", name)
	_ = mw.WriteField("version", version)

	fw, _ := mw.CreateFormFile("content", filename)
	_, _ = fw.Write([]byte("payload"))
	_ = mw.Close()

	req := formattest.Request(http.MethodPost, "/repos/myrepo/legacy/", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return formattest.Do(register(h), req).Code
}

// FMTA-8: empty identity fields and a filename that belongs to another package
// are rejected before anything is stored.
func TestUploadValidatesIdentity(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for _, tc := range []struct {
		name, version, filename string
		want                    int
	}{
		{"", "1.0", "foo-1.0.tar.gz", http.StatusBadRequest},
		{"foo", "", "foo-1.0.tar.gz", http.StatusBadRequest},
		{"aaa", "1.0", "foo-1.0.tar.gz", http.StatusBadRequest}, // shadowing another package's file
		{"foo", "2.0", "foo-1.0.tar.gz", http.StatusBadRequest}, // version mismatch
		{"foo", "1.0", "foo-1.0.whl", http.StatusBadRequest},    // malformed wheel name
		{"My_Pkg", "1.0", "my-pkg-1.0.tar.gz", http.StatusOK},   // sdist, normalized equal
		{"my.pkg", "1.0", "my_pkg-1.0-py3-none-any.whl", http.StatusOK},
		{"foo-bar", "1.0-1", "foo_bar-1.0_1-py3-none-any.whl", http.StatusOK},
	} {
		if got := uploadAs(t, h, tc.name, tc.version, tc.filename); got != tc.want {
			t.Errorf("upload(%q,%q,%q) = %d, want %d", tc.name, tc.version, tc.filename, got, tc.want)
		}
	}

	if len(db.pkgs) != 3 {
		t.Errorf("stored %d packages, want 3", len(db.pkgs))
	}
}

// FMTA-8/9: a file resolves by its own filename, never through the whole
// repository list, and an ambiguous filename is not served arbitrarily.
func TestDownloadAmbiguousFilename(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.pkgs = []*models.PyPIPackage{
		{Name: "aaa", Version: "1.0", Filename: "foo-1.0.tar.gz", SHA256Digest: "a1", SizeBytes: 1},
		{Name: "foo", Version: "1.0", Filename: "foo-1.0.tar.gz", SHA256Digest: "f1", SizeBytes: 1},
	}
	bs.Blobs["a1"], bs.Blobs["f1"] = []byte("A"), []byte("F")

	if c := formattest.Do(register(h), formattest.Request(http.MethodGet, "/repos/myrepo/files/foo-1.0.tar.gz", nil)).Code; c == http.StatusOK {
		t.Errorf("ambiguous filename served with 200")
	}

	if c := formattest.Do(register(h), formattest.Request(http.MethodDelete, "/repos/myrepo/files/foo-1.0.tar.gz", nil)).Code; c != http.StatusConflict {
		t.Errorf("ambiguous delete = %d, want 409", c)
	}

	if len(db.pkgs) != 2 {
		t.Errorf("ambiguous delete removed a package")
	}
}

func TestUploadBodyTooLarge(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	h.maxUpload = 1024

	req := uploadRequest(t, "demo_pkg-1.0.tar.gz", bytes.Repeat([]byte("x"), 4*uploadFormOverhead))

	w := formattest.Do(register(h), req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body.String())
	}

	if len(db.pkgs) != 0 || len(bs.Blobs) != 0 {
		t.Fatalf("oversized upload stored something: pkgs=%d blobs=%d", len(db.pkgs), len(bs.Blobs))
	}
}

func TestUploadFilePartTooLarge(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	h.maxUpload = 8

	// Within the body cap (limit + form overhead) but over the per-file limit.
	w := formattest.Do(register(h), uploadRequest(t, "demo_pkg-1.0.tar.gz", []byte("0123456789abcdef")))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body.String())
	}

	if len(db.pkgs) != 0 || len(bs.Blobs) != 0 {
		t.Fatalf("oversized upload stored something: pkgs=%d blobs=%d", len(db.pkgs), len(bs.Blobs))
	}
}

// A second upload that reuses a filename already held by another
// (name, version) is refused instead of making the file ambiguous.
func TestUploadRejectsUsedFilename(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	if got := uploadAs(t, h, "foo", "1-2", "foo-1-2.tar.gz"); got != http.StatusOK {
		t.Fatalf("first upload = %d, want 200", got)
	}

	if got := uploadAs(t, h, "foo-1", "2", "foo-1-2.tar.gz"); got != http.StatusConflict {
		t.Fatalf("colliding upload = %d, want 409", got)
	}

	if len(db.pkgs) != 1 || len(bs.Blobs) != 1 {
		t.Fatalf("colliding upload stored something: pkgs=%d blobs=%d", len(db.pkgs), len(bs.Blobs))
	}

	w := formattest.Do(register(h), formattest.Request(http.MethodGet, "/repos/myrepo/files/foo-1-2.tar.gz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("download after rejected collision = %d, want 200", w.Code)
	}
}

func TestUploadRejectsAmbiguousExistingFilename(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.pkgs = []*models.PyPIPackage{
		{Name: "foo", Version: "1-2", Filename: "foo-1-2.tar.gz"},
		{Name: "foo-1", Version: "2", Filename: "foo-1-2.tar.gz"},
	}

	if got := uploadAs(t, h, "foo", "1-2", "foo-1-2.tar.gz"); got != http.StatusConflict {
		t.Fatalf("upload over ambiguous filename = %d, want 409", got)
	}
}

func TestDownloadAmbiguousFilenameConflict(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.pkgs = []*models.PyPIPackage{
		{Name: "foo", Version: "1-2", Filename: "foo-1-2.tar.gz", SHA256Digest: "a"},
		{Name: "foo-1", Version: "2", Filename: "foo-1-2.tar.gz", SHA256Digest: "b"},
	}

	w := formattest.Do(register(h), formattest.Request(http.MethodGet, "/repos/myrepo/files/foo-1-2.tar.gz", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestDownloadContentDispositionEscapesFilename(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	name := `evil"; x=y.tar.gz`
	db.pkgs = []*models.PyPIPackage{{Name: "evil", Version: "1.0", Filename: name, SHA256Digest: "abc", SizeBytes: 3}}
	bs.Blobs["abc"] = []byte("xyz")

	req := formattest.Request(http.MethodGet, "/repos/myrepo/files/"+url.PathEscape(name), nil)
	req.URL.RawPath = "" // route on the decoded path so chi hands the raw name to the handler

	w := formattest.Do(register(h), req)
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

const demoWheel = "demo_pkg-1.0-py3-none-any.whl"

func wheelWith(t *testing.T, metadata string) []byte {
	t.Helper()

	return archivetest.Zip(t,
		archivetest.File("demo_pkg/__init__.py", ""),
		archivetest.File("demo_pkg-1.0.dist-info/METADATA", "Metadata-Version: 2.1\nName: Demo_Pkg\nVersion: 1.0\n"+metadata),
	)
}

// A curl upload sends no license fields: policies must still see what the
// wheel itself declares.
func TestUploadLicensePolicyReadsTheArtifact(t *testing.T) {
	wheels := map[string]string{
		"PEP 639 expression": "License-Expression: GPL-3.0-only\n",
		"short License":      "License: GPL-3.0-only\n",
		"classifier":         "Classifier: License :: OSI Approved :: GNU General Public License v3 (GPLv3)\n",
	}

	for name, metadata := range wheels {
		for _, c := range policytest.Cases("GPL-3.0-only") {
			t.Run(name+"/"+c.Name, func(t *testing.T) {
				inner := &pypiDB{DB: formattest.NewDB()}
				bs := formattest.NewBlobStore()
				pub := &formattest.Publisher{}

				w := uploadFile(t, c.DB(inner), bs, pub, demoWheel, wheelWith(t, metadata))
				c.AssertResponse(t, w.Code, w.Body.Bytes())
				assertPkgStored(t, c, w.Code, inner, bs, pub)
			})
		}
	}
}

func TestUploadLicensePolicyArtifactBeatsFormFields(t *testing.T) {
	denyGPL := []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0-only"})}

	tests := []struct {
		name     string
		metadata string
		fields   []pypiField
		want     policytest.Outcome
	}{
		{"form claims MIT, wheel is GPL", "License-Expression: GPL-3.0-only\n", []pypiField{{"license_expression", "MIT"}, {"license", "MIT"}}, policytest.Blocked},
		{"form claims GPL, wheel is MIT", "License-Expression: MIT\n", []pypiField{{"license_expression", "GPL-3.0-only"}}, policytest.Admitted},
		{"wheel declares nothing: the form is the fallback", "", []pypiField{{"license_expression", "GPL-3.0-only"}}, policytest.Blocked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := policytest.Case{Want: tc.want, Policies: denyGPL}
			inner := &pypiDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := uploadFile(t, c.DB(inner), bs, pub, demoWheel, wheelWith(t, tc.metadata), tc.fields...)
			c.AssertResponse(t, w.Code, w.Body.Bytes())
			assertPkgStored(t, c, w.Code, inner, bs, pub)
		})
	}
}

// Django 3.0 declares "License: BSD" and the classifier "BSD License": neither
// names one SPDX license, the raw value is what a policy can match.
func TestUploadLicensePolicyDjangoStyleBSD(t *testing.T) {
	metadata := "License: BSD\nClassifier: License :: OSI Approved :: BSD License\n"

	c := policytest.Case{Want: policytest.Blocked, Policies: []*models.LicensePolicy{policytest.Policy("block", nil, []string{"BSD"})}}
	inner := &pypiDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}

	w := uploadFile(t, c.DB(inner), bs, pub, demoWheel, wheelWith(t, metadata))
	c.AssertResponse(t, w.Code, w.Body.Bytes())
	assertPkgStored(t, c, w.Code, inner, bs, pub)
}

func TestUploadSdistLicenseFromPKGINFO(t *testing.T) {
	sdist := archivetest.TarGz(t, archivetest.File("demo_pkg-1.0/PKG-INFO", "Metadata-Version: 2.1\nName: Demo_Pkg\nVersion: 1.0\nLicense: GPL-3.0-only\n"))

	c := policytest.Case{Want: policytest.Blocked, Policies: []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0-only"})}}
	inner := &pypiDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}

	w := uploadFile(t, c.DB(inner), bs, pub, "demo_pkg-1.0.tar.gz", sdist, pypiField{"license", "MIT"})
	c.AssertResponse(t, w.Code, w.Body.Bytes())
	assertPkgStored(t, c, w.Code, inner, bs, pub)
}

func TestUploadRejectsUnsafeArchives(t *testing.T) {
	const header = "Metadata-Version: 2.1\nName: Demo_Pkg\nVersion: 1.0\n"

	archives := map[string][]byte{
		"zip bomb METADATA": archivetest.Zip(t, archivetest.File("demo_pkg-1.0.dist-info/METADATA", header+"Description: "+strings.Repeat("A", 8<<20))),
		"two METADATA files": archivetest.Zip(t,
			archivetest.File("demo_pkg-1.0.dist-info/METADATA", header+"License: MIT\n"),
			archivetest.File("other-1.0.dist-info/METADATA", header+"License: GPL-3.0-only\n")),
	}

	for name, wheel := range archives {
		t.Run(name, func(t *testing.T) {
			inner := &pypiDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := uploadFile(t, inner, bs, pub, demoWheel, wheel, pypiField{"license", "MIT"})
			if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "LICENSE_METADATA_UNSAFE") {
				t.Fatalf("want 422 LICENSE_METADATA_UNSAFE, got %d: %s", w.Code, w.Body.String())
			}

			if len(inner.pkgs) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
				t.Fatal("rejected upload stored something")
			}
		})
	}
}

// A file whose licenses cannot be derived (a type that carries no readable
// metadata, a damaged archive, a wheel without METADATA) is judged on the
// client's form fields only when no blocking policy applies; with one, the
// upload is refused whatever the form claims.
func TestUploadUnderivableLicenses(t *testing.T) {
	files := map[string]struct {
		filename string
		content  []byte
	}{
		"egg":                    {"demo_pkg-1.0.egg", []byte("egg")},
		"windows installer":      {"demo_pkg-1.0.exe", []byte("MZ")},
		"wheel that is no zip":   {demoWheel, []byte("not a zip")},
		"wheel without METADATA": {demoWheel, archivetest.Zip(t, archivetest.File("demo_pkg/__init__.py", ""))},
	}

	for name, f := range files {
		for _, c := range policytest.UnderivableCases() {
			t.Run(name+"/"+c.Name, func(t *testing.T) {
				inner := &pypiDB{DB: formattest.NewDB()}
				bs := formattest.NewBlobStore()
				pub := &formattest.Publisher{}

				w := uploadFile(t, c.DB(inner), bs, pub, f.filename, f.content, pypiField{"license", "MIT"})
				c.AssertResponse(t, w.Code, w.Body.Bytes())
				assertPkgStored(t, c, w.Code, inner, bs, pub)
			})
		}
	}
}
