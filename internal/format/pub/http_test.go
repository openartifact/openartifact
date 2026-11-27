// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	pkgpub "github.com/openartifact/openartifact/pkg/pub"
	"github.com/openartifact/openartifact/pkg/pub/pubtest"
)

// pubDB is an in-memory store of pub packages with the live unique key of the
// generic package tables.
type pubDB struct {
	*formattest.DB

	rows []*models.PubPackage

	limit, used int64
}

func (d *pubDB) ReserveStorageUsage(_ context.Context, _ string, size int64, _ int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size

	return true, nil
}

func (d *pubDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.used -= size
	return nil
}

func (d *pubDB) CreatePubPackage(_ context.Context, c *models.PubPackage) error {
	for _, r := range d.rows {
		if r.Name == c.Name && r.Version == c.Version {
			return database.ErrDuplicate
		}
	}

	c.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", len(d.rows)+1)
	c.UpdatedAt = c.CreatedAt
	d.rows = append(d.rows, c)

	return nil
}

func (d *pubDB) GetPubPackage(_ context.Context, _, name, version string) (*models.PubPackage, error) {
	for _, r := range d.rows {
		if r.Name == name && r.Version == version {
			return r, nil
		}
	}

	return nil, nil
}

func (d *pubDB) ListPubVersions(_ context.Context, _, name string) ([]*models.PubPackage, error) {
	var out []*models.PubPackage

	for _, r := range d.rows {
		if r.Name == name {
			out = append(out, r)
		}
	}

	return out, nil
}

type fixture struct {
	h      *HTTPHandler
	db     *pubDB
	blobs  *formattest.BlobStore
	events *formattest.Publisher
	router http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	db := &pubDB{DB: formattest.NewDB("pub")}
	bs := formattest.NewBlobStore()
	events := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(events)

	router := chi.NewRouter()
	router.Route("/repos/pub/{name}", h.RegisterRepoRoutes)

	return &fixture{h: h, db: db, blobs: bs, events: events, router: router}
}

func (f *fixture) do(method, target string, body []byte, headers ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = formattest.Request(method, target, nil)
	} else {
		req = formattest.Request(method, target, bytes.NewReader(body))
	}

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	return w
}

const root = "/repos/pub/myrepo/"

func multipartBody(t *testing.T, data []byte) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)

	fw, err := mw.CreateFormFile("file", "package.tar.gz")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}

	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes(), mw.FormDataContentType()
}

// upload posts an archive to the upload URL the first step hands out.
func (f *fixture) upload(t *testing.T, data []byte) *httptest.ResponseRecorder {
	t.Helper()

	body, ct := multipartBody(t, data)

	return f.do(http.MethodPost, root+pkgpub.UploadPath, body, "Content-Type", ct)
}

func (f *fixture) publish(t *testing.T, spec pubtest.Spec) (*httptest.ResponseRecorder, []byte) {
	t.Helper()

	data := pubtest.Build(t, spec)

	return f.upload(t, data), data
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not JSON (%d): %s", w.Code, w.Body.String())
	}

	return doc
}

var demo = pubtest.Spec{Name: "demo_pkg", Version: "1.0.0"} //nolint:gochecknoglobals // read-only test fixture

//nolint:gocyclo,cyclop // one scenario: the three publish steps, the documents, the download
func TestPublishFlowReadDownload(t *testing.T) {
	f := newFixture(t)

	// Step 1: the upload URL.
	w := f.do(http.MethodGet, root+pkgpub.NewPath, nil)
	step1 := decode(t, w)

	if w.Code != http.StatusOK || step1["url"] != "http://localhost/repos/pub/myrepo/api/packages/versions/newUpload" {
		t.Fatalf("new = %d %v", w.Code, step1)
	}

	if fields, ok := step1["fields"].(map[string]any); !ok || len(fields) != 0 {
		t.Errorf("fields = %v", step1["fields"])
	}

	// Step 2: the upload answers 204 and points at the finishing request.
	w, data := f.publish(t, demo)
	if w.Code != http.StatusNoContent {
		t.Fatalf("upload = %d: %s", w.Code, w.Body.String())
	}

	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Host != "localhost" || loc.Path != root+pkgpub.FinishPath ||
		loc.Query().Get("package") != "demo_pkg" || loc.Query().Get("version") != "1.0.0" {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}

	if f.db.used != int64(len(data)) || len(f.blobs.Blobs) != 1 || len(f.db.rows) != 1 {
		t.Errorf("quota used = %d (want %d), blobs = %d, rows = %d", f.db.used, len(data), len(f.blobs.Blobs), len(f.db.rows))
	}

	if evs := f.events.Events(); len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded ||
		evs[0].Artifact()["name"] != "demo_pkg" || evs[0].Artifact()["path"] != "demo_pkg-1.0.0.tar.gz" {
		t.Errorf("events = %+v", evs)
	}

	// Step 3: the finishing GET.
	w = f.do(http.MethodGet, w.Header().Get("Location"), nil)
	if msg, _ := decode(t, w)["success"].(map[string]any); w.Code != http.StatusOK ||
		!strings.Contains(fmt.Sprint(msg["message"]), "demo_pkg") {
		t.Fatalf("finish = %d: %s", w.Code, w.Body.String())
	}

	// The package document.
	w = f.do(http.MethodGet, root+"api/packages/demo_pkg", nil, "Accept", pkgpub.ContentType)
	doc := decode(t, w)

	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != pkgpub.ContentType || doc["name"] != "demo_pkg" {
		t.Fatalf("package = %d %s: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}

	latest, _ := doc["latest"].(map[string]any)
	versions, _ := doc["versions"].([]any)
	wantURL := "http://localhost/repos/pub/myrepo/api/archives/demo_pkg-1.0.0.tar.gz"

	if len(versions) != 1 || latest["version"] != "1.0.0" || latest["archive_url"] != wantURL ||
		latest["archive_sha256"] != f.db.rows[0].SHA256 || latest["published"] == "" {
		t.Fatalf("document = %s", w.Body.String())
	}

	spec, _ := latest["pubspec"].(map[string]any)
	if spec["name"] != "demo_pkg" || spec["description"] != "A demo package." {
		t.Errorf("pubspec = %v", latest["pubspec"])
	}

	// The version document.
	w = f.do(http.MethodGet, root+"api/packages/demo_pkg/versions/1.0.0", nil)
	if one := decode(t, w); w.Code != http.StatusOK || one["archive_url"] != wantURL || one["version"] != "1.0.0" {
		t.Fatalf("version = %d: %s", w.Code, w.Body.String())
	}

	// The archive is what was uploaded, and its URL is the one the document names.
	w = f.do(http.MethodGet, wantURL, nil)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("download = %d, %d bytes", w.Code, w.Body.Len())
	}

	// Republishing the version is refused and changes nothing.
	if w, _ = f.publish(t, demo); w.Code != http.StatusConflict {
		t.Errorf("second publish = %d, want 409", w.Code)
	}

	if f.db.used != int64(len(data)) || len(f.db.rows) != 1 {
		t.Errorf("after a refused publish: quota = %d, rows = %d", f.db.used, len(f.db.rows))
	}
}

func TestPackageDocumentListsEveryVersionAndTheLatestStable(t *testing.T) {
	f := newFixture(t)

	for _, v := range []string{"1.10.0", "2.0.0-beta.1", "1.2.0", "1.9.0"} {
		spec := demo
		spec.Version = v

		if w, _ := f.publish(t, spec); w.Code != http.StatusNoContent {
			t.Fatalf("publish %s = %d: %s", v, w.Code, w.Body.String())
		}
	}

	w := f.do(http.MethodGet, root+"api/packages/demo_pkg", nil)
	doc := decode(t, w)

	var got []string
	for _, v := range doc["versions"].([]any) {
		got = append(got, v.(map[string]any)["version"].(string))
	}

	if want := "1.2.0 1.9.0 1.10.0 2.0.0-beta.1"; strings.Join(got, " ") != want {
		t.Errorf("versions = %v, want %s", got, want)
	}

	if latest := doc["latest"].(map[string]any); latest["version"] != "1.10.0" {
		t.Errorf("latest = %v, want the highest stable version", latest["version"])
	}
}

func TestPackageDocumentRevalidates(t *testing.T) {
	f := newFixture(t)

	if w, _ := f.publish(t, demo); w.Code != http.StatusNoContent {
		t.Fatalf("publish = %d", w.Code)
	}

	first := f.do(http.MethodGet, root+"api/packages/demo_pkg", nil)

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	if w := f.do(http.MethodGet, root+"api/packages/demo_pkg", nil, "If-None-Match", etag); w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}

	spec := demo
	spec.Version = "1.0.1"

	if w, _ := f.publish(t, spec); w.Code != http.StatusNoContent {
		t.Fatalf("publish = %d", w.Code)
	}

	if w := f.do(http.MethodGet, root+"api/packages/demo_pkg", nil, "If-None-Match", etag); w.Code != http.StatusOK {
		t.Errorf("after a new version = %d, want 200", w.Code)
	}
}

func TestUploadRejectsInvalidArchives(t *testing.T) {
	f := newFixture(t)

	cases := map[string][]byte{
		"not an archive": []byte("plain text"),
		"no pubspec":     pubtest.Tar(t, []pubtest.Entry{{Name: "lib/a.dart", Body: []byte("x")}}),
		"bad name":       pubtest.Build(t, pubtest.Spec{Pubspec: "name: Bad-Name\nversion: 1.0.0\n"}),
		"bad version":    pubtest.Build(t, pubtest.Spec{Pubspec: "name: ok\nversion: one\n"}),
	}

	for name, data := range cases {
		w := f.upload(t, data)
		body := decode(t, w)

		errDoc, _ := body["error"].(map[string]any)
		if w.Code != http.StatusBadRequest || errDoc["code"] != "PackageRejected" || errDoc["message"] == "" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}

	if len(f.db.rows) != 0 || len(f.blobs.Blobs) != 0 || f.db.used != 0 {
		t.Errorf("rejected uploads left rows=%d blobs=%d quota=%d", len(f.db.rows), len(f.blobs.Blobs), f.db.used)
	}

	// A form without the file part.
	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("other", "x")
	_ = mw.Close()

	if w := f.do(
		http.MethodPost,
		root+pkgpub.UploadPath,
		buf.Bytes(),
		"Content-Type",
		mw.FormDataContentType(),
	); w.Code != http.StatusBadRequest {
		t.Errorf("no file part = %d, want 400", w.Code)
	}
}

func TestUploadQuotaExceeded(t *testing.T) {
	f := newFixture(t)
	f.db.limit = 10

	w, _ := f.publish(t, demo)
	if errDoc, _ := decode(t, w)["error"].(map[string]any); w.Code != http.StatusInsufficientStorage || errDoc["message"] == "" {
		t.Fatalf("over quota = %d: %s", w.Code, w.Body.String())
	}

	if len(f.db.rows) != 0 || len(f.blobs.Blobs) != 0 {
		t.Errorf("rows=%d blobs=%d", len(f.db.rows), len(f.blobs.Blobs))
	}
}

func TestNotFoundAnswersWithTheSpecErrorDocument(t *testing.T) {
	f := newFixture(t)

	for _, target := range []string{
		root + "api/packages/missing",
		root + "api/packages/Not-A-Name",
		root + "api/packages/missing/versions/1.0.0",
		root + "api/archives/missing-1.0.0.tar.gz",
		root + "api/archives/garbage",
		root + pkgpub.FinishPath + "?package=missing&version=1.0.0",
		"/repos/pub/other/api/packages/missing",
	} {
		w := f.do(http.MethodGet, target, nil)

		if errDoc, _ := decode(t, w)["error"].(map[string]any); w.Code != http.StatusNotFound || errDoc["code"] != "NotFound" {
			t.Errorf("GET %s = %d: %s", target, w.Code, w.Body.String())
		}
	}
}

func TestDownloadQuarantinedByRowID(t *testing.T) {
	f := newFixture(t)

	for _, v := range []string{"1.0.0", "1.1.0"} {
		spec := demo
		spec.Version = v

		if w, _ := f.publish(t, spec); w.Code != http.StatusNoContent {
			t.Fatalf("publish %s = %d: %s", v, w.Code, w.Body.String())
		}
	}

	formattest.AssertQuarantinedByID(t, &f.db.Quarantine, f.db.rows[0].ID,
		func(path string) int { return f.do(http.MethodGet, path, nil).Code },
		root+"api/archives/demo_pkg-1.0.0.tar.gz", root+"api/archives/demo_pkg-1.1.0.tar.gz")
}
