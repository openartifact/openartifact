// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/cran"
)

// cranDB is an in-memory store of package files.
type cranDB struct {
	*formattest.DB

	files []*models.CRANFile
	stamp int

	limit int64 // 0 = unlimited
	used  int64
	count int
}

func newCRANDB() *cranDB { return &cranDB{DB: formattest.NewDB("cran")} }

func (d *cranDB) CreateCRANFile(_ context.Context, f *models.CRANFile, _ bool) error {
	for _, e := range d.files {
		if e.Path == f.Path {
			return database.ErrDuplicate
		}
	}

	f.ID = fmt.Sprintf("file-%d", len(d.files)+1)
	d.files = append(d.files, f)
	d.stamp++

	return nil
}

func (d *cranDB) GetCRANFile(_ context.Context, _, path string) (*models.CRANFile, error) {
	for _, f := range d.files {
		if f.Path == path {
			return f, nil
		}
	}

	return nil, nil
}

func (d *cranDB) ListCRANTreeFiles(_ context.Context, _, tree string) ([]*models.CRANFile, error) {
	var out []*models.CRANFile

	for _, f := range d.files {
		if f.Tree == tree {
			out = append(out, f)
		}
	}

	return out, nil
}

func (d *cranDB) ListCRANPackageFiles(_ context.Context, _, pkg string) ([]*models.CRANFile, error) {
	var out []*models.CRANFile

	for _, f := range d.files {
		if f.Package == pkg {
			out = append(out, f)
		}
	}

	return out, nil
}

func (d *cranDB) ListCRANPackageNames(context.Context, string) ([]string, error) {
	var names []string

	for _, f := range d.files {
		if !slices.Contains(names, f.Package) {
			names = append(names, f.Package)
		}
	}

	slices.Sort(names)

	return names, nil
}

func (d *cranDB) DeleteCRANFile(_ context.Context, _, id string) error {
	d.files = slices.DeleteFunc(d.files, func(f *models.CRANFile) bool { return f.ID == id })
	d.stamp++

	return nil
}

func (d *cranDB) CRANIndexStamp(context.Context, string) (string, error) {
	return fmt.Sprintf("%d/%d", len(d.files), d.stamp), nil
}

func (d *cranDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size
	d.count += count

	return true, nil
}

func (d *cranDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.used -= size
	d.count -= count

	return nil
}

// ListLicensePolicies: no policy unless a test wraps the store (policyDB).
func (d *cranDB) ListLicensePolicies(context.Context, string) ([]*models.LicensePolicy, error) {
	return nil, nil
}

func (d *cranDB) GetOrCreateStorageQuota(context.Context, string) (*models.StorageQuota, error) {
	return &models.StorageQuota{MaxSizeBytes: d.limit}, nil
}

// newHandlerOnly is newTestHandler for tests that only drive the handler.
func newHandlerOnly(t *testing.T) *HTTPHandler {
	t.Helper()

	h, _, _, _ := newTestHandler(t) //nolint:dogsled // only the handler matters here

	return h
}

func newTestHandler(t *testing.T) (*HTTPHandler, *cranDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := newCRANDB()
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func description(name, version string, extra ...string) string {
	lines := []string{
		"Package: " + name,
		"Version: " + version,
		"Title: A test package",
		"Description: Does nothing",
		"License: MIT + file LICENSE",
	}

	return strings.Join(append(lines, extra...), "\n") + "\n"
}

// sourcePackage builds the .tar.gz R CMD build would: the package directory
// with a DESCRIPTION.
func sourcePackage(t *testing.T, dir, desc string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, f := range []struct{ name, body string }{{dir + "/DESCRIPTION", desc}, {dir + "/NAMESPACE", "export(x)\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// binaryZip builds a Windows binary package.
func binaryZip(t *testing.T, dir, desc string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create(dir + "/DESCRIPTION")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte(desc)); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func put(h *HTTPHandler, path string, body []byte) *httptest.ResponseRecorder {
	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/"+path, bytes.NewReader(body)))
}

func get(h *HTTPHandler, path string, headers ...string) *httptest.ResponseRecorder {
	req := formattest.Request(http.MethodGet, "/repos/myrepo/"+path, nil)

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	return formattest.Do(h.RegisterRepoRoutes, req)
}

func uploadSource(t *testing.T, h *HTTPHandler, name, version string, extra ...string) {
	t.Helper()

	body := sourcePackage(t, name, description(name, version, extra...))

	if w := put(h, fmt.Sprintf("src/contrib/%s_%s.tar.gz", name, version), body); w.Code != http.StatusCreated {
		t.Fatalf("upload %s %s = %d: %s", name, version, w.Code, w.Body)
	}
}

func TestUploadStoresFileReservesQuotaAndEmitsEvent(t *testing.T) {
	check := formattest.IsolatedTemp(t)
	h, db, bs, pub := newTestHandler(t)
	body := sourcePackage(t, "mypkg", description("mypkg", "1.0.0", "Depends: R (>= 4.0)", "Imports: glue"))

	w := put(h, "src/contrib/mypkg_1.0.0.tar.gz", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	check()

	if db.used != int64(len(body)) || db.count != 1 {
		t.Errorf("quota used=%d count=%d, want %d/1", db.used, db.count, len(body))
	}

	if len(db.files) != 1 {
		t.Fatalf("files = %d, want 1", len(db.files))
	}

	f := db.files[0]
	if f.Package != "mypkg" || f.Version != "1.0.0" || f.Path != "src/contrib/mypkg_1.0.0.tar.gz" || f.Tree != "src/contrib" {
		t.Errorf("file = %+v", f)
	}

	if f.Fields["Imports"] != "glue" || f.License != "MIT + file LICENSE" || f.Title != "A test package" {
		t.Errorf("metadata = %+v", f)
	}

	if _, ok := bs.Blobs[f.BlobKey]; !ok {
		t.Error("blob not stored")
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "mypkg" || a["version"] != "1.0.0" || a["path"] != f.Path {
		t.Errorf("artifact = %v", a)
	}
}

func TestDownloadReturnsUploadedBytes(t *testing.T) {
	h := newHandlerOnly(t)
	body := sourcePackage(t, "mypkg", description("mypkg", "1.0.0"))
	put(h, "src/contrib/mypkg_1.0.0.tar.gz", body)

	for _, path := range []string{"src/contrib/mypkg_1.0.0.tar.gz", "src/contrib/Archive/mypkg/mypkg_1.0.0.tar.gz"} {
		w := get(h, path)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) {
			t.Errorf("GET %s = %d (%d bytes), want the uploaded %d bytes", path, w.Code, w.Body.Len(), len(body))
		}

		if w.Header().Get("ETag") == "" {
			t.Errorf("GET %s has no ETag", path)
		}
	}

	if w := get(h, "src/contrib/mypkg_2.0.0.tar.gz"); w.Code != http.StatusNotFound {
		t.Errorf("unknown version = %d, want 404", w.Code)
	}
}

func TestPackagesListsNewestVersionOfEachPackage(t *testing.T) {
	h := newHandlerOnly(t)
	uploadSource(t, h, "alpha", "1.0.0")
	uploadSource(t, h, "alpha", "1.10.0", "Imports: glue")
	uploadSource(t, h, "alpha", "1.9.0")
	uploadSource(t, h, "beta", "0.1", "Depends: R (>= 3.5)")

	w := get(h, "src/contrib/PACKAGES")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	recs, err := cran.ParsePackages(bytes.NewReader(w.Body.Bytes()), cran.MaxIndexBytes)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range recs {
		got = append(got, r.Get("Package")+"_"+r.Get("Version"))
	}

	if want := []string{"alpha_1.10.0", "beta_0.1"}; !slices.Equal(got, want) {
		t.Errorf("PACKAGES lists %v, want %v", got, want)
	}

	if recs[0].Get("Imports") != "glue" || recs[0].Get("MD5sum") == "" {
		t.Errorf("record = %v", recs[0])
	}

	// The gzip and rds variants carry the same packages.
	gz := get(h, "src/contrib/PACKAGES.gz")
	if gz.Code != http.StatusOK || gz.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("PACKAGES.gz = %d %q", gz.Code, gz.Header().Get("Content-Type"))
	}

	plain, err := cran.Gunzip(bytes.NewReader(gz.Body.Bytes()), cran.MaxIndexBytes)
	if err != nil || !bytes.Equal(plain, w.Body.Bytes()) {
		t.Errorf("PACKAGES.gz does not hold PACKAGES: %v", err)
	}

	if rds := get(h, "src/contrib/PACKAGES.rds"); rds.Code != http.StatusOK || rds.Body.Len() == 0 {
		t.Errorf("PACKAGES.rds = %d (%d bytes)", rds.Code, rds.Body.Len())
	}

	// Every version stays downloadable.
	for _, f := range []string{"alpha_1.0.0", "alpha_1.9.0", "alpha_1.10.0"} {
		if c := get(h, "src/contrib/"+f+".tar.gz").Code; c != http.StatusOK {
			t.Errorf("%s = %d", f, c)
		}
	}
}

func TestPackagesETagFollowsTheFileSet(t *testing.T) {
	h := newHandlerOnly(t)
	uploadSource(t, h, "alpha", "1.0.0")

	first := get(h, "src/contrib/PACKAGES")
	etag := first.Header().Get("ETag")

	if etag == "" {
		t.Fatal("PACKAGES has no ETag")
	}

	if w := get(h, "src/contrib/PACKAGES", "If-None-Match", etag); w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}

	uploadSource(t, h, "beta", "1.0.0")

	w := get(h, "src/contrib/PACKAGES", "If-None-Match", etag)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Package: beta") {
		t.Errorf("after an upload = %d, want 200 with beta listed", w.Code)
	}
}

func TestEmptyRepositoryServesEmptyIndex(t *testing.T) {
	h := newHandlerOnly(t)

	for _, doc := range []string{"PACKAGES", "PACKAGES.gz", "PACKAGES.rds"} {
		if w := get(h, "src/contrib/"+doc); w.Code != http.StatusOK {
			t.Errorf("%s of an empty repository = %d, want 200", doc, w.Code)
		}
	}
}

func TestBinaryTreeHasItsOwnIndexAndKeepsSourceMetadata(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	uploadSource(t, h, "alpha", "1.0.0")

	zipBody := binaryZip(t, "alpha", description("alpha", "1.0.0", "Built: R 4.5.0; ; 2025-01-01 00:00:00 UTC; windows"))

	if w := put(h, "bin/windows/contrib/4.5/alpha_1.0.0.zip", zipBody); w.Code != http.StatusCreated {
		t.Fatalf("binary upload = %d: %s", w.Code, w.Body)
	}

	if len(db.files) != 2 {
		t.Fatalf("files = %d, want 2", len(db.files))
	}

	bin := get(h, "bin/windows/contrib/4.5/PACKAGES")
	if bin.Code != http.StatusOK || !strings.Contains(bin.Body.String(), "Package: alpha") {
		t.Errorf("binary PACKAGES = %d %q", bin.Code, bin.Body)
	}

	if w := get(h, "bin/windows/contrib/4.5/alpha_1.0.0.zip"); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), zipBody) {
		t.Errorf("binary download = %d", w.Code)
	}

	// A second binary of the same name and version is a duplicate in its own tree.
	if w := put(h, "bin/windows/contrib/4.5/alpha_1.0.0.zip", zipBody); w.Code != http.StatusConflict {
		t.Errorf("duplicate binary = %d, want 409", w.Code)
	}

	// The source tree does not list the binary.
	src := get(h, "src/contrib/PACKAGES")
	if strings.Contains(src.Body.String(), "Built:") {
		t.Errorf("source PACKAGES lists a binary:\n%s", src.Body)
	}
}

func TestArchiveListings(t *testing.T) {
	h := newHandlerOnly(t)
	uploadSource(t, h, "alpha", "1.0.0")
	uploadSource(t, h, "alpha", "1.1.0")
	uploadSource(t, h, "beta", "2.0.0")

	root := get(h, "src/contrib/Archive/")
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), `href="alpha/"`) ||
		!strings.Contains(root.Body.String(), `href="beta/"`) {
		t.Errorf("Archive/ = %d %q", root.Code, root.Body)
	}

	dir := get(h, "src/contrib/Archive/alpha/")
	if dir.Code != http.StatusOK {
		t.Fatalf("Archive/alpha/ = %d", dir.Code)
	}

	for _, want := range []string{"alpha_1.0.0.tar.gz", "alpha_1.1.0.tar.gz"} {
		if !strings.Contains(dir.Body.String(), `href="`+want+`"`) {
			t.Errorf("Archive/alpha/ lacks %s: %s", want, dir.Body)
		}
	}

	if strings.Contains(dir.Body.String(), "beta") {
		t.Errorf("Archive/alpha/ lists another package: %s", dir.Body)
	}

	// The probe a group uses: a package that is not held is a 404.
	if w := get(h, "src/contrib/Archive/gamma/"); w.Code != http.StatusNotFound {
		t.Errorf("Archive/gamma/ = %d, want 404", w.Code)
	}
}

func TestUploadRejectsMismatchesAndGarbage(t *testing.T) {
	check := formattest.IsolatedTemp(t)
	h, db, bs, _ := newTestHandler(t)

	cases := []struct {
		name, path string
		body       []byte
		want       int
	}{
		{"name differs", "src/contrib/other_1.0.tar.gz", sourcePackage(t, "other", description("mypkg", "1.0")), http.StatusBadRequest},
		{"version differs", "src/contrib/mypkg_1.0.tar.gz", sourcePackage(t, "mypkg", description("mypkg", "2.0")), http.StatusBadRequest},
		{
			"directory differs",
			"src/contrib/mypkg_1.0.tar.gz",
			sourcePackage(t, "wrongdir", description("mypkg", "1.0")),
			http.StatusBadRequest,
		},
		{"not an archive", "src/contrib/mypkg_1.0.tar.gz", []byte("hello"), http.StatusBadRequest},
		{
			"file name is no package",
			"src/contrib/mypkg.tar.gz",
			sourcePackage(t, "mypkg", description("mypkg", "1.0")),
			http.StatusNotFound,
		},
		{"index is generated", "src/contrib/PACKAGES", []byte("x"), http.StatusMethodNotAllowed},
		{"traversal", "src/contrib/../mypkg_1.0.tar.gz", sourcePackage(t, "mypkg", description("mypkg", "1.0")), http.StatusNotFound},
		{"unknown tree", "elsewhere/mypkg_1.0.tar.gz", sourcePackage(t, "mypkg", description("mypkg", "1.0")), http.StatusNotFound},
		{
			"wrong extension for the tree",
			"src/contrib/mypkg_1.0.zip",
			binaryZip(t, "mypkg", description("mypkg", "1.0")),
			http.StatusNotFound,
		},
	}

	for _, c := range cases {
		if w := put(h, c.path, c.body); w.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, w.Code, c.want, strings.TrimSpace(w.Body.String()))
		}
	}

	check()

	if len(db.files) != 0 || len(bs.Blobs) != 0 || db.used != 0 {
		t.Errorf("rejected uploads left files=%d blobs=%d used=%d", len(db.files), len(bs.Blobs), db.used)
	}
}

func TestUploadDuplicateIs409AndConsumesNoQuota(t *testing.T) {
	h, db, _, pub := newTestHandler(t)
	uploadSource(t, h, "alpha", "1.0.0")

	used := db.used

	body := sourcePackage(t, "alpha", description("alpha", "1.0.0", "Imports: other"))
	if w := put(h, "src/contrib/alpha_1.0.0.tar.gz", body); w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}

	if db.used != used || len(db.files) != 1 || len(pub.Events()) != 1 {
		t.Errorf("duplicate changed state: used=%d files=%d events=%d", db.used, len(db.files), len(pub.Events()))
	}
}

func TestUploadOverQuotaIs507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 10

	w := put(h, "src/contrib/alpha_1.0.0.tar.gz", sourcePackage(t, "alpha", description("alpha", "1.0.0")))
	if w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", w.Code)
	}

	if len(db.files) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 {
		t.Errorf("files=%d blobs=%d events=%d used=%d; want nothing persisted", len(db.files), len(bs.Blobs), len(pub.Events()), db.used)
	}
}

func TestDeleteRemovesFileFromIndexAndReleasesQuota(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	uploadSource(t, h, "alpha", "1.0.0")
	uploadSource(t, h, "alpha", "1.1.0")

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/src/contrib/alpha_1.1.0.tar.gz", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", w.Code, w.Body)
	}

	if db.count != 1 || len(db.files) != 1 || len(bs.Blobs) != 1 {
		t.Errorf("count=%d files=%d blobs=%d, want 1/1/1", db.count, len(db.files), len(bs.Blobs))
	}

	if c := get(h, "src/contrib/alpha_1.1.0.tar.gz").Code; c != http.StatusNotFound {
		t.Errorf("deleted file = %d, want 404", c)
	}

	if idx := get(h, "src/contrib/PACKAGES").Body.String(); !strings.Contains(idx, "Version: 1.0.0") || strings.Contains(idx, "1.1.0") {
		t.Errorf("index after delete:\n%s", idx)
	}

	evs := pub.Events()
	if len(evs) != 3 || evs[2].Type != webhook.EventArtifactDeleted {
		t.Errorf("events = %+v", evs)
	}

	if c := formattest.Do(
		h.RegisterRepoRoutes,
		formattest.Request(http.MethodDelete, "/repos/myrepo/src/contrib/alpha_1.1.0.tar.gz", nil),
	).Code; c != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", c)
	}
}

func TestDownloadQuarantinedByRowID(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	for _, v := range []struct{ id, version string }{{"file-1", "1.0.0"}, {"file-2", "1.1.0"}} {
		db.files = append(db.files, &models.CRANFile{
			ID: v.id, Package: "alpha", Version: v.version, Path: "src/contrib/alpha_" + v.version + ".tar.gz",
			Filename: "alpha_" + v.version + ".tar.gz", Tree: "src/contrib", BlobKey: "abc", Digest: "abc", SizeBytes: 3,
		})
	}

	bs.Blobs["abc"] = []byte("xyz")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/src/contrib/alpha_1.0.0.tar.gz", "/repos/myrepo/src/contrib/alpha_1.1.0.tar.gz")
}

func TestHeadAndMethods(t *testing.T) {
	h := newHandlerOnly(t)
	uploadSource(t, h, "alpha", "1.0.0")

	head := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodHead, "/repos/myrepo/src/contrib/alpha_1.0.0.tar.gz", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") == "" {
		t.Errorf("HEAD = %d with %d body bytes, etag %q", head.Code, head.Body.Len(), head.Header().Get("ETag"))
	}

	if w := get(h, "src/contrib/Archive/alpha/alpha_9.9.9.tar.gz"); w.Code != http.StatusNotFound {
		t.Errorf("unknown archive file = %d, want 404", w.Code)
	}
}
