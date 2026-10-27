// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/gpg"
)

// pacmanDB is an in-memory pacman_packages table with the unique live file name.
type pacmanDB struct {
	*formattest.DB

	pkgs map[string]*models.PacmanPackage
	meta map[string]*models.RepoMetadata

	limit, used int64
}

func (d *pacmanDB) ReserveStorageUsage(_ context.Context, _ string, size int64, _ int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size

	return true, nil
}

func (d *pacmanDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.used -= size
	return nil
}

func (d *pacmanDB) CreatePacmanPackage(_ context.Context, p *models.PacmanPackage) error {
	if d.pkgs[p.Filename] != nil {
		return database.ErrDuplicate
	}

	p.ID = "pkg-" + p.Filename
	d.pkgs[p.Filename] = p

	return nil
}

func (d *pacmanDB) GetPacmanPackageByFilename(_ context.Context, _, filename string) (*models.PacmanPackage, error) {
	return d.pkgs[filename], nil
}

func (d *pacmanDB) SetPacmanSignature(_ context.Context, _, id, sig, source, key string) error {
	for _, p := range d.pkgs {
		if p.ID == id {
			p.Signature, p.SignatureSource, p.SignatureKey = sig, source, key
		}
	}

	return nil
}

func (d *pacmanDB) DeletePacmanPackage(_ context.Context, _, id string) error {
	for name, p := range d.pkgs {
		if p.ID == id {
			delete(d.pkgs, name)
		}
	}

	return nil
}

func (d *pacmanDB) GetRepoMetadataByPath(_ context.Context, _, path string) (*models.RepoMetadata, error) {
	return d.meta[path], nil
}

type regenRecorder struct{ repos []string }

func (r *regenRecorder) NotifyStale(repoID string) { r.repos = append(r.repos, repoID) }

// newTestDB is newTestHandler for tests that need the handler and its table only.
func newTestDB(t *testing.T) (*HTTPHandler, *pacmanDB) {
	t.Helper()

	h, db, _, _, _ := newTestHandler(t) //nolint:dogsled // the other fixtures are not needed

	return h, db
}

func newTestHandler(t *testing.T) (*HTTPHandler, *pacmanDB, *formattest.BlobStore, *formattest.Publisher, *regenRecorder) {
	t.Helper()

	db := &pacmanDB{DB: formattest.NewDB(), pkgs: map[string]*models.PacmanPackage{}, meta: map[string]*models.RepoMetadata{}}
	db.Repo.Format = FormatName

	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	regen := &regenRecorder{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)
	h.SetRegenerator(regen)

	return h, db, bs, pub, regen
}

// buildPackage returns a gzip-compressed package with a .PKGINFO and one file.
func buildPackage(t *testing.T, name, version, arch string) []byte {
	t.Helper()

	info := "pkgname = " + name + "\npkgbase = " + name + "\npkgver = " + version + "\npkgdesc = demo\narch = " + arch +
		"\nlicense = MIT\ndepend = glibc\nsize = 10\nbuilddate = 1700000000\n"

	var tarBuf bytes.Buffer

	tw := tar.NewWriter(&tarBuf)

	for _, m := range []struct{ name, body string }{{".PKGINFO", info}, {"usr/bin/" + name, "#!/bin/sh\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	zw := gzip.NewWriter(&out)
	_, _ = zw.Write(tarBuf.Bytes())

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return out.Bytes()
}

func do(h *HTTPHandler, method, target string, body []byte, headers ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = formattest.Request(method, target, nil)
	} else {
		req = formattest.Request(method, target, bytes.NewReader(body))
	}

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	return formattest.Do(h.RegisterRepoRoutes, req)
}

const hello = "hello-1:2.0-1-x86_64.pkg.tar.gz"

//nolint:gocyclo,cyclop // one scenario: upload, download, resume, conflict, delete
func TestUploadDownloadDelete(t *testing.T) {
	h, db, bs, pub, regen := newTestHandler(t)
	pkg := buildPackage(t, "hello", "1:2.0-1", "x86_64")

	w := do(h, http.MethodPut, "/repos/myrepo/os/x86_64/"+hello, pkg)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	row := db.pkgs[hello]
	if row == nil || row.Name != "hello" || row.Version != "1:2.0-1" || row.RepoArch != "x86_64" || row.MD5 == "" {
		t.Fatalf("row = %+v", row)
	}

	if row.FileList == nil || *row.FileList != "usr/bin/hello" {
		t.Errorf("file list = %v", row.FileList)
	}

	if len(regen.repos) != 1 {
		t.Errorf("regeneration requests = %v", regen.repos)
	}

	if evs := pub.Events(); len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded ||
		evs[0].Artifact()["path"] != "os/x86_64/"+hello {
		t.Errorf("events = %+v", evs)
	}

	if db.used != int64(len(pkg)) {
		t.Errorf("quota used = %d, want %d", db.used, len(pkg))
	}

	// The colon of the epoch arrives percent-encoded from pacman.
	w = do(h, http.MethodGet, "/repos/myrepo/os/x86_64/hello-1%3A2.0-1-x86_64.pkg.tar.gz", nil)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), pkg) {
		t.Fatalf("download status = %d, %d bytes", w.Code, w.Body.Len())
	}

	// A resumed download.
	w = do(h, http.MethodGet, "/repos/myrepo/os/x86_64/"+hello, nil, "Range", "bytes=10-")
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), pkg[10:]) {
		t.Fatalf("range status = %d, %d bytes", w.Code, w.Body.Len())
	}

	if cr := w.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 10-") {
		t.Errorf("Content-Range = %q", cr)
	}

	if w = do(
		h,
		http.MethodGet,
		"/repos/myrepo/os/x86_64/"+hello,
		nil,
		"Range",
		"bytes=999999-",
	); w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("range beyond the end = %d", w.Code)
	}

	if w = do(h, http.MethodPut, "/repos/myrepo/os/x86_64/"+hello, pkg); w.Code != http.StatusConflict {
		t.Errorf("second upload = %d, want 409", w.Code)
	}

	if w = do(h, http.MethodDelete, "/repos/myrepo/os/x86_64/"+hello, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}

	if len(db.pkgs) != 0 || len(bs.Blobs) != 0 || db.used != 0 {
		t.Errorf("after delete: rows=%d blobs=%d quota=%d", len(db.pkgs), len(bs.Blobs), db.used)
	}

	if len(regen.repos) != 2 {
		t.Errorf("regeneration requests = %v", regen.repos)
	}
}

func TestUploadValidation(t *testing.T) {
	h, db := newTestDB(t)
	pkg := buildPackage(t, "hello", "1:2.0-1", "x86_64")
	anyPkg := buildPackage(t, "docs", "1.0-1", "any")

	for _, tc := range []struct {
		name, path string
		body       []byte
		want       int
	}{
		{"not a package", "/os/x86_64/" + hello, []byte("junk"), http.StatusBadRequest},
		{"arch directory mismatch", "/os/aarch64/" + hello, pkg, http.StatusBadRequest},
		{"file name mismatch", "/os/x86_64/hello-9-1-x86_64.pkg.tar.gz", pkg, http.StatusBadRequest},
		{"compression mismatch", "/os/x86_64/hello-1:2.0-1-x86_64.pkg.tar.xz", pkg, http.StatusBadRequest},
		{"database upload", "/os/x86_64/myrepo.db", pkg, http.StatusBadRequest},
		{"invalid architecture", "/os/x86_64!/" + hello, pkg, http.StatusBadRequest},
		{"any directory", "/os/any/docs-1.0-1-any.pkg.tar.gz", anyPkg, http.StatusBadRequest},
		{"any package in an architecture directory", "/os/x86_64/docs-1.0-1-any.pkg.tar.gz", anyPkg, http.StatusCreated},
		{"hyphen spelling of the epoch", "/os/x86_64/hello-1-2.0-1-x86_64.pkg.tar.gz", pkg, http.StatusCreated},
	} {
		if w := do(h, http.MethodPut, "/repos/myrepo"+tc.path, tc.body); w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, w.Code, tc.want, strings.TrimSpace(w.Body.String()))
		}
	}

	if len(db.pkgs) != 2 {
		t.Errorf("stored %d packages, want 2", len(db.pkgs))
	}

	// An any package is served under every architecture directory.
	if w := do(h, http.MethodGet, "/repos/myrepo/os/aarch64/docs-1.0-1-any.pkg.tar.gz", nil); w.Code != http.StatusOK {
		t.Errorf("any package under aarch64 = %d", w.Code)
	}

	// An x86_64 package is not.
	if w := do(h, http.MethodGet, "/repos/myrepo/os/aarch64/hello-1-2.0-1-x86_64.pkg.tar.gz", nil); w.Code != http.StatusNotFound {
		t.Errorf("x86_64 package under aarch64 = %d", w.Code)
	}
}

func TestOtherFormatIsNotFound(t *testing.T) {
	h, db := newTestDB(t)
	db.Repo.Format = "conda"

	if w := do(
		h,
		http.MethodPut,
		"/repos/myrepo/os/x86_64/"+hello,
		buildPackage(t, "hello", "1:2.0-1", "x86_64"),
	); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestQuotaRefusal(t *testing.T) {
	h, db, bs, _, _ := newTestHandler(t)
	db.limit = 10

	if w := do(
		h,
		http.MethodPut,
		"/repos/myrepo/os/x86_64/"+hello,
		buildPackage(t, "hello", "1:2.0-1", "x86_64"),
	); w.Code == http.StatusCreated {
		t.Errorf("upload over the quota accepted")
	}

	if len(db.pkgs) != 0 || len(bs.Blobs) != 0 || db.used != 0 {
		t.Errorf("rows=%d blobs=%d used=%d", len(db.pkgs), len(bs.Blobs), db.used)
	}
}

func TestSignatureUploadAndDelete(t *testing.T) {
	h, db, _, _, regen := newTestHandler(t)
	pkg := buildPackage(t, "hello", "1:2.0-1", "x86_64")

	if w := do(h, http.MethodPut, "/repos/myrepo/os/x86_64/"+hello+".sig", []byte("x")); w.Code != http.StatusNotFound {
		t.Fatalf("signature of an unknown package = %d", w.Code)
	}

	do(h, http.MethodPut, "/repos/myrepo/os/x86_64/"+hello, pkg)

	if w := do(h, http.MethodGet, "/repos/myrepo/os/x86_64/"+hello+".sig", nil); w.Code != http.StatusNotFound {
		t.Errorf("signature of an unsigned package = %d", w.Code)
	}

	if w := do(h, http.MethodPut, "/repos/myrepo/os/x86_64/"+hello+".sig", []byte("junk")); w.Code != http.StatusBadRequest {
		t.Errorf("junk signature = %d", w.Code)
	}

	entity, err := gpg.GenerateEntity("t", "", "t@example.org", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := gpg.NewLocalSignerFromEntity(entity)
	if err != nil {
		t.Fatal(err)
	}

	armored, err := signer.DetachedSign(bytes.NewReader(pkg))
	if err != nil {
		t.Fatal(err)
	}

	// An armored signature is stored, and served, in binary form.
	if w := do(h, http.MethodPut, "/repos/myrepo/os/x86_64/"+hello+".sig", armored); w.Code != http.StatusCreated {
		t.Fatalf("signature upload = %d: %s", w.Code, w.Body.String())
	}

	row := db.pkgs[hello]
	if row.SignatureSource != models.PacmanSigUploader {
		t.Errorf("source = %q", row.SignatureSource)
	}

	got, err := base64.StdEncoding.DecodeString(row.Signature)
	if err != nil || len(got) == 0 || got[0]&0x80 == 0 {
		t.Fatalf("stored signature is not binary OpenPGP: %v", err)
	}

	w := do(h, http.MethodGet, "/repos/myrepo/os/x86_64/"+hello+".sig", nil)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), got) || w.Header().Get("Content-Type") != "application/pgp-signature" {
		t.Errorf("signature download = %d", w.Code)
	}

	if w := do(h, http.MethodDelete, "/repos/myrepo/os/x86_64/"+hello+".sig", nil); w.Code != http.StatusNoContent {
		t.Errorf("signature delete = %d", w.Code)
	}

	if row.Signature != "" || len(regen.repos) < 3 {
		t.Errorf("signature %q after delete, regeneration requests %v", row.Signature, regen.repos)
	}

	// A signature the server made is not the uploader's to delete.
	row.Signature, row.SignatureSource = "AAAA", models.PacmanSigServer
	if w := do(h, http.MethodDelete, "/repos/myrepo/os/x86_64/"+hello+".sig", nil); w.Code != http.StatusNotFound {
		t.Errorf("deleting a server signature = %d", w.Code)
	}
}

func TestServeDatabases(t *testing.T) {
	h, db, bs, _, _ := newTestHandler(t)
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	bs.Blobs["dbsha"] = []byte("db bytes")
	bs.Blobs["sigsha"] = []byte("sig bytes")
	db.meta["os/x86_64/db.tar.gz"] = &models.RepoMetadata{BlobSHA256: "dbsha", LastGenerated: modified}
	db.meta["os/x86_64/db.tar.gz.sig"] = &models.RepoMetadata{BlobSHA256: "sigsha", LastGenerated: modified}

	for _, name := range []string{"myrepo.db", "myrepo.db.tar.gz", "anything.db"} {
		w := do(h, http.MethodGet, "/repos/myrepo/os/x86_64/"+name, nil)
		if w.Code != http.StatusOK || w.Body.String() != "db bytes" || w.Header().Get("Last-Modified") == "" {
			t.Errorf("%s = %d %q", name, w.Code, w.Body.String())
		}
	}

	if w := do(h, http.MethodGet, "/repos/myrepo/os/x86_64/myrepo.db.sig", nil); w.Code != http.StatusOK || w.Body.String() != "sig bytes" {
		t.Errorf("db.sig = %d %q", w.Code, w.Body.String())
	}

	// pacman revalidates with If-Modified-Since.
	w := do(h, http.MethodGet, "/repos/myrepo/os/x86_64/myrepo.db", nil, "If-Modified-Since", modified.Format(http.TimeFormat))
	if w.Code != http.StatusNotModified {
		t.Errorf("If-Modified-Since at the generation time = %d", w.Code)
	}

	w = do(
		h,
		http.MethodGet,
		"/repos/myrepo/os/x86_64/myrepo.db",
		nil,
		"If-Modified-Since",
		modified.Add(-time.Hour).Format(http.TimeFormat),
	)
	if w.Code != http.StatusOK {
		t.Errorf("If-Modified-Since before the generation time = %d", w.Code)
	}

	// No database for an architecture without packages, no files database yet.
	for _, path := range []string{"/os/aarch64/myrepo.db", "/os/x86_64/myrepo.files", "/os/x86_64/myrepo.files.sig"} {
		if w := do(h, http.MethodGet, "/repos/myrepo"+path, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, w.Code)
		}
	}
}

func TestClassify(t *testing.T) {
	for name, want := range map[string]fileKind{
		"a-1-1-x86_64.pkg.tar.zst":      kindPackage,
		"a-1:1-1-any.pkg.tar.xz":        kindPackage,
		"a-1-1-x86_64.pkg.tar.gz.sig":   kindPackageSig,
		"core.db":                       kindDatabase,
		"core.db.tar.gz":                kindDatabase,
		"core.files":                    kindDatabase,
		"core.files.tar.gz.sig":         kindDatabaseSig,
		"core.db.sig":                   kindDatabaseSig,
		".db":                           kindInvalid,
		"a-1-1-x86_64.pkg.tar.bz2":      kindInvalid,
		"../a-1-1-x86_64.pkg.tar.zst":   kindInvalid,
		"a b-1-1-x86_64.pkg.tar.zst":    kindInvalid,
		".hidden-1-1-any.pkg.tar.zst":   kindInvalid,
		"a-1-1-x86_64.pkg.tar.zst.sig2": kindInvalid,
	} {
		if got, _ := classify(name); got != want {
			t.Errorf("classify(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseRange(t *testing.T) {
	for _, tc := range []struct {
		header            string
		start, end        int64
		ok, unsatisfiable bool
	}{
		{"bytes=0-", 0, 99, true, false},
		{"bytes=10-", 10, 99, true, false},
		{"bytes=10-19", 10, 19, true, false},
		{"bytes=10-500", 10, 99, true, false},
		{"bytes=100-", 0, 0, false, true},
		{"bytes=-5", 0, 0, false, false},
		{"bytes=1-2,4-5", 0, 0, false, false},
		{"bytes=9-3", 0, 0, false, false},
		{"items=1-", 0, 0, false, false},
		{"", 0, 0, false, false},
		{"bytes=x-", 0, 0, false, false},
	} {
		start, end, ok, unsat := parseRange(tc.header, 100)
		if ok != tc.ok || unsat != tc.unsatisfiable || (ok && (start != tc.start || end != tc.end)) {
			t.Errorf("parseRange(%q) = %d %d %v %v", tc.header, start, end, ok, unsat)
		}
	}
}
