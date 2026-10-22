// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

const testModule = "github.com/example/mymodule"

// gomodDB stores modules in insertion order and lists them back unsorted, as
// the database does: the handler, not the query, ranks versions.
type gomodDB struct {
	*formattest.DB
	quotaFake

	mods []*models.GoModule

	// createErr, when set, is returned by CreateGoModule.
	createErr error

	// zipBlobs maps a ZipHash to the blob key of the first zip stored under it:
	// the blobs table is unique per digest, so CreateGoModule's upsert hands
	// back the existing blob for a digest it has seen (an empty one included).
	zipBlobs map[string]string
}

func (d *gomodDB) CreateGoModule(_ context.Context, m *models.GoModule) error {
	if d.createErr != nil {
		return d.createErr
	}

	if d.zipBlobs == nil {
		d.zipBlobs = map[string]string{}
	}

	if existing, ok := d.zipBlobs[m.ZipHash]; ok {
		m.ZipBlobKey = existing
	} else {
		d.zipBlobs[m.ZipHash] = m.ZipBlobKey
	}

	d.mods = append(d.mods, m)

	return nil
}

func (d *gomodDB) GetGoModule(_ context.Context, _, modulePath, version string) (*models.GoModule, error) {
	for _, m := range d.mods {
		if m.ModulePath == modulePath && m.Version == version {
			return m, nil
		}
	}

	return nil, nil
}

func (d *gomodDB) ListGoModuleVersions(_ context.Context, _, modulePath string) ([]*models.GoModule, error) {
	var out []*models.GoModule

	for _, m := range d.mods {
		if m.ModulePath == modulePath {
			out = append(out, m)
		}
	}

	return out, nil
}

// seed stores one module version per entry, in the order given.
func (d *gomodDB) seed(modulePath string, versions ...string) {
	for _, v := range versions {
		d.mods = append(d.mods, &models.GoModule{
			ModulePath:   modulePath,
			Version:      v,
			GoModContent: "module " + modulePath + "\n",
		})
	}
}

func newTestRouter(t *testing.T) (http.Handler, *gomodDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &gomodDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(sub chi.Router) {
		h.RegisterRepoRoutes(sub)
		sub.Put("/*", h.HandlePut)
	})

	return r, db, bs, pub
}

func serve(router http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, formattest.Request(method, target, bytes.NewReader(body)))

	return w
}

// moduleZip builds a module zip the way `go mod` lays it out: every file under
// "<module>@<version>/", the module's go.mod among them.
func moduleZip(t *testing.T, modulePath, version string) []byte {
	t.Helper()

	return createGoModZip(t, map[string]string{
		modulePath + "@" + version + "/go.mod":  "module " + modulePath + "\n\ngo 1.21\n",
		modulePath + "@" + version + "/main.go": "package mymodule\n",
	})
}

// latestInfo fetches <module>/@latest and returns the reported version.
func latestInfo(t *testing.T, router http.Handler, module string) string {
	t.Helper()

	w := serve(router, http.MethodGet, "/repos/myrepo/"+module+"/@latest", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("@latest status = %d: %s", w.Code, w.Body.String())
	}

	var info struct{ Version string }
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("@latest body %q: %v", w.Body.String(), err)
	}

	return info.Version
}

func TestUploadEmitsEventAndServesMultiSegmentModule(t *testing.T) {
	router, db, bs, pub := newTestRouter(t)

	zipData := moduleZip(t, testModule, "v1.0.0")

	if w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/v1.0.0", zipData); w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != testModule || a["version"] != "v1.0.0" {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.mods[0].ZipBlobKey]; !ok {
		t.Fatal("zip blob not stored")
	}

	if w := serve(router, http.MethodGet, "/repos/myrepo/"+testModule+"/@v/list", nil); w.Code != http.StatusOK || w.Body.String() != "v1.0.0" {
		t.Fatalf("list = %d %q", w.Code, w.Body.String())
	}
}

// Every version serves the zip that was uploaded for it. The zip blob is
// identified by its digest, which the upload must hand to the database.
func TestEachVersionServesItsOwnZip(t *testing.T) {
	router, db, _, _ := newTestRouter(t)

	zips := map[string][]byte{
		"v1.0.0": moduleZip(t, testModule, "v1.0.0"),
		"v1.1.0": moduleZip(t, testModule, "v1.1.0"),
	}

	for _, v := range []string{"v1.0.0", "v1.1.0"} {
		if w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/"+v, zips[v]); w.Code != http.StatusCreated {
			t.Fatalf("upload %s = %d: %s", v, w.Code, w.Body.String())
		}
	}

	if len(db.mods) != 2 || db.mods[0].ZipBlobKey == db.mods[1].ZipBlobKey {
		t.Fatalf("two different zips share one blob: %+v", db.mods)
	}

	for v, want := range zips {
		w := serve(router, http.MethodGet, "/repos/myrepo/"+testModule+"/@v/"+v+".zip", nil)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
			t.Errorf("%s.zip = %d, %d bytes; want the %d bytes uploaded for %s", v, w.Code, w.Body.Len(), len(want), v)
		}

		sum := sha256.Sum256(want)
		if got := w.Header().Get("ETag"); got != `"`+hex.EncodeToString(sum[:])+`"` {
			t.Errorf("%s.zip ETag = %s, want the zip digest", v, got)
		}
	}
}

// A row whose zip blob has no real sha256 (published by <= 0.4.1, when every
// zip shared one blobs row keyed by "") must never be served: the object behind
// it belongs to another module, possibly in another organization.
func TestZipWithInvalidHashFailsClosed(t *testing.T) {
	validHash := strings.Repeat("a", 64)

	for name, hash := range map[string]string{
		"empty":     "",
		"prefixed":  "sha256:" + validHash,
		"uppercase": strings.ToUpper(validHash),
		"short":     "abc",
	} {
		t.Run(name, func(t *testing.T) {
			router, db, bs, _ := newTestRouter(t)
			db.mods = []*models.GoModule{{
				ModulePath: testModule, Version: "v1.0.0", ZipBlobKey: validHash, ZipHash: hash,
				ZipSizeBytes: 14, GoModContent: "module x",
			}}
			bs.Blobs[validHash] = []byte("ANOTHER ORG ZIP")

			w := serve(router, http.MethodGet, "/repos/myrepo/"+testModule+"/@v/v1.0.0.zip", nil)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", w.Code)
			}

			if bytes.Contains(w.Body.Bytes(), []byte("ANOTHER")) {
				t.Fatalf("leaked bytes: %q", w.Body.String())
			}
		})
	}
}

func TestZipQuarantine(t *testing.T) {
	router, db, bs, _ := newTestRouter(t)
	zipKey := strings.Repeat("ab", 32)
	db.mods = []*models.GoModule{{ModulePath: testModule, Version: "v1.0.0", ZipBlobKey: zipKey, ZipHash: zipKey, ZipSizeBytes: 3, GoModContent: "module x"}}
	bs.Blobs[zipKey] = []byte("xyz")

	url := "/repos/myrepo/" + testModule + "/@v/v1.0.0.zip"

	if w := serve(router, http.MethodGet, url, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	db.Keys[testModule+"/@v/v1.0.0.zip"] = true

	if w := serve(router, http.MethodGet, url, nil); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

// The database hands versions back in publish order (any order, really); the
// proxy must rank them by semantic version. A string comparison puts v1.9.0
// above v1.10.0 and a prerelease above its release.
func TestLatestAndListRankVersionsBySemver(t *testing.T) {
	router, db, _, _ := newTestRouter(t)
	// Lexically descending, i.e. exactly what ORDER BY version DESC returned.
	db.seed(testModule, "v1.9.0", "v1.10.0-rc1", "v1.10.0", "v1.2.0")

	if got := latestInfo(t, router, testModule); got != "v1.10.0" {
		t.Errorf("@latest = %q, want v1.10.0", got)
	}

	w := serve(router, http.MethodGet, "/repos/myrepo/"+testModule+"/@v/list", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}

	want := "v1.10.0\nv1.10.0-rc1\nv1.9.0\nv1.2.0"
	if got := w.Body.String(); got != want {
		t.Errorf("list = %q, want %q (highest first)", got, want)
	}
}

// @latest is the highest release; a prerelease is only the answer when the
// module has no release at all.
func TestLatestPrefersReleaseOverNewerPrerelease(t *testing.T) {
	router, db, _, _ := newTestRouter(t)
	db.seed(testModule, "v1.0.0", "v2.0.0-beta.1")

	if got := latestInfo(t, router, testModule); got != "v1.0.0" {
		t.Errorf("@latest = %q, want the release v1.0.0 over the prerelease v2.0.0-beta.1", got)
	}
}

func TestLatestFallsBackToHighestPrerelease(t *testing.T) {
	router, db, _, _ := newTestRouter(t)
	// Lexically "rc.10" < "rc.2"; by SemVer precedence the numeric field 10 > 2.
	db.seed(testModule, "v1.0.0-rc.2", "v1.0.0-rc.10", "v0.0.0-20240102150405-0123456789ab")

	if got := latestInfo(t, router, testModule); got != "v1.0.0-rc.10" {
		t.Errorf("@latest = %q, want v1.0.0-rc.10", got)
	}
}

func TestLatestUnknownModuleIs404(t *testing.T) {
	router, db, _, _ := newTestRouter(t)
	db.seed("github.com/example/other", "v1.0.0")

	if w := serve(router, http.MethodGet, "/repos/myrepo/"+testModule+"/@latest", nil); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// Go case-encodes uppercase letters in URL paths ("!r" = "R"): both the module
// path and the version. The stored values are the decoded ones, and everything
// answers to the encoded URL the go command builds.
func TestCaseEncodedModuleAndVersion(t *testing.T) {
	router, db, _, pub := newTestRouter(t)

	const (
		module  = "github.com/Example/MyModule"
		version = "v1.0.0-RC1"
		base    = "/repos/myrepo/github.com/!example/!my!module/@v/"
		escVer  = "v1.0.0-!r!c1"
	)

	if w := serve(router, http.MethodPut, base+escVer, moduleZip(t, module, version)); w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	if len(db.mods) != 1 || db.mods[0].ModulePath != module || db.mods[0].Version != version {
		t.Fatalf("stored = %+v, want %s@%s (decoded)", db.mods, module, version)
	}

	if a := pub.Events()[0].Artifact(); a["name"] != module || a["version"] != version {
		t.Errorf("event artifact = %v, want decoded name and version", a)
	}

	// The info body carries the plain version, not the encoded one.
	w := serve(router, http.MethodGet, base+escVer+".info", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Version":"`+version+`"`) {
		t.Fatalf(".info = %d %q", w.Code, w.Body.String())
	}

	if w := serve(router, http.MethodGet, base+escVer+".mod", nil); w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "module "+module) {
		t.Fatalf(".mod = %d %q", w.Code, w.Body.String())
	}

	if w := serve(router, http.MethodGet, base+escVer+".zip", nil); w.Code != http.StatusOK {
		t.Fatalf(".zip status = %d", w.Code)
	}

	if w := serve(router, http.MethodGet, base+"list", nil); w.Code != http.StatusOK || w.Body.String() != version {
		t.Fatalf("list = %d %q, want the plain version", w.Code, w.Body.String())
	}

	if got := latestInfo(t, router, "github.com/!example/!my!module"); got != version {
		t.Errorf("@latest = %q, want %q", got, version)
	}

	// An unencoded uppercase version is not a valid GOPROXY path.
	if w := serve(router, http.MethodGet, base+version+".info", nil); w.Code != http.StatusNotFound {
		t.Errorf("unencoded version .info status = %d, want 404", w.Code)
	}
}

func TestUploadRejectsWrongZipOrVersion(t *testing.T) {
	const otherMod = "module github.com/other/thing\n"

	cases := []struct {
		name   string
		target string
		zip    map[string]string
	}{
		{
			name:   "module line differs from the URL module",
			target: testModule + "/@v/v1.0.0",
			zip:    map[string]string{testModule + "@v1.0.0/go.mod": otherMod},
		},
		{
			name:   "only a file that merely ends in go.mod",
			target: testModule + "/@v/v1.0.0",
			zip:    map[string]string{testModule + "@v1.0.0/foo-go.mod": sampleGoMod},
		},
		{
			name:   "go.mod outside the module prefix",
			target: testModule + "/@v/v1.0.0",
			zip:    map[string]string{"mymodule@v1.0.0/go.mod": sampleGoMod},
		},
		{
			name:   "zip prefix names another version",
			target: testModule + "/@v/v1.0.0",
			zip:    map[string]string{testModule + "@v1.0.1/go.mod": sampleGoMod},
		},
		{
			name:   "go.mod without a module line",
			target: testModule + "/@v/v1.0.0",
			zip:    map[string]string{testModule + "@v1.0.0/go.mod": "go 1.21\n"},
		},
		{
			name:   "version is not a canonical semver",
			target: testModule + "/@v/v1.0",
			zip:    map[string]string{testModule + "@v1.0/go.mod": sampleGoMod},
		},
		{
			name:   "version is not a version at all",
			target: testModule + "/@v/latest",
			zip:    map[string]string{testModule + "@latest/go.mod": sampleGoMod},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			router, db, _, pub := newTestRouter(t)

			w := serve(router, http.MethodPut, "/repos/myrepo/"+c.target, createGoModZip(t, c.zip))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}

			if len(db.mods) != 0 || len(pub.Events()) != 0 {
				t.Errorf("rejected upload left state behind: mods=%d events=%d", len(db.mods), len(pub.Events()))
			}
		})
	}
}

// The nested module's go.mod must not be mistaken for the root one.
func TestUploadUsesRootGoModNotNestedOne(t *testing.T) {
	router, db, _, _ := newTestRouter(t)

	zipData := createGoModZip(t, map[string]string{
		testModule + "@v1.0.0/sub/go.mod": "module github.com/other/nested\n",
		testModule + "@v1.0.0/go.mod":     sampleGoMod,
	})

	if w := serve(router, http.MethodPut, "/repos/myrepo/"+testModule+"/@v/v1.0.0", zipData); w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	if len(db.mods) != 1 || db.mods[0].GoModContent != sampleGoMod {
		t.Errorf("stored go.mod = %+v, want the root one", db.mods)
	}
}

func TestUploadDuplicateVersionIs409AndKeepsOriginal(t *testing.T) {
	router, db, bs, _ := newTestRouter(t)
	url := "/repos/myrepo/" + testModule + "/@v/v1.0.0"

	if w := serve(router, http.MethodPut, url, moduleZip(t, testModule, "v1.0.0")); w.Code != http.StatusCreated {
		t.Fatalf("first upload = %d", w.Code)
	}

	blobs := len(bs.Blobs)
	other := createGoModZip(t, map[string]string{
		testModule + "@v1.0.0/go.mod":  "module " + testModule + "\n\ngo 1.21\n",
		testModule + "@v1.0.0/main.go": "package mymodule // different\n",
	})

	if w := serve(router, http.MethodPut, url, other); w.Code != http.StatusConflict {
		t.Fatalf("duplicate upload = %d, want 409", w.Code)
	}

	if len(db.mods) != 1 || len(bs.Blobs) != blobs {
		t.Errorf("mods=%d blobs=%d (was %d): duplicate left state behind", len(db.mods), len(bs.Blobs), blobs)
	}
}

func TestUploadCreateFailureCleansBlobsAndRaceIs409(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{errors.New("boom"), http.StatusInternalServerError}, {database.ErrDuplicate, http.StatusConflict}} {
		router, db, bs, _ := newTestRouter(t)
		db.createErr = tc.err
		url := "/repos/myrepo/" + testModule + "/@v/v1.0.0"

		if w := serve(router, http.MethodPut, url, moduleZip(t, testModule, "v1.0.0")); w.Code != tc.want {
			t.Errorf("%v: status = %d, want %d", tc.err, w.Code, tc.want)
		}

		if len(bs.Blobs) != 0 {
			t.Errorf("%v: %d orphan blobs left", tc.err, len(bs.Blobs))
		}
	}
}

func TestQuarantinedVersionHiddenFromListLatestInfo(t *testing.T) {
	router, db, _, _ := newTestRouter(t)
	db.seed(testModule, "v1.0.0", "v1.1.0")
	db.Keys[testModule+"/@v/v1.1.0.zip"] = true
	base := "/repos/myrepo/" + testModule

	if w := serve(router, http.MethodGet, base+"/@v/v1.1.0.info", nil); w.Code != http.StatusForbidden {
		t.Errorf(".info status = %d, want 403", w.Code)
	}

	if w := serve(router, http.MethodGet, base+"/@v/list", nil); strings.TrimSpace(w.Body.String()) != "v1.0.0" {
		t.Errorf("list = %q, want only v1.0.0", w.Body.String())
	}

	if got := latestInfo(t, router, testModule); got != "v1.0.0" {
		t.Errorf("@latest = %q, want v1.0.0", got)
	}
}
