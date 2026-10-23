// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/webhook"
)

const (
	recipeURL = "/repos/myrepo/v2/conans/zlib/1.3/_/_"
	rrev1     = "9b4de2c1f0e8a7d65c3b2a1908f7e6d5"
	rrev2     = "0aa1bb2cc3dd4ee5ff6a7b8c9d0e1f2a"
	pid       = "d4f0ab9e8c7b6a5948372615f0e1d2c3b4a59687"
	prev1     = "5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
)

func recipeFileURL(rrev, name string) string {
	return recipeURL + "/revisions/" + rrev + "/files/" + name
}

func packageURL(rrev, id string) string {
	return recipeURL + "/revisions/" + rrev + "/packages/" + id
}

func packageFileURL(rrev, id, prev, name string) string {
	return packageURL(rrev, id) + "/revisions/" + prev + "/files/" + name
}

type env struct {
	t   *testing.T
	h   *HTTPHandler
	db  *conanDB
	bs  *formattest.BlobStore
	pub *formattest.Publisher
}

func newEnv(t *testing.T) *env {
	t.Helper()

	db := newConanDB()
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return &env{t: t, h: h, db: db, bs: bs, pub: pub}
}

// do serves one request through the handler's routes.
func (e *env) do(method, target, body string) *httptest.ResponseRecorder {
	e.t.Helper()

	return formattest.Do(e.h.RegisterRepoRoutes, formattest.Request(method, target, strings.NewReader(body)))
}

// doAs is do with an authenticated principal on the request context.
func (e *env) doAs(p *auth.Principal, method, target, user, password string) *httptest.ResponseRecorder {
	e.t.Helper()

	req := formattest.Request(method, target, nil)
	if p != nil {
		req = req.WithContext(middleware.WithPrincipal(context.Background(), p))
	}

	if user != "" {
		req.SetBasicAuth(user, password)
	}

	return formattest.Do(e.h.RegisterRepoRoutes, req)
}

func (e *env) mustDo(method, target, body string, want int) *httptest.ResponseRecorder {
	e.t.Helper()

	w := e.do(method, target, body)
	if w.Code != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, target, w.Code, want, w.Body.String())
	}

	return w
}

// publishRecipe uploads the files `conan upload` sends for a recipe revision,
// in its order (sorted, manifest last).
func (e *env) publishRecipe(rrev string) {
	e.t.Helper()

	for _, f := range []string{"conan_export.tgz", "conan_sources.tgz", "conanfile.py", "conanmanifest.txt"} {
		e.mustDo(http.MethodPut, recipeFileURL(rrev, f), f+"@"+rrev, http.StatusCreated)
	}
}

// publishPackage uploads the files of one package revision.
func (e *env) publishPackage(rrev, id, prev string) {
	e.t.Helper()

	for _, f := range []string{"conan_package.tgz", "conaninfo.txt", "conanmanifest.txt"} {
		e.mustDo(http.MethodPut, packageFileURL(rrev, id, prev, f), f+"@"+prev, http.StatusCreated)
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (the conan client rejects anything else)", ct)
	}

	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}

	return v
}

type revisionsResponse struct {
	Revisions []struct {
		Revision string `json:"revision"`
		Time     string `json:"time"`
	} `json:"revisions"`
}

func TestPingAdvertisesRevisionsAndChecksumDeploy(t *testing.T) {
	e := newEnv(t)

	for _, p := range []string{"/repos/myrepo/v1/ping", "/repos/myrepo/v2/ping"} {
		w := e.mustDo(http.MethodGet, p, "", http.StatusOK)
		if got := w.Header().Get("X-Conan-Server-Capabilities"); got != "revisions,checksum_deploy" {
			t.Errorf("%s capabilities = %q, want revisions,checksum_deploy", p, got)
		}
	}
}

func TestAuthenticateReturnsTheBasicPasswordAsToken(t *testing.T) {
	e := newEnv(t)
	user := &auth.Principal{Username: "alice", Credential: auth.CredentialBasicToken}
	url := "/repos/myrepo/v2/users/authenticate"

	w := e.doAs(user, http.MethodGet, url, "alice", "oat_secret")
	if w.Code != http.StatusOK || w.Body.String() != "oat_secret" {
		t.Fatalf("authenticate = %d %q, want 200 oat_secret", w.Code, w.Body.String())
	}

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}

	for name, p := range map[string]*auth.Principal{
		"anonymous":    {IsAnonymous: true, Credential: auth.CredentialNone},
		"bearer":       {Username: "alice", Credential: auth.CredentialBearerToken},
		"session":      {Username: "alice", Credential: auth.CredentialSession},
		"legacy key":   {Username: "system/legacy-api-key", IsLegacy: true, Credential: auth.CredentialLegacy},
		"no principal": nil,
	} {
		if w := e.doAs(p, http.MethodGet, url, "alice", "oat_secret"); w.Code != http.StatusUnauthorized {
			t.Errorf("authenticate as %s = %d, want 401", name, w.Code)
		}
	}
}

func TestCheckCredentialsReturnsTheUserName(t *testing.T) {
	e := newEnv(t)
	url := "/repos/myrepo/v2/users/check_credentials"

	w := e.doAs(&auth.Principal{Username: "alice", Credential: auth.CredentialBearerToken}, http.MethodGet, url, "", "")
	if w.Code != http.StatusOK || w.Body.String() != "alice" {
		t.Fatalf("check_credentials = %d %q, want 200 alice", w.Code, w.Body.String())
	}

	if w := e.doAs(&auth.Principal{IsAnonymous: true}, http.MethodGet, url, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous check_credentials = %d, want 401", w.Code)
	}
}

// TestUploadSequenceStoresEveryFile is the regression test for CONAN-1: a conan
// client uploads four recipe files and three package files; each used to be
// stored under one (name, version, user/channel, revision "0") key, so the
// second file failed.
func TestUploadSequenceStoresEveryFile(t *testing.T) {
	e := newEnv(t)

	// Nothing is published yet.
	e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusNotFound)
	e.mustDo(http.MethodGet, recipeURL+"/revisions", "", http.StatusNotFound)

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)

	if got := e.db.count(); got != 7 {
		t.Fatalf("stored rows = %d, want 7 (one per file)", got)
	}

	if len(e.bs.Blobs) != 7 {
		t.Errorf("stored blobs = %d, want 7", len(e.bs.Blobs))
	}

	latest := decode[struct{ Revision, Time string }](t, e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusOK))
	if latest.Revision != rrev1 || !strings.HasSuffix(latest.Time, "+00:00") {
		t.Errorf("latest = %+v, want %s with an ISO 8601 UTC time", latest, rrev1)
	}

	revs := decode[revisionsResponse](t, e.mustDo(http.MethodGet, recipeURL+"/revisions", "", http.StatusOK))
	if len(revs.Revisions) != 1 || revs.Revisions[0].Revision != rrev1 {
		t.Errorf("revisions = %+v", revs)
	}

	files := decode[struct {
		Files map[string]struct{ Size int64 } `json:"files"`
	}](t, e.mustDo(http.MethodGet, recipeURL+"/revisions/"+rrev1+"/files", "", http.StatusOK))
	if len(files.Files) != 4 || files.Files["conanfile.py"].Size != int64(len("conanfile.py@"+rrev1)) {
		t.Errorf("recipe files = %+v, want 4 files with their real sizes", files.Files)
	}

	// Each file downloads under its own name.
	for _, f := range []string{"conan_export.tgz", "conan_sources.tgz", "conanfile.py", "conanmanifest.txt"} {
		w := e.mustDo(http.MethodGet, recipeFileURL(rrev1, f), "", http.StatusOK)
		if w.Body.String() != f+"@"+rrev1 {
			t.Errorf("download %s = %q", f, w.Body.String())
		}
	}

	pkgFiles := decode[struct {
		Files map[string]struct{ Size int64 } `json:"files"`
	}](t, e.mustDo(http.MethodGet, packageURL(rrev1, pid)+"/revisions/"+prev1+"/files", "", http.StatusOK))
	if len(pkgFiles.Files) != 3 {
		t.Errorf("package files = %+v, want 3", pkgFiles.Files)
	}

	w := e.mustDo(http.MethodGet, packageFileURL(rrev1, pid, prev1, "conan_package.tgz"), "", http.StatusOK)
	if w.Body.String() != "conan_package.tgz@"+prev1 {
		t.Errorf("package download = %q", w.Body.String())
	}

	// A file the revision does not have, and a revision that was never uploaded.
	e.mustDo(http.MethodGet, recipeFileURL(rrev1, "nothing.txt"), "", http.StatusNotFound)
	e.mustDo(http.MethodGet, recipeURL+"/revisions/"+rrev2+"/files", "", http.StatusNotFound)
}

func TestRevisionIsPublishedWithItsManifest(t *testing.T) {
	e := newEnv(t)

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "class Z: pass", http.StatusCreated)

	// Uploaded but not published: the client must not see it yet.
	e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusNotFound)
	e.mustDo(http.MethodGet, recipeURL+"/revisions/"+rrev1+"/files", "", http.StatusNotFound)

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanmanifest.txt"), "1700000000\n", http.StatusCreated)
	e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusOK)
}

func TestPackageUploadNeedsItsRecipeRevision(t *testing.T) {
	e := newEnv(t)

	e.mustDo(http.MethodPut, packageFileURL(rrev1, pid, prev1, "conan_package.tgz"), "x", http.StatusNotFound)

	e.publishRecipe(rrev1)
	e.mustDo(http.MethodPut, packageFileURL(rrev1, pid, prev1, "conan_package.tgz"), "x", http.StatusCreated)
	e.mustDo(http.MethodPut, packageFileURL(rrev2, pid, prev1, "conan_package.tgz"), "x", http.StatusNotFound)
}

func TestLatestIsTheNewestPublishedRevision(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.publishRecipe(rrev2)

	latest := decode[struct{ Revision string }](t, e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusOK))
	if latest.Revision != rrev2 {
		t.Errorf("latest = %s, want %s", latest.Revision, rrev2)
	}

	revs := decode[revisionsResponse](t, e.mustDo(http.MethodGet, recipeURL+"/revisions", "", http.StatusOK))
	if len(revs.Revisions) != 2 || revs.Revisions[0].Revision != rrev2 || revs.Revisions[1].Revision != rrev1 {
		t.Errorf("revisions = %+v, want newest first", revs)
	}

	// Re-uploading the manifest of the older revision republishes it.
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanmanifest.txt"), "touched", http.StatusCreated)

	latest = decode[struct{ Revision string }](t, e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusOK))
	if latest.Revision != rrev1 {
		t.Errorf("latest after republish = %s, want %s", latest.Revision, rrev1)
	}
}

func TestPackageRevisionsAndLatest(t *testing.T) {
	e := newEnv(t)
	prev2 := "7a7a7a7a7a7a7a7a7a7a7a7a7a7a7a7a"

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)
	e.publishPackage(rrev1, pid, prev2)

	latest := decode[struct{ Revision string }](t, e.mustDo(http.MethodGet, packageURL(rrev1, pid)+"/latest", "", http.StatusOK))
	if latest.Revision != prev2 {
		t.Errorf("package latest = %s, want %s", latest.Revision, prev2)
	}

	revs := decode[revisionsResponse](t, e.mustDo(http.MethodGet, packageURL(rrev1, pid)+"/revisions", "", http.StatusOK))
	if len(revs.Revisions) != 2 || revs.Revisions[0].Revision != prev2 {
		t.Errorf("package revisions = %+v", revs)
	}

	e.mustDo(http.MethodGet, packageURL(rrev1, "ffffffffffffffffffffffffffffffffffffffff")+"/latest", "", http.StatusNotFound)
	e.mustDo(http.MethodGet, packageURL(rrev2, pid)+"/revisions", "", http.StatusNotFound)
}

func TestReuploadOfSameBytesIsANoOp(t *testing.T) {
	e := newEnv(t)
	e.db.Repo.ImmutableReleases = true

	e.publishRecipe(rrev1)

	rows := e.db.count()
	events := len(e.pub.Events())

	// A retried upload (or `conan upload` of a revision that is already there)
	// succeeds even when releases are immutable: nothing changes.
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "conanfile.py@"+rrev1, http.StatusCreated)

	if e.db.count() != rows || len(e.pub.Events()) != events {
		t.Errorf("an identical re-upload changed state: rows %d->%d, events %d->%d", rows, e.db.count(), events, len(e.pub.Events()))
	}
}

func TestOverwriteReplacesFileAndFreesOldBlob(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	before := len(e.bs.Blobs)

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "class Z: v2", http.StatusCreated)

	w := e.mustDo(http.MethodGet, recipeFileURL(rrev1, "conanfile.py"), "", http.StatusOK)
	if w.Body.String() != "class Z: v2" {
		t.Errorf("overwritten file = %q", w.Body.String())
	}

	if e.db.count() != 4 || len(e.bs.Blobs) != before {
		t.Errorf("rows = %d blobs = %d, want 4 and %d (the replaced blob is released)", e.db.count(), len(e.bs.Blobs), before)
	}
}

func TestImmutableReleasesRefuseOverwriteAndDelete(t *testing.T) {
	e := newEnv(t)
	e.db.Repo.ImmutableReleases = true

	e.publishRecipe(rrev1)

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "tampered", http.StatusConflict)

	w := e.mustDo(http.MethodGet, recipeFileURL(rrev1, "conanfile.py"), "", http.StatusOK)
	if w.Body.String() != "conanfile.py@"+rrev1 {
		t.Errorf("a refused overwrite changed the file: %q", w.Body.String())
	}

	e.mustDo(http.MethodDelete, recipeURL+"/revisions/"+rrev1, "", http.StatusConflict)
	e.mustDo(http.MethodDelete, recipeURL, "", http.StatusConflict)

	if e.db.count() != 4 {
		t.Errorf("rows = %d, want 4 after refused deletes", e.db.count())
	}

	// New files and new revisions are still welcome.
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "metadata/notes.txt"), "n", http.StatusCreated)
	e.publishRecipe(rrev2)
}

func TestDeleteRevisionTakesItsPackagesAndAnswers200(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)
	e.publishRecipe(rrev2)

	// The conan client treats anything but 200 as a failure.
	e.mustDo(http.MethodDelete, recipeURL+"/revisions/"+rrev1, "", http.StatusOK)

	if e.db.count() != 4 {
		t.Fatalf("rows = %d, want only revision 2's 4 files left", e.db.count())
	}

	if len(e.bs.Blobs) != 4 {
		t.Errorf("blobs = %d, want 4 after the delete released the others", len(e.bs.Blobs))
	}

	e.mustDo(http.MethodGet, recipeURL+"/revisions/"+rrev1+"/files", "", http.StatusNotFound)
	e.mustDo(http.MethodDelete, recipeURL+"/revisions/"+rrev1, "", http.StatusNotFound)

	latest := decode[struct{ Revision string }](t, e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusOK))
	if latest.Revision != rrev2 {
		t.Errorf("latest = %s, want %s", latest.Revision, rrev2)
	}

	evs := e.pub.Events()
	if last := evs[len(evs)-1]; last.Type != webhook.EventArtifactDeleted {
		t.Errorf("last event = %s, want artifact.deleted", last.Type)
	}
}

func TestDeletePackagesKeepsTheRecipe(t *testing.T) {
	e := newEnv(t)
	otherPID := "1111111111111111111111111111111111111111"

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)
	e.publishPackage(rrev1, otherPID, prev1)

	e.mustDo(http.MethodDelete, packageURL(rrev1, pid)+"/revisions/"+prev1, "", http.StatusOK)
	e.mustDo(http.MethodGet, packageURL(rrev1, pid)+"/latest", "", http.StatusNotFound)
	e.mustDo(http.MethodGet, packageURL(rrev1, otherPID)+"/latest", "", http.StatusOK)

	e.mustDo(http.MethodDelete, recipeURL+"/revisions/"+rrev1+"/packages", "", http.StatusOK)
	e.mustDo(http.MethodGet, packageURL(rrev1, otherPID)+"/latest", "", http.StatusNotFound)
	e.mustDo(http.MethodDelete, recipeURL+"/revisions/"+rrev1+"/packages", "", http.StatusNotFound)

	if e.db.count() != 4 {
		t.Errorf("rows = %d, want the 4 recipe files", e.db.count())
	}
}

func TestDeleteRecipeRemovesEveryRevision(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)
	e.publishRecipe(rrev2)

	e.mustDo(http.MethodDelete, recipeURL, "", http.StatusOK)

	if e.db.count() != 0 || len(e.bs.Blobs) != 0 {
		t.Errorf("rows = %d blobs = %d, want none", e.db.count(), len(e.bs.Blobs))
	}

	e.mustDo(http.MethodDelete, recipeURL, "", http.StatusNotFound)
}

func TestSearch(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	other := "/repos/myrepo/v2/conans/Fmt/10.0/acme/stable/revisions/" + rrev1 + "/files/conanmanifest.txt"
	e.mustDo(http.MethodPut, other, "m", http.StatusCreated)

	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"Fmt/10.0@acme/stable", "zlib/1.3"}},
		{"q=*", []string{"Fmt/10.0@acme/stable", "zlib/1.3"}},
		{"q=zlib", []string{"zlib/1.3"}},
		{"q=zlib/*", []string{"zlib/1.3"}},
		{"q=zlib/1.3", []string{"zlib/1.3"}},
		{"q=zlib/1.4", []string{}},
		{"q=fmt/*", []string{"Fmt/10.0@acme/stable"}},
		{"q=fmt/*&ignorecase=False", []string{}},
		{"q=Fmt/*&ignorecase=False", []string{"Fmt/10.0@acme/stable"}},
		{"q=*/1.?", []string{"zlib/1.3"}},
		{"q=*@acme/*", []string{"Fmt/10.0@acme/stable"}},
		{"q=[zy]lib", []string{"zlib/1.3"}},
		{"q=[!z]*", []string{"Fmt/10.0@acme/stable"}},
		{"q=nothing*", []string{}},
	}

	for _, tc := range cases {
		w := e.mustDo(http.MethodGet, "/repos/myrepo/v2/conans/search?"+tc.query, "", http.StatusOK)

		got := decode[struct{ Results []string }](t, w).Results
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("search %q = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestSearchPackages(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)

	want := map[string]map[string]string{pid: {"content": "conaninfo.txt@" + prev1}}

	for _, target := range []string{recipeURL + "/search", recipeURL + "/revisions/" + rrev1 + "/search"} {
		got := decode[map[string]map[string]string](t, e.mustDo(http.MethodGet, target, "", http.StatusOK))
		if got[pid]["content"] != want[pid]["content"] || len(got) != 1 {
			t.Errorf("%s = %v, want %v", target, got, want)
		}
	}

	listed := decode[map[string]map[string]string](t, e.mustDo(http.MethodGet, recipeURL+"/search?list_only=True", "", http.StatusOK))
	if len(listed) != 1 || len(listed[pid]) != 0 {
		t.Errorf("list_only = %v, want {pid: {}}", listed)
	}

	// A recipe revision without packages is an empty (not missing) result.
	e.publishRecipe(rrev2)

	empty := decode[map[string]map[string]string](t, e.mustDo(http.MethodGet, recipeURL+"/revisions/"+rrev2+"/search", "", http.StatusOK))
	if len(empty) != 0 {
		t.Errorf("search of a revision without packages = %v, want {}", empty)
	}

	e.mustDo(http.MethodGet, "/repos/myrepo/v2/conans/other/1.0/_/_/search", "", http.StatusNotFound)
}

func TestHeadReturnsHeadersOnly(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	w := e.mustDo(http.MethodHead, recipeFileURL(rrev1, "conanfile.py"), "", http.StatusOK)
	if w.Body.Len() != 0 {
		t.Errorf("HEAD body = %q", w.Body.String())
	}

	if got, want := w.Header().Get("Content-Length"), strconv.Itoa(len("conanfile.py@"+rrev1)); got != want {
		t.Errorf("Content-Length = %q, want %s", got, want)
	}

	e.mustDo(http.MethodHead, recipeFileURL(rrev1, "missing"), "", http.StatusNotFound)
}

func TestMetadataFilesKeepTheirSubdirectory(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "metadata/sign/signature.json"), "sig", http.StatusCreated)

	w := e.mustDo(http.MethodGet, recipeFileURL(rrev1, "metadata/sign/signature.json"), "", http.StatusOK)
	if w.Body.String() != "sig" {
		t.Errorf("metadata download = %q", w.Body.String())
	}

	files := decode[struct{ Files map[string]any }](t, e.mustDo(http.MethodGet, recipeURL+"/revisions/"+rrev1+"/files", "", http.StatusOK))
	if _, ok := files.Files["metadata/sign/signature.json"]; !ok {
		t.Errorf("files = %v, want the metadata path", files.Files)
	}
}

func TestMalformedAddressesAreRejected(t *testing.T) {
	e := newEnv(t)

	for _, target := range []string{
		recipeFileURL(rrev1, "a/../b"),
		recipeFileURL(rrev1, "a//b"),
		recipeFileURL(rrev1, "%2e%2e/b"),
		recipeFileURL(rrev1, "a%00b"),
		recipeFileURL("bad%20rev", "conanfile.py"),
		recipeFileURL(strings.Repeat("a", 65), "conanfile.py"),
		"/repos/myrepo/v2/conans/zlib/" + strings.Repeat("v", 101) + "/_/_/revisions/" + rrev1 + "/files/conanfile.py",
	} {
		if w := e.do(http.MethodPut, target, "x"); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", target, w.Code)
		}
	}

	if e.db.count() != 0 {
		t.Errorf("rows = %d, want none", e.db.count())
	}
}

func TestUploadAndDeleteEmitEvents(t *testing.T) {
	e := newEnv(t)

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "data", http.StatusCreated)

	evs := e.pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "zlib" || a["version"] != "1.3" || a["path"] != "zlib/1.3/_/_/conanfile.py" {
		t.Errorf("artifact = %v", a)
	}

	e.mustDo(http.MethodDelete, recipeURL, "", http.StatusOK)

	evs = e.pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}

	if len(e.bs.Blobs) != 0 {
		t.Error("blob not removed after delete")
	}
}

func TestDownloadQuarantine(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	e.mustDo(http.MethodGet, recipeFileURL(rrev1, "conan_export.tgz"), "", http.StatusOK)

	e.db.Keys["zlib/1.3/_/_/conan_export.tgz"] = true

	e.mustDo(http.MethodGet, recipeFileURL(rrev1, "conan_export.tgz"), "", http.StatusForbidden)
	e.mustDo(http.MethodGet, recipeFileURL(rrev1, "conanfile.py"), "", http.StatusOK)
}
