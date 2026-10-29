// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
	"github.com/openartifact/openartifact/pkg/composer/composertest"
)

// composerDB is an in-memory composer_packages table with its live unique key.
type composerDB struct {
	*formattest.DB

	rows []*models.ComposerPackage

	limit, used int64
}

func (d *composerDB) ReserveStorageUsage(_ context.Context, _ string, size int64, _ int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size

	return true, nil
}

func (d *composerDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.used -= size
	return nil
}

func (d *composerDB) CreateComposerPackage(_ context.Context, p *models.ComposerPackage) error {
	for _, r := range d.rows {
		if r.Name == p.Name && r.VersionNormalized == p.VersionNormalized {
			return database.ErrDuplicate
		}
	}

	p.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", len(d.rows)+1)
	p.CreatedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(len(d.rows)) * time.Hour)
	p.UpdatedAt = p.CreatedAt
	d.rows = append(d.rows, p)

	return nil
}

func (d *composerDB) GetComposerPackage(_ context.Context, _, name, normalized string) (*models.ComposerPackage, error) {
	for _, r := range d.rows {
		if r.Name == name && r.VersionNormalized == normalized {
			return r, nil
		}
	}

	return nil, nil
}

func (d *composerDB) ListComposerPackageVersions(_ context.Context, _, name string) ([]*models.ComposerPackage, error) {
	var out []*models.ComposerPackage

	for _, r := range d.rows {
		if r.Name == name {
			out = append(out, r)
		}
	}

	return out, nil
}

func (d *composerDB) ListComposerPackageNames(_ context.Context, _ string) ([]*models.ComposerPackageName, error) {
	var names []*models.ComposerPackageName

	for _, r := range d.rows {
		var meta struct {
			Description string `json:"description"`
		}

		_ = json.Unmarshal(r.ComposerJSON, &meta)

		if i := slices.IndexFunc(names, func(n *models.ComposerPackageName) bool { return n.Name == r.Name }); i >= 0 {
			names[i].Description = meta.Description
			continue
		}

		names = append(names, &models.ComposerPackageName{Name: r.Name, Description: meta.Description, UpdatedAt: r.UpdatedAt})
	}

	slices.SortFunc(names, func(a, b *models.ComposerPackageName) int { return strings.Compare(a.Name, b.Name) })

	return names, nil
}

func (d *composerDB) DeleteComposerPackage(_ context.Context, _, id string) error {
	d.rows = slices.DeleteFunc(d.rows, func(r *models.ComposerPackage) bool { return r.ID == id })
	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *composerDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &composerDB{DB: formattest.NewDB()}
	db.Repo.Format = FormatName

	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost:8080")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

// newHandlerOnly is newTestHandler for tests that need the handler alone.
func newHandlerOnly(t *testing.T) *HTTPHandler {
	t.Helper()

	h, _, _, _ := newTestHandler(t) //nolint:dogsled // the other fixtures are not needed

	return h
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

const root = "/repos/myrepo/"

func upload(h *HTTPHandler, name, version string, zip []byte) *httptest.ResponseRecorder {
	return do(h, http.MethodPut, root+"packages/upload/"+name+"/"+version, zip)
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not JSON (%d): %s", w.Code, w.Body.String())
	}

	return doc
}

// versions reads a p2 document, expanded, and returns the versions of name.
func versions(t *testing.T, w *httptest.ResponseRecorder, name string) []pkgcomposer.Version {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("metadata = %d: %s", w.Code, w.Body.String())
	}

	pkgs, err := pkgcomposer.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	return pkgs[name]
}

var acme = composertest.Spec{ //nolint:gochecknoglobals // read-only test fixture
	Name: "acme/tools", License: "MIT", Description: "Tools", Require: map[string]string{"php": ">=8.1"},
}

//nolint:gocyclo,cyclop // one scenario: upload, read the metadata, download, delete
func TestUploadReadDownloadDelete(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	stable := composertest.Build(t, acme)
	dev := composertest.Build(t, composertest.Spec{Name: "acme/tools", Dir: "acme-tools-abc", Description: "Dev tools"})

	w := upload(h, "acme/tools", "v1.2.0", stable)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", w.Code, w.Body.String())
	}

	wantSHA1 := sha1.Sum(stable)
	wantSHA256 := sha256.Sum256(stable)

	row := db.rows[0]
	if row.Version != "v1.2.0" || row.VersionNormalized != "1.2.0.0" || row.SHA1 != hex.EncodeToString(wantSHA1[:]) ||
		row.SHA256 != hex.EncodeToString(wantSHA256[:]) {
		t.Fatalf("row = %+v", row)
	}

	if db.used != int64(len(stable)) || len(bs.Blobs) != 1 {
		t.Errorf("quota used = %d (want %d), blobs = %d", db.used, len(stable), len(bs.Blobs))
	}

	if evs := pub.Events(); len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].Artifact()["name"] != "acme/tools" {
		t.Errorf("events = %+v", evs)
	}

	// A branch, spelled the short way, and a numeric dev version.
	if w := upload(h, "acme/tools", "master", dev); w.Code != http.StatusCreated {
		t.Fatalf("upload master = %d: %s", w.Code, w.Body.String())
	}

	if w := upload(h, "acme/tools", "2.x-dev", composertest.Build(t, composertest.Spec{Name: "acme/tools"})); w.Code != http.StatusCreated {
		t.Fatalf("upload 2.x-dev = %d: %s", w.Code, w.Body.String())
	}

	if db.rows[1].Version != "dev-master" {
		t.Errorf("branch version = %q, want dev-master", db.rows[1].Version)
	}

	// packages.json names the metadata URL and every package.
	doc := decode(t, do(h, http.MethodGet, root+"packages.json", nil))
	if doc["metadata-url"] != "http://localhost:8080/repos/myrepo/p2/%package%.json" ||
		fmt.Sprint(doc["available-packages"]) != "[acme/tools]" {
		t.Errorf("packages.json = %v", doc)
	}

	// The stable document has the release, the dev document the two dev versions.
	vs := versions(t, do(h, http.MethodGet, root+"p2/acme/tools.json", nil), "acme/tools")
	if len(vs) != 1 || vs[0]["version"] != "v1.2.0" || vs[0]["version_normalized"] != "1.2.0.0" || vs[0]["description"] != "Tools" {
		t.Fatalf("stable versions = %v", vs)
	}

	dist, _ := vs[0]["dist"].(map[string]any)
	wantURL := "http://localhost:8080/repos/myrepo/dists/acme/tools/v1.2.0/" + row.SHA256 + ".zip"

	if dist["type"] != "zip" || dist["url"] != wantURL || dist["shasum"] != row.SHA1 || dist["reference"] != row.SHA256 {
		t.Errorf("dist = %v", dist)
	}

	devs := versions(t, do(h, http.MethodGet, root+"p2/acme/tools~dev.json", nil), "acme/tools")
	if len(devs) != 2 || devs[0]["version"] != "2.x-dev" || devs[1]["version"] != "dev-master" || devs[1]["description"] != "Dev tools" {
		t.Fatalf("dev versions = %v", devs)
	}

	// The dist streams the exact bytes; the wrong reference is a 404.
	if w := do(h, http.MethodGet, wantURL, nil); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), stable) ||
		w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("dist = %d, %d bytes", w.Code, w.Body.Len())
	}

	if w := do(h, http.MethodGet, root+"dists/acme/tools/v1.2.0/"+strings.Repeat("0", 64)+".zip", nil); w.Code != http.StatusNotFound {
		t.Errorf("dist with a wrong reference = %d, want 404", w.Code)
	}

	// Delete: the version leaves the metadata and the quota is released.
	if w := do(h, http.MethodDelete, root+"packages/upload/acme/tools/1.2.0", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}

	if w := do(h, http.MethodGet, root+"p2/acme/tools.json", nil); w.Code != http.StatusNotFound {
		t.Errorf("stable metadata after delete = %d, want 404", w.Code)
	}

	if w := do(h, http.MethodDelete, root+"packages/upload/acme/tools/1.2.0", nil); w.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", w.Code)
	}
}

func TestUploadRejects(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	good := composertest.Build(t, acme)

	if w := upload(h, "acme/tools", "1.0.0", good); w.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", w.Code, w.Body.String())
	}

	for _, c := range []struct {
		name    string
		target  string
		version string
		body    []byte
		status  int
	}{
		{"duplicate", "acme/tools", "1.0.0", good, http.StatusConflict},
		{"duplicate by normalization", "acme/tools", "v1.0", good, http.StatusConflict},
		{"name mismatch", "acme/other", "1.0.0", good, http.StatusBadRequest},
		{"bad version", "acme/tools", "latest", good, http.StatusBadRequest},
		{"not a zip", "acme/tools", "1.1.0", []byte("hello"), http.StatusBadRequest},
		{"empty body", "acme/tools", "1.1.0", []byte{}, http.StatusBadRequest},
		{"declared version differs", "acme/tools", "1.1.0", composertest.Build(t, composertest.Spec{Name: "acme/tools", Version: "1.2.0"}), http.StatusBadRequest},
	} {
		if w := upload(h, c.target, c.version, c.body); w.Code != c.status {
			t.Errorf("%s: status = %d, want %d: %s", c.name, w.Code, c.status, w.Body.String())
		}
	}

	// A declared version that normalizes to the URL's is fine.
	if w := upload(
		h,
		"acme/tools",
		"1.3.0",
		composertest.Build(t, composertest.Spec{Name: "acme/tools", Version: "v1.3"}),
	); w.Code != http.StatusCreated {
		t.Errorf("matching declared version = %d: %s", w.Code, w.Body.String())
	}

	if len(db.rows) != 2 {
		t.Errorf("rows = %d, want 2", len(db.rows))
	}
}

func TestUploadQuota(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.limit = 10

	if w := upload(h, "acme/tools", "1.0.0", composertest.Build(t, acme)); w.Code == http.StatusCreated {
		t.Fatalf("upload over the quota = %d", w.Code)
	}

	if len(db.rows) != 0 || len(bs.Blobs) != 0 {
		t.Errorf("rows = %d, blobs = %d after a refused upload", len(db.rows), len(bs.Blobs))
	}
}

func TestMetadataRevalidation(t *testing.T) {
	h := newHandlerOnly(t)

	if w := upload(h, "acme/tools", "1.0.0", composertest.Build(t, acme)); w.Code != http.StatusCreated {
		t.Fatalf("upload = %d", w.Code)
	}

	for _, target := range []string{root + "packages.json", root + "p2/acme/tools.json", root + "list.json"} {
		first := do(h, http.MethodGet, target, nil)

		etag := first.Header().Get("ETag")
		if first.Code != http.StatusOK || etag == "" {
			t.Fatalf("%s = %d, ETag %q", target, first.Code, etag)
		}

		if cc := first.Header().Get("Cache-Control"); strings.Contains(cc, "public") {
			t.Errorf("%s: Cache-Control %q for an authenticated read", target, cc)
		}

		if w := do(h, http.MethodGet, target, nil, "If-None-Match", etag); w.Code != http.StatusNotModified {
			t.Errorf("%s with the ETag = %d, want 304", target, w.Code)
		}
	}
}

func TestListAndSearch(t *testing.T) {
	h := newHandlerOnly(t)

	upload(h, "acme/tools", "1.0.0", composertest.Build(t, acme))
	upload(h, "acme/other", "1.0.0", composertest.Build(t, composertest.Spec{Name: "acme/other"}))
	upload(h, "zeta/lib", "1.0.0", composertest.Build(t, composertest.Spec{Name: "zeta/lib", Description: "Parses TOOLS files"}))

	for target, want := range map[string]string{
		root + "list.json":                "[acme/other acme/tools zeta/lib]",
		root + "list.json?filter=acme/*":  "[acme/other acme/tools]",
		root + "list.json?filter=ACME/T*": "[acme/tools]",
		root + "list.json?filter=nope":    "[]",
	} {
		w := do(h, http.MethodGet, target, nil)
		if got := fmt.Sprint(decode(t, w)["packageNames"]); got != want {
			t.Errorf("%s = %s, want %s", target, got, want)
		}
	}

	doc := decode(t, do(h, http.MethodGet, root+"search.json?q=tools", nil))
	if doc["total"] != float64(2) {
		t.Errorf("search total = %v, want 2 (name and description matches): %v", doc["total"], doc)
	}
}

func TestWrongMethodsAndFormats(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	if w := do(h, http.MethodPost, root+"packages.json", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST packages.json = %d, want 405", w.Code)
	}

	if w := do(h, http.MethodGet, root+"packages/upload/acme/tools/1.0.0", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET upload = %d, want 405", w.Code)
	}

	for _, p := range []string{"p2/Acme/Tools.json", "p2/acme/tools.txt", "nothing"} {
		if w := do(h, http.MethodGet, root+p, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}

	db.Repo.Format = "raw"

	if w := do(h, http.MethodGet, root+"packages.json", nil); w.Code != http.StatusNotFound {
		t.Errorf("packages.json of a raw repository = %d, want 404", w.Code)
	}
}

// TestGuardClaimsOnlyComposerRepositories drives the guard the way the server
// mounts it, in front of another format's catch-all route.
func TestGuardClaimsOnlyComposerRepositories(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		r.Use(h.Guard)
		r.Get("/*", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Other", "1") })
	})

	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, formattest.Request(http.MethodGet, target, nil))

		return w
	}

	if w := get(root + "packages.json"); w.Code != http.StatusOK || w.Header().Get("X-Other") != "" {
		t.Errorf("packages.json through the guard = %d", w.Code)
	}

	if w := get(root + "some/other/path"); w.Header().Get("X-Other") != "1" {
		t.Error("a path that is not a composer path did not reach the next handler")
	}

	db.Repo.Format = "terraform"

	if w := get(root + "p2/acme/tools.json"); w.Header().Get("X-Other") != "1" {
		t.Error("a request for a repository of another format was claimed")
	}
}
