// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/swift"
	"github.com/openartifact/openartifact/pkg/swift/swifttest"
)

// swiftDB is an in-memory store of swift releases with the live unique key of
// the generic package tables and the case-insensitive package lookup.
type swiftDB struct {
	*formattest.DB

	rows []*models.SwiftRelease

	limit, used int64
}

func (d *swiftDB) ReserveStorageUsage(_ context.Context, _ string, size int64, _ int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size

	return true, nil
}

func (d *swiftDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.used -= size
	return nil
}

func (d *swiftDB) CreateSwiftRelease(_ context.Context, r *models.SwiftRelease) error {
	if rel, _ := d.GetSwiftRelease(context.Background(), "", r.Scope, r.Name, r.Version); rel != nil {
		return database.ErrDuplicate
	}

	r.ID = "release-" + swift.Key(r.Scope, r.Name) + "-" + r.Version
	r.UpdatedAt = r.CreatedAt
	d.rows = append(d.rows, r)

	return nil
}

func (d *swiftDB) GetSwiftRelease(_ context.Context, _, scope, name, version string) (*models.SwiftRelease, error) {
	for _, r := range d.rows {
		if swift.Key(r.Scope, r.Name) == swift.Key(scope, name) && r.Version == version {
			return r, nil
		}
	}

	return nil, nil
}

func (d *swiftDB) ListSwiftReleaseVersions(_ context.Context, _, scope, name string) ([]models.SwiftReleaseVersion, error) {
	var out []models.SwiftReleaseVersion

	for _, r := range d.rows {
		if swift.Key(r.Scope, r.Name) == swift.Key(scope, name) {
			out = append(out, models.SwiftReleaseVersion{Version: r.Version, CreatedAt: r.CreatedAt})
		}
	}

	return out, nil
}

func (d *swiftDB) ListSwiftIdentifiers(_ context.Context, _, normalizedURL string) ([]string, error) {
	var out []string

	for _, r := range d.rows {
		if slices.Contains(r.RepositoryURLs, normalizedURL) && !slices.Contains(out, swift.ID(r.Scope, r.Name)) {
			out = append(out, swift.ID(r.Scope, r.Name))
		}
	}

	slices.Sort(out)

	return out, nil
}

func (d *swiftDB) DeleteSwiftRelease(_ context.Context, _, id string) error {
	d.rows = slices.DeleteFunc(d.rows, func(r *models.SwiftRelease) bool { return r.ID == id })
	return nil
}

type fixture struct {
	h   *HTTPHandler
	db  *swiftDB
	bs  *formattest.BlobStore
	pub *formattest.Publisher
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	db := &swiftDB{DB: formattest.NewDB("swift")}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://registry.test")
	h.SetPublisher(pub)

	return &fixture{h: h, db: db, bs: bs, pub: pub}
}

const root = "/repos/myrepo"

// do sends a request with the Accept header of a SwiftPM client unless the
// headers name one.
func (f *fixture) do(method, target string, body []byte, headers ...string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}

	req := formattest.Request(method, target, rd)
	req.Header.Set("Accept", swift.MediaJSON)

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	return formattest.Do(f.h.RegisterRepoRoutes, req)
}

// part describes one multipart section of a publish request.
type part struct {
	name     string
	body     []byte
	encoding string
}

// form builds the multipart body "swift package-registry publish" sends: the
// sections carry no file name.
func form(t *testing.T, parts ...part) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)

	for _, p := range parts {
		h := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="` + p.name + `"`}}

		body := p.body

		if p.encoding != "" {
			h.Set("Content-Transfer-Encoding", p.encoding)
		}

		if p.encoding == "base64" {
			body = []byte(base64.StdEncoding.EncodeToString(body))
		}

		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}

	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes(), mw.FormDataContentType()
}

func (f *fixture) publish(t *testing.T, path string, parts []part, headers ...string) *httptest.ResponseRecorder {
	t.Helper()

	body, ct := form(t, parts...)

	return f.do(http.MethodPut, root+path, body, append([]string{"Content-Type", ct}, headers...)...)
}

func archivePart(data []byte) part {
	return part{name: "source-archive", body: data, encoding: "binary"}
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// published publishes a release of mona/LinkedList and fails the test unless
// it is created.
func (f *fixture) published(t *testing.T, version string, extra map[string]string, metadata string) []byte {
	t.Helper()

	data := swifttest.Package(t, "", "LinkedList", extra)
	parts := []part{archivePart(data)}

	if metadata != "" {
		parts = append(parts, part{name: "metadata", body: []byte(metadata), encoding: "quoted-printable"})
	}

	if w := f.publish(t, "/mona/LinkedList/"+version, parts); w.Code != http.StatusCreated {
		t.Fatalf("publish %s = %d: %s", version, w.Code, w.Body.String())
	}

	return data
}

func jsonBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not JSON (%d): %s", w.Code, w.Body.String())
	}

	return doc
}

// problem asserts the response is a problem document with the status and a
// detail containing want.
func problem(t *testing.T, w *httptest.ResponseRecorder, status int, want string) {
	t.Helper()

	if w.Code != status {
		t.Fatalf("status = %d, want %d: %s", w.Code, status, w.Body.String())
	}

	if ct := w.Header().Get("Content-Type"); ct != swift.ContentTypeProblem {
		t.Errorf("Content-Type = %q, want %q", ct, swift.ContentTypeProblem)
	}

	if detail, _ := jsonBody(t, w)["detail"].(string); !strings.Contains(detail, want) {
		t.Errorf("detail = %q, want it to contain %q", detail, want)
	}
}

func TestPublishThenRead(t *testing.T) {
	f := newFixture(t)

	data := f.published(t, "1.0.0", map[string]string{"Package@swift-5.7.swift": swifttest.Manifest("5.7", "LinkedList")},
		`{"description":"A linked list","repositoryURLs":["https://github.com/mona/LinkedList.git","git@github.com:mona/LinkedList.git"]}`)

	row := f.db.rows[0]
	if row.Filename != "LinkedList-1.0.0.zip" || row.SHA256 != sum(data) || row.SizeBytes != int64(len(data)) {
		t.Fatalf("row = %+v", row)
	}

	if f.db.used != int64(len(data)) || len(f.bs.Blobs) != 1 {
		t.Errorf("quota used = %d (want %d), blobs = %d", f.db.used, len(data), len(f.bs.Blobs))
	}

	if evs := f.pub.Events(); len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded ||
		evs[0].Artifact()["name"] != "mona.LinkedList" || evs[0].Artifact()["version"] != "1.0.0" {
		t.Errorf("events = %+v", evs)
	}

	if !slices.Equal(row.RepositoryURLs, []string{"github.com/mona/linkedlist"}) {
		t.Errorf("repository URLs = %v, want the two spellings stored as one", row.RepositoryURLs)
	}

	t.Run("release metadata", func(t *testing.T) { checkReleaseMetadata(t, f, data) })
	t.Run("source archive", func(t *testing.T) { checkSourceArchive(t, f, data) })
	t.Run("case insensitive", func(t *testing.T) { checkCaseInsensitive(t, f, data) })
	t.Run("identifiers", func(t *testing.T) { checkIdentifiers(t, f, data) })
}

func checkReleaseMetadata(t *testing.T, f *fixture, data []byte) {
	t.Helper()

	w := f.do(http.MethodGet, root+"/mona/LinkedList/1.0.0", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Content-Version") != "1" {
		t.Fatalf("GET release = %d %v", w.Code, w.Header())
	}

	doc := jsonBody(t, w)
	res, _ := doc["resources"].([]any)

	if doc["id"] != "mona.LinkedList" || doc["version"] != "1.0.0" || len(res) != 1 {
		t.Fatalf("doc = %v", doc)
	}

	archive, _ := res[0].(map[string]any)
	if archive["name"] != "source-archive" || archive["type"] != "application/zip" || archive["checksum"] != sum(data) {
		t.Errorf("source-archive resource = %v", archive)
	}

	if md, _ := doc["metadata"].(map[string]any); md["description"] != "A linked list" {
		t.Errorf("metadata = %v", doc["metadata"])
	}

	// The .json extension names the same document.
	if w2 := f.do(http.MethodGet, root+"/mona/LinkedList/1.0.0.json", nil); w2.Body.String() != w.Body.String() {
		t.Errorf("1.0.0.json = %q, want %q", w2.Body.String(), w.Body.String())
	}
}

func checkSourceArchive(t *testing.T, f *fixture, data []byte) {
	t.Helper()

	w := f.do(http.MethodGet, root+"/mona/LinkedList/1.0.0.zip", nil, "Accept", swift.MediaZip)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("GET zip = %d, %d bytes", w.Code, w.Body.Len())
	}

	want := map[string]string{
		"Content-Type":        "application/zip",
		"Content-Version":     "1",
		"Content-Disposition": `attachment; filename="LinkedList-1.0.0.zip"`,
		"Accept-Ranges":       "bytes",
		"Digest":              swift.DigestHeader(sum(data)),
	}

	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	// A byte range is served.
	r := f.do(http.MethodGet, root+"/mona/LinkedList/1.0.0.zip", nil, "Range", "bytes=0-3")
	if r.Code != http.StatusPartialContent || !bytes.Equal(r.Body.Bytes(), data[:4]) {
		t.Errorf("range = %d %q", r.Code, r.Body.Bytes())
	}

	if h := f.do(http.MethodHead, root+"/mona/LinkedList/1.0.0.zip", nil); h.Code != http.StatusOK || h.Body.Len() != 0 {
		t.Errorf("HEAD zip = %d with %d body bytes", h.Code, h.Body.Len())
	}
}

func checkCaseInsensitive(t *testing.T, f *fixture, data []byte) {
	t.Helper()

	for _, p := range []string{"/MONA/linkedlist/1.0.0", "/Mona/LINKEDLIST/1.0.0.zip", "/mona/linkedlist"} {
		if w := f.do(http.MethodGet, root+p, nil); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d", p, w.Code)
		}
	}

	// The identity is the one it was published with.
	if id := jsonBody(t, f.do(http.MethodGet, root+"/MONA/linkedlist/1.0.0", nil))["id"]; id != "mona.LinkedList" {
		t.Errorf("id = %v", id)
	}
}

func checkIdentifiers(t *testing.T, f *fixture, data []byte) {
	t.Helper()

	for _, u := range []string{"https://github.com/mona/LinkedList", "ssh://git@github.com/mona/linkedlist.git", "http://www.github.com:443/mona/LinkedList/"} {
		w := f.do(http.MethodGet, root+"/identifiers?url="+u, nil)
		ids, _ := jsonBody(t, w)["identifiers"].([]any)

		if w.Code != http.StatusOK || len(ids) != 1 || ids[0] != "mona.LinkedList" {
			t.Errorf("identifiers(%s) = %d %s", u, w.Code, w.Body.String())
		}
	}

	problem(t, f.do(http.MethodGet, root+"/identifiers", nil), http.StatusBadRequest, "url")
	problem(t, f.do(http.MethodGet, root+"/identifiers?url=https://github.com/other/repo", nil), http.StatusNotFound, "not found")
}

func TestReleaseListAndLinks(t *testing.T) {
	f := newFixture(t)

	for _, v := range []string{"1.0.0", "1.10.0", "1.2.0", "2.0.0-beta.1"} {
		f.published(t, v, nil, "")
	}

	w := f.do(http.MethodGet, root+"/mona/LinkedList", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", w.Code, w.Body.String())
	}

	// Newest first, with the absolute URL of each release.
	body := w.Body.String()
	order := []string{"2.0.0-beta.1", "1.10.0", "1.2.0", "1.0.0"}

	last := -1

	for _, v := range order {
		i := strings.Index(body, `"`+v+`":{"url":"http://registry.test/repos/swift/myrepo/mona/LinkedList/`+v+`"}`)
		if i <= last {
			t.Fatalf("version %s missing or out of order in %s", v, body)
		}

		last = i
	}

	links := swift.ParseLinks(w.Header().Values("Link"))
	if len(links) != 1 || links[0].Rel != swift.RelLatest ||
		links[0].URL != "http://registry.test/repos/swift/myrepo/mona/LinkedList/1.10.0" {
		t.Errorf("list links = %+v, want the newest stable release as latest-version", links)
	}

	if !bytes.Equal(f.do(http.MethodGet, root+"/mona/LinkedList.json", nil).Body.Bytes(), w.Body.Bytes()) {
		t.Error("the .json list differs from the list")
	}

	// A release names its neighbours.
	rel := swift.ParseLinks(f.do(http.MethodGet, root+"/mona/LinkedList/1.2.0", nil).Header().Values("Link"))
	got := map[string]string{}

	for _, l := range rel {
		got[l.Rel] = l.URL[strings.LastIndex(l.URL, "/")+1:]
	}

	if got[swift.RelLatest] != "1.10.0" || got[swift.RelSuccessor] != "1.10.0" || got[swift.RelPredecessor] != "1.0.0" {
		t.Errorf("release links = %v", got)
	}

	problem(t, f.do(http.MethodGet, root+"/mona/Unknown", nil), http.StatusNotFound, "not found")
	problem(t, f.do(http.MethodGet, root+"/mona/LinkedList/9.9.9", nil), http.StatusNotFound, "not found")
}

func TestManifests(t *testing.T) {
	f := newFixture(t)

	f.published(t, "1.0.0", map[string]string{
		"Package@swift-4.2.swift":   swifttest.Manifest("4.2", "LinkedList"),
		"Package@swift-5.7.0.swift": swifttest.Manifest("5.7", "LinkedList"),
	}, "")

	const manifest = root + "/mona/LinkedList/1.0.0/Package.swift"

	w := f.do(http.MethodGet, manifest, nil, "Accept", swift.MediaSwift)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/x-swift" ||
		w.Header().Get("Content-Disposition") != `attachment; filename="Package.swift"` ||
		w.Body.String() != swifttest.Manifest("5.9", "LinkedList") {
		t.Fatalf("Package.swift = %d %v\n%s", w.Code, w.Header(), w.Body.String())
	}

	alts := map[string]swift.Link{}

	for _, l := range swift.ParseLinks(w.Header().Values("Link")) {
		if l.Rel != swift.RelAlternate {
			t.Errorf("unexpected link %+v", l)
		}

		alts[l.Params["filename"]] = l
	}

	if l := alts["Package@swift-4.2.swift"]; l.Params["swift-tools-version"] != "4.2" ||
		l.URL != "http://registry.test/repos/swift/myrepo"+strings.TrimPrefix(manifest, root)+"?swift-version=4.2" {
		t.Errorf("alternate for 4.2 = %+v", l)
	}

	if l := alts["Package@swift-5.7.0.swift"]; l.Params["swift-tools-version"] != "5.7" ||
		!strings.HasSuffix(l.URL, "?swift-version=5.7.0") {
		t.Errorf("alternate for 5.7.0 = %+v", l)
	}

	v := f.do(http.MethodGet, manifest+"?swift-version=4.2", nil)
	if v.Code != http.StatusOK || v.Body.String() != swifttest.Manifest("4.2", "LinkedList") ||
		v.Header().Get("Content-Disposition") != `attachment; filename="Package@swift-4.2.swift"` {
		t.Errorf("?swift-version=4.2 = %d %v\n%s", v.Code, v.Header(), v.Body.String())
	}

	// A Swift version without a manifest of its own is redirected to the plain one.
	for _, sv := range []string{"5.9", "nonsense", "../../x"} {
		r := f.do(http.MethodGet, manifest+"?swift-version="+sv, nil)
		if r.Code != http.StatusSeeOther ||
			r.Header().Get("Location") != "http://registry.test/repos/swift/myrepo"+strings.TrimPrefix(manifest, root) {
			t.Errorf("?swift-version=%s = %d Location %q", sv, r.Code, r.Header().Get("Location"))
		}
	}

	problem(t, f.do(http.MethodGet, root+"/mona/LinkedList/2.0.0/Package.swift", nil), http.StatusNotFound, "not found")

	// The validator revalidates the document.
	etag := w.Header().Get("ETag")
	if n := f.do(http.MethodGet, manifest, nil, "If-None-Match", etag); n.Code != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", n.Code)
	}
}

func TestPublishRefusals(t *testing.T) {
	good := swifttest.Package(t, "", "LinkedList", nil)

	t.Run("duplicate is refused before the body is read", func(t *testing.T) {
		f := newFixture(t)
		f.published(t, "1.0.0", nil, "")

		// The body is not a multipart form: the answer is 409, so the request was
		// refused from its headers (a client sending Expect: 100-continue never
		// uploads the archive).
		w := f.do(
			http.MethodPut,
			root+"/mona/LinkedList/1.0.0",
			[]byte("not read"),
			"Content-Type",
			"multipart/form-data; boundary=x",
			"Expect",
			"100-continue",
		)
		problem(t, w, http.StatusConflict, "a release with version 1.0.0 already exists")

		// The same release under another spelling of the name is the same release.
		problem(t, f.publish(t, "/MONA/linkedlist/1.0.0", []part{archivePart(good)}), http.StatusConflict, "already exists")

		if len(f.db.rows) != 1 || f.db.used != int64(len(good)) {
			t.Errorf("rows = %d, used = %d", len(f.db.rows), f.db.used)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		f := newFixture(t)

		tests := []struct {
			name    string
			path    string
			parts   []part
			headers []string
			status  int
			detail  string
		}{
			{"version", "/mona/LinkedList/1.0", []part{archivePart(good)}, nil, 422, "invalid"},
			{"version with extension", "/mona/LinkedList/1.0.0.zip", []part{archivePart(good)}, nil, 422, "invalid"},
			{"scope", "/-mona/LinkedList/1.0.0", []part{archivePart(good)}, nil, 422, "invalid"},
			{"name", "/mona/Linked%20List/1.0.0", []part{archivePart(good)}, nil, 422, "invalid"},
			{"no archive", "/mona/LinkedList/1.0.0", []part{{name: "metadata", body: []byte("{}")}}, nil, 422, "source-archive"},
			{"not a zip", "/mona/LinkedList/1.0.0", []part{archivePart([]byte("hello"))}, nil, 422, "archive"},
			{
				"no manifest", "/mona/LinkedList/1.0.0",
				[]part{archivePart(swifttest.Archive(t, map[string]string{"README.md": "x"}))}, nil, 422,
				"package doesn't contain a valid manifest (Package.swift) file",
			},
			{
				"metadata not an object",
				"/mona/LinkedList/1.0.0",
				[]part{archivePart(good), {name: "metadata", body: []byte("[]")}},
				nil,
				422,
				"metadata",
			},
			{
				"bad license URL", "/mona/LinkedList/1.0.0",
				[]part{archivePart(good), {name: "metadata", body: []byte(`{"licenseURL":"not a url"}`)}}, nil, 422, "metadata",
			},
			{"duplicate section", "/mona/LinkedList/1.0.0", []part{archivePart(good), archivePart(good)}, nil, 400, "source-archive"},
			{
				"signature without format",
				"/mona/LinkedList/1.0.0",
				[]part{
					archivePart(good),
					{name: "source-archive-signature", body: []byte("sig")},
				},
				nil,
				422,
				"X-Swift-Package-Signature-Format",
			},
			{
				"bad signature format", "/mona/LinkedList/1.0.0", []part{archivePart(good)},
				[]string{"X-Swift-Package-Signature-Format", "cms 1"}, 400, "X-Swift-Package-Signature-Format",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				w := f.publish(t, tc.path, tc.parts, tc.headers...)
				problem(t, w, tc.status, tc.detail)

				if w.Header().Get("Content-Version") != "1" {
					t.Errorf("Content-Version = %q", w.Header().Get("Content-Version"))
				}
			})
		}

		if len(f.db.rows) != 0 || f.db.used != 0 || len(f.bs.Blobs) != 0 {
			t.Errorf("refused releases left rows = %d, quota = %d, blobs = %d", len(f.db.rows), f.db.used, len(f.bs.Blobs))
		}
	})

	t.Run("not multipart", func(t *testing.T) {
		f := newFixture(t)
		problem(
			t,
			f.do(http.MethodPut, root+"/mona/LinkedList/1.0.0", good, "Content-Type", "application/zip"),
			http.StatusUnsupportedMediaType,
			"multipart",
		)
	})

	t.Run("truncated body", func(t *testing.T) {
		f := newFixture(t)
		body, ct := form(t, archivePart(good))
		problem(
			t,
			f.do(http.MethodPut, root+"/mona/LinkedList/1.0.0", body[:len(body)/2], "Content-Type", ct),
			http.StatusBadRequest,
			"multipart",
		)
	})

	t.Run("too large announced", func(t *testing.T) {
		f := newFixture(t)

		req := formattest.Request(http.MethodPut, root+"/mona/LinkedList/1.0.0", strings.NewReader("x"))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		req.ContentLength = maxArchiveBytes + 4*maxPartBytes + formOverhead

		problem(t, formattest.Do(f.h.RegisterRepoRoutes, req), http.StatusRequestEntityTooLarge, "too large")
	})

	t.Run("quota", func(t *testing.T) {
		f := newFixture(t)
		f.db.limit = 10

		problem(t, f.publish(t, "/mona/LinkedList/1.0.0", []part{archivePart(good)}), http.StatusInsufficientStorage, "quota")

		if len(f.db.rows) != 0 || len(f.bs.Blobs) != 0 {
			t.Errorf("rows = %d, blobs = %d", len(f.db.rows), len(f.bs.Blobs))
		}
	})

	t.Run("no temp files left", func(t *testing.T) {
		check := formattest.IsolatedTemp(t)
		f := newFixture(t)

		f.published(t, "1.0.0", nil, "")
		f.publish(t, "/mona/LinkedList/1.0.0", []part{archivePart(good)})
		f.publish(t, "/mona/LinkedList/1.0.1", []part{archivePart([]byte("hello"))})
		check()
	})
}

func TestPublishEncodingsAndSignature(t *testing.T) {
	f := newFixture(t)

	data := swifttest.Package(t, "", "LinkedList", nil)

	// The sections as the specification's example sends them: the archive and
	// the signatures base64 encoded, the metadata quoted-printable.
	w := f.publish(t, "/mona/LinkedList/1.1.1", []part{
		{name: "source-archive", body: data, encoding: "base64"},
		{name: "source-archive-signature", body: []byte("archive-signature"), encoding: "base64"},
		{name: "metadata", body: []byte(`{"repositoryURLs":[]}`), encoding: "quoted-printable"},
		{name: "metadata-signature", body: []byte("metadata-signature"), encoding: "base64"},
		{name: "ignored", body: []byte("x")},
	}, "X-Swift-Package-Signature-Format", "cms-1.0.0")
	if w.Code != http.StatusCreated {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	if loc := w.Header().Get("Location"); loc != "http://registry.test/repos/swift/myrepo/mona/LinkedList/1.1.1" {
		t.Errorf("Location = %q", loc)
	}

	if w.Header().Get("Content-Version") != "1" {
		t.Errorf("Content-Version = %q", w.Header().Get("Content-Version"))
	}

	if row := f.db.rows[0]; row.SHA256 != sum(data) || row.SignatureFormat != "cms-1.0.0" {
		t.Fatalf("row = %+v", row)
	}

	doc := jsonBody(t, f.do(http.MethodGet, root+"/mona/LinkedList/1.1.1", nil))
	res, _ := doc["resources"].([]any)
	archive, _ := res[0].(map[string]any)
	signing, _ := archive["signing"].(map[string]any)

	if signing["signatureBase64Encoded"] != base64.StdEncoding.EncodeToString([]byte("archive-signature")) ||
		signing["signatureFormat"] != "cms-1.0.0" {
		t.Errorf("signing = %v", signing)
	}

	z := f.do(http.MethodGet, root+"/mona/LinkedList/1.1.1.zip", nil)
	if z.Header().Get("X-Swift-Package-Signature-Format") != "cms-1.0.0" ||
		z.Header().Get("X-Swift-Package-Signature") != base64.StdEncoding.EncodeToString([]byte("archive-signature")) {
		t.Errorf("signature headers = %v", z.Header())
	}

	// An unsigned release has none of them.
	f.published(t, "1.1.2", nil, "")

	if u := f.do(http.MethodGet, root+"/mona/LinkedList/1.1.2.zip", nil); u.Header().Get("X-Swift-Package-Signature") != "" {
		t.Errorf("unsigned release carries a signature: %v", u.Header())
	}

	if r := jsonBody(t, f.do(http.MethodGet, root+"/mona/LinkedList/1.1.2", nil)); r["resources"] == nil {
		t.Errorf("unsigned release = %v", r)
	} else if rs, _ := r["resources"].([]any); rs[0].(map[string]any)["signing"] != nil {
		t.Errorf("unsigned release has signing: %v", rs[0])
	}
}

func TestAcceptNegotiation(t *testing.T) {
	f := newFixture(t)
	f.published(t, "1.0.0", nil, "")

	tests := []struct {
		accept string
		want   int
	}{
		{"", http.StatusOK}, // processed as version 1
		{swift.MediaJSON, http.StatusOK},
		{"application/json", http.StatusOK},
		{"*/*", http.StatusOK},
		{"application/vnd.swift.registry+json", http.StatusOK},
		{"application/vnd.swift.registry.v2+json", http.StatusUnsupportedMediaType},
		{"application/vnd.swift.registry.v0+json", http.StatusUnsupportedMediaType},
		{"application/vnd.swift.registry.vX+json", http.StatusBadRequest},
	}

	for _, tc := range tests {
		req := formattest.Request(http.MethodGet, root+"/mona/LinkedList", nil)
		if tc.accept != "" {
			req.Header.Set("Accept", tc.accept)
		}

		w := formattest.Do(f.h.RegisterRepoRoutes, req)
		if w.Code != tc.want {
			t.Errorf("Accept %q = %d, want %d", tc.accept, w.Code, tc.want)
		}

		if w.Header().Get("Content-Version") != "1" {
			t.Errorf("Accept %q: Content-Version = %q", tc.accept, w.Header().Get("Content-Version"))
		}
	}
}

func TestRepositoryOfAnotherFormat(t *testing.T) {
	f := newFixture(t)
	f.db.Repo.Format = "raw"

	problem(t, f.do(http.MethodGet, root+"/mona/LinkedList", nil), http.StatusNotFound, "repository not found")
	problem(t, f.do(http.MethodGet, "/repos/other/mona/LinkedList", nil), http.StatusNotFound, "repository not found")
}

func TestLogin(t *testing.T) {
	f := newFixture(t)

	as := func(p *auth.Principal) int {
		req := formattest.Request(http.MethodPost, root+"/login", nil)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

		return formattest.Do(f.h.RegisterRepoRoutes, req).Code
	}

	if got := as(&auth.Principal{Username: "mona"}); got != http.StatusOK {
		t.Errorf("login as a user = %d, want 200", got)
	}

	anon := formattest.Request(http.MethodPost, root+"/login", nil)
	anon = anon.WithContext(middleware.WithPrincipal(anon.Context(), &auth.Principal{IsAnonymous: true}))

	w := formattest.Do(f.h.RegisterRepoRoutes, anon)
	problem(t, w, http.StatusUnauthorized, "authentication")

	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 without a WWW-Authenticate challenge")
	}
}

func TestDeleteRelease(t *testing.T) {
	f := newFixture(t)

	data := f.published(t, "1.0.0", nil, "")
	f.published(t, "1.0.1", nil, "")

	if w := f.do(http.MethodDelete, root+"/mona/LinkedList/1.0.0", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}

	if w := f.do(http.MethodGet, root+"/mona/LinkedList/1.0.0", nil); w.Code != http.StatusNotFound {
		t.Errorf("deleted release = %d, want 404", w.Code)
	}

	versions, _ := f.db.ListSwiftReleaseVersions(context.Background(), "", "mona", "LinkedList")
	if len(versions) != 1 || versions[0].Version != "1.0.1" {
		t.Errorf("versions = %+v", versions)
	}

	// The archive and the quota are given back; the other release is intact.
	if f.db.used != int64(len(data)) {
		t.Errorf("quota used = %d, want %d (one release left)", f.db.used, len(data))
	}

	if evs := f.pub.Events(); evs[len(evs)-1].Type != webhook.EventArtifactDeleted {
		t.Errorf("last event = %+v", evs[len(evs)-1])
	}

	problem(t, f.do(http.MethodDelete, root+"/mona/LinkedList/1.0.0", nil), http.StatusNotFound, "not found")

	// The release can be published again.
	f.published(t, "1.0.0", nil, "")
}

func TestQuarantinedArchiveIsRefused(t *testing.T) {
	f := newFixture(t)
	f.published(t, "1.0.0", nil, "")

	get := formattest.StatusOf(f.h.RegisterRepoRoutes)

	if got := get(root + "/mona/LinkedList/1.0.0.zip"); got != http.StatusOK {
		t.Fatalf("archive = %d", got)
	}

	formattest.AssertQuarantinedByID(
		t,
		&f.db.Quarantine,
		f.db.rows[0].ID,
		get,
		root+"/mona/LinkedList/1.0.0.zip",
		root+"/mona/LinkedList/1.0.0",
	)
}

func TestRevalidation(t *testing.T) {
	f := newFixture(t)
	f.published(t, "1.0.0", nil, "")

	for _, p := range []string{"/mona/LinkedList", "/mona/LinkedList/1.0.0", "/mona/LinkedList/1.0.0/Package.swift", "/mona/LinkedList/1.0.0.zip"} {
		w := f.do(http.MethodGet, root+p, nil)

		etag := w.Header().Get("ETag")
		if etag == "" {
			t.Errorf("%s has no ETag", p)
			continue
		}

		if n := f.do(http.MethodGet, root+p, nil, "If-None-Match", etag); n.Code != http.StatusNotModified {
			t.Errorf("%s with its ETag = %d, want 304", p, n.Code)
		}
	}
}
