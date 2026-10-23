// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	v1Recipe = "/repos/myrepo/v1/conans/zlib/1.3/_/_"
	v1Base   = "http://localhost/repos/myrepo/v1/files/zlib/1.3/_/_/"
)

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))

	return hex.EncodeToString(sum[:])
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))

	return hex.EncodeToString(sum[:])
}

// putURL PUTs body to an absolute URL a v1 response handed out.
func (e *env) putURL(rawURL, body string, want int) {
	e.t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		e.t.Fatalf("bad URL %q: %v", rawURL, err)
	}

	e.mustDo(http.MethodPut, u.RequestURI(), body, want)
}

// getURL GETs an absolute URL a v1 response handed out.
func (e *env) getURL(rawURL string) *httptest.ResponseRecorder {
	e.t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		e.t.Fatalf("bad URL %q: %v", rawURL, err)
	}

	return e.mustDo(http.MethodGet, u.RequestURI(), "", http.StatusOK)
}

// v1Upload uploads files the way a Conan 1 client does: ask for upload URLs,
// then PUT each file (manifest last) to its URL. It returns the URLs.
func (e *env) v1Upload(prefix string, files map[string]string) map[string]string {
	e.t.Helper()

	w := e.mustDo(http.MethodPost, prefix+"/upload_urls", sizesJSON(files), http.StatusOK)
	urls := decode[map[string]string](e.t, w)

	if len(urls) != len(files) {
		e.t.Fatalf("upload_urls returned %d urls, want %d: %v", len(urls), len(files), urls)
	}

	order := make([]string, 0, len(files))

	for name := range files {
		if name != manifestFile {
			order = append(order, name)
		}
	}

	if _, ok := files[manifestFile]; ok {
		order = append(order, manifestFile)
	}

	for _, name := range order {
		e.putURL(urls[name], files[name], http.StatusCreated)
	}

	return urls
}

// sizesJSON is the {file: size} body of upload_urls.
func sizesJSON(files map[string]string) string {
	sizes := make(map[string]int, len(files))
	for name, body := range files {
		sizes[name] = len(body)
	}

	b, err := json.Marshal(sizes)
	if err != nil {
		panic(err)
	}

	return string(b)
}

func v1RecipeFiles() map[string]string {
	return map[string]string{
		"conan_export.tgz":  "export-bytes",
		"conan_sources.tgz": "sources-bytes",
		"conanfile.py":      "class Zlib: pass",
		"conanmanifest.txt": "1700000000\nconanfile.py: abc\n",
	}
}

func v1PackageFiles() map[string]string {
	return map[string]string{
		"conan_package.tgz": "package-bytes",
		"conaninfo.txt":     "[settings]\n    os=Linux\n",
		"conanmanifest.txt": "1700000001\nconaninfo.txt: def\n",
	}
}

func TestV1UploadDownloadRoundTrip(t *testing.T) {
	e := newEnv(t)

	urls := e.v1Upload(v1Recipe, v1RecipeFiles())

	// Recipe files live under revision 0, addressed by absolute URLs.
	if want := v1Base + "0/export/conanfile.py"; urls["conanfile.py"] != want {
		t.Errorf("conanfile.py upload url = %q, want %q", urls["conanfile.py"], want)
	}

	// Published: Conan 2's view of the same repository shows revision 0.
	latest := decode[struct{ Revision string }](t, e.mustDo(http.MethodGet, recipeURL+"/latest", "", http.StatusOK))
	if latest.Revision != "0" {
		t.Errorf("v2 latest = %q, want revision 0", latest.Revision)
	}

	dl := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/download_urls", "", http.StatusOK))
	if len(dl) != len(v1RecipeFiles()) {
		t.Fatalf("download_urls = %v, want %d files", dl, len(v1RecipeFiles()))
	}

	for name, body := range v1RecipeFiles() {
		if got := e.getURL(dl[name]).Body.String(); got != body {
			t.Errorf("download %s = %q, want %q", name, got, body)
		}
	}

	digest := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/digest", "", http.StatusOK))
	if len(digest) != 1 || digest[manifestFile] != dl[manifestFile] {
		t.Errorf("digest = %v, want only the manifest url", digest)
	}

	snapshot := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe, "", http.StatusOK))
	for name, body := range v1RecipeFiles() {
		if snapshot[name] != md5Hex(body) {
			t.Errorf("snapshot[%s] = %q, want md5 %s", name, snapshot[name], md5Hex(body))
		}
	}
}

func TestV1UnknownRecipeIsNotFound(t *testing.T) {
	e := newEnv(t)

	for _, p := range []string{"", "/digest", "/download_urls", "/packages/" + pid, "/packages/" + pid + "/digest", "/packages/" + pid + "/download_urls"} {
		e.mustDo(http.MethodGet, v1Recipe+p, "", http.StatusNotFound)
	}

	e.mustDo(http.MethodGet, v1Recipe+"/search", "", http.StatusNotFound)
}

func TestV1PackageUploadFollowsTheRecipe(t *testing.T) {
	e := newEnv(t)

	// A package needs a published recipe.
	e.mustDo(http.MethodPost, v1Recipe+"/packages/"+pid+"/upload_urls", sizesJSON(v1PackageFiles()), http.StatusNotFound)

	e.v1Upload(v1Recipe, v1RecipeFiles())
	urls := e.v1Upload(v1Recipe+"/packages/"+pid, v1PackageFiles())

	if want := v1Base + "0/package/" + pid + "/0/conan_package.tgz"; urls["conan_package.tgz"] != want {
		t.Errorf("package upload url = %q, want %q", urls["conan_package.tgz"], want)
	}

	dl := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/packages/"+pid+"/download_urls", "", http.StatusOK))
	for name, body := range v1PackageFiles() {
		if got := e.getURL(dl[name]).Body.String(); got != body {
			t.Errorf("download %s = %q, want %q", name, got, body)
		}
	}

	snapshot := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/packages/"+pid, "", http.StatusOK))
	if snapshot["conaninfo.txt"] != md5Hex(v1PackageFiles()["conaninfo.txt"]) {
		t.Errorf("package snapshot = %v", snapshot)
	}

	digest := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/packages/"+pid+"/digest", "", http.StatusOK))
	if len(digest) != 1 || digest[manifestFile] == "" {
		t.Errorf("package digest = %v", digest)
	}

	e.mustDo(http.MethodGet, v1Recipe+"/packages/"+strings.Repeat("e", 40)+"/download_urls", "", http.StatusNotFound)
}

func TestV1SharesStorageWithConan2(t *testing.T) {
	e := newEnv(t)

	// A Conan 2 client publishes recipe and package revisions.
	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)

	// A Conan 1 client downloads them (the newest revision, no revisions asked).
	dl := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/download_urls", "", http.StatusOK))
	if want := v1Base + rrev1 + "/export/conanfile.py"; dl["conanfile.py"] != want {
		t.Errorf("v1 download url = %q, want %q", dl["conanfile.py"], want)
	}

	if got := e.getURL(dl["conanfile.py"]).Body.String(); got != "conanfile.py@"+rrev1 {
		t.Errorf("v1 download of a v2 file = %q", got)
	}

	pdl := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/packages/"+pid+"/download_urls", "", http.StatusOK))
	if want := v1Base + rrev1 + "/package/" + pid + "/" + prev1 + "/conan_package.tgz"; pdl["conan_package.tgz"] != want {
		t.Errorf("v1 package url = %q, want %q", pdl["conan_package.tgz"], want)
	}

	// A package uploaded by Conan 1 attaches to that newest recipe revision.
	urls := e.v1Upload(v1Recipe+"/packages/"+strings.Repeat("a", 40), v1PackageFiles())
	if want := v1Base + rrev1 + "/package/" + strings.Repeat("a", 40) + "/0/conan_package.tgz"; urls["conan_package.tgz"] != want {
		t.Errorf("package upload url = %q, want %q", urls["conan_package.tgz"], want)
	}

	// The revision of a Conan 1 recipe upload becomes the newest one.
	e.v1Upload(v1Recipe, v1RecipeFiles())

	snapshot := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe, "", http.StatusOK))
	if snapshot["conanfile.py"] != md5Hex(v1RecipeFiles()["conanfile.py"]) {
		t.Errorf("snapshot after a v1 upload = %v, want the v1 recipe", snapshot)
	}

	revs := decode[revisionsResponse](t, e.mustDo(http.MethodGet, recipeURL+"/revisions", "", http.StatusOK))
	if len(revs.Revisions) != 2 || revs.Revisions[0].Revision != "0" || revs.Revisions[1].Revision != rrev1 {
		t.Errorf("v2 revisions = %+v, want 0 then %s", revs.Revisions, rrev1)
	}
}

func TestV1UploadURLsValidateTheRequest(t *testing.T) {
	e := newEnv(t)

	for name, body := range map[string]string{
		"not json":      "nope",
		"a list":        `["conanfile.py"]`,
		"path escape":   `{"../conanfile.py": 1}`,
		"empty name":    `{"": 1}`,
		"negative size": `{"conanfile.py": -1}`,
		"string size":   `{"conanfile.py": "big"}`,
	} {
		if w := e.do(http.MethodPost, v1Recipe+"/upload_urls", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: upload_urls = %d, want 400: %s", name, w.Code, w.Body.String())
		}
	}

	e.mustDo(http.MethodPost, v1Recipe+"/upload_urls", `{"conan_export.tgz": 9999999999}`, http.StatusRequestEntityTooLarge)
	e.mustDo(http.MethodPost, "/repos/myrepo/v1/conans/zlib/1.3/_/../upload_urls", `{}`, http.StatusBadRequest)
}

func TestV1Search(t *testing.T) {
	e := newEnv(t)

	e.v1Upload(v1Recipe, v1RecipeFiles())

	got := decode[struct{ Results []string }](t, e.mustDo(http.MethodGet, "/repos/myrepo/v1/conans/search?q=zlib/*", "", http.StatusOK))
	if len(got.Results) != 1 || got.Results[0] != "zlib/1.3" {
		t.Errorf("search = %v, want [zlib/1.3]", got.Results)
	}

	// Packages of every recipe revision are listed, a newer revision's winning.
	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)
	e.v1Upload(v1Recipe+"/packages/"+strings.Repeat("b", 40), v1PackageFiles())

	infos := decode[map[string]map[string]string](t, e.mustDo(http.MethodGet, v1Recipe+"/search", "", http.StatusOK))
	if len(infos) != 2 || infos[pid]["content"] != "conaninfo.txt@"+prev1 || infos[strings.Repeat("b", 40)]["content"] != v1PackageFiles()["conaninfo.txt"] {
		t.Errorf("package search = %v, want both packages with their conaninfo", infos)
	}
}

func TestV1RemoveFilesOnlyTouchesRevisionZero(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.v1Upload(v1Recipe, v1RecipeFiles())

	e.mustDo(http.MethodPost, v1Recipe+"/remove_files", `{"files": ["conan_sources.tgz", "ghost.txt"]}`, http.StatusOK)

	e.mustDo(http.MethodGet, recipeFileURL("0", "conan_sources.tgz"), "", http.StatusNotFound)
	e.mustDo(http.MethodGet, recipeFileURL("0", "conanfile.py"), "", http.StatusOK)
	e.mustDo(http.MethodGet, recipeFileURL(rrev1, "conan_sources.tgz"), "", http.StatusOK)

	// Nothing to remove is fine: the client sends it after every overwrite.
	e.mustDo(http.MethodPost, v1Recipe+"/remove_files", `{"files": ["ghost.txt"]}`, http.StatusOK)
	e.mustDo(http.MethodPost, v1Recipe+"/remove_files", `{"files": ["../x"]}`, http.StatusBadRequest)
	e.mustDo(http.MethodPost, v1Recipe+"/remove_files", `nope`, http.StatusBadRequest)
}

func TestV1RemovePackages(t *testing.T) {
	e := newEnv(t)
	other := strings.Repeat("c", 40)

	e.v1Upload(v1Recipe, v1RecipeFiles())
	e.v1Upload(v1Recipe+"/packages/"+pid, v1PackageFiles())
	e.v1Upload(v1Recipe+"/packages/"+other, v1PackageFiles())

	e.mustDo(http.MethodPost, v1Recipe+"/packages/delete", `{"package_ids": ["`+pid+`"]}`, http.StatusOK)
	e.mustDo(http.MethodGet, v1Recipe+"/packages/"+pid+"/download_urls", "", http.StatusNotFound)
	e.mustDo(http.MethodGet, v1Recipe+"/packages/"+other+"/download_urls", "", http.StatusOK)

	// An empty list means every package.
	e.mustDo(http.MethodPost, v1Recipe+"/packages/delete", `{"package_ids": []}`, http.StatusOK)
	e.mustDo(http.MethodGet, v1Recipe+"/packages/"+other+"/download_urls", "", http.StatusNotFound)
	e.mustDo(http.MethodGet, v1Recipe+"/download_urls", "", http.StatusOK)

	// Nothing left: the client treats 404 as "no packages".
	e.mustDo(http.MethodPost, v1Recipe+"/packages/delete", `{"package_ids": []}`, http.StatusNotFound)
	e.mustDo(http.MethodPost, v1Recipe+"/packages/delete", `{"package_ids": ["a/b"]}`, http.StatusBadRequest)
}

func TestV1DeleteRecipe(t *testing.T) {
	e := newEnv(t)

	e.v1Upload(v1Recipe, v1RecipeFiles())
	e.v1Upload(v1Recipe+"/packages/"+pid, v1PackageFiles())

	e.mustDo(http.MethodDelete, v1Recipe, "", http.StatusOK)
	e.mustDo(http.MethodGet, v1Recipe+"/download_urls", "", http.StatusNotFound)
	e.mustDo(http.MethodDelete, v1Recipe, "", http.StatusNotFound)

	if e.db.count() != 0 {
		t.Errorf("rows left after deleting the recipe = %d", e.db.count())
	}
}

func TestV1AuthEndpoints(t *testing.T) {
	e := newEnv(t)
	user := &auth.Principal{Username: "alice", Credential: auth.CredentialBasicToken}

	w := e.doAs(user, http.MethodGet, "/repos/myrepo/v1/users/authenticate", "alice", "oat_secret")
	if w.Code != http.StatusOK || w.Body.String() != "oat_secret" {
		t.Errorf("v1 authenticate = %d %q, want 200 oat_secret", w.Code, w.Body.String())
	}

	if w := e.doAs(nil, http.MethodGet, "/repos/myrepo/v1/users/authenticate", "alice", "oat_secret"); w.Code != http.StatusUnauthorized {
		t.Errorf("v1 authenticate without a principal = %d, want 401", w.Code)
	}

	bearer := &auth.Principal{Username: "alice", Credential: auth.CredentialBearerToken}
	if w := e.doAs(bearer, http.MethodGet, "/repos/myrepo/v1/users/check_credentials", "", ""); w.Code != http.StatusOK || w.Body.String() != "alice" {
		t.Errorf("v1 check_credentials = %d %q, want 200 alice", w.Code, w.Body.String())
	}

	if w := e.doAs(nil, http.MethodGet, "/repos/myrepo/v1/users/check_credentials", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("v1 check_credentials without a principal = %d, want 401", w.Code)
	}
}

func TestV1UploadStoresDigests(t *testing.T) {
	e := newEnv(t)

	e.v1Upload(v1Recipe, v1RecipeFiles())

	row, _ := e.db.GetConanPackage(t.Context(), "repo-1", models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "0", Filename: "conanfile.py",
	})
	if row == nil {
		t.Fatal("conanfile.py not stored")
	}

	body := v1RecipeFiles()["conanfile.py"]
	if row.MD5 != md5Hex(body) || row.SHA1 != sha1Hex(body) {
		t.Errorf("digests = %s/%s, want %s/%s", row.MD5, row.SHA1, md5Hex(body), sha1Hex(body))
	}
}

func TestV1SnapshotFillsInDigestsOfOlderFiles(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	// Files stored before the digests were tracked have none.
	before, _ := e.db.ListConanRecipeFiles(t.Context(), "repo-1", "zlib", "1.3", "_/_")
	for _, f := range before {
		f.MD5, f.SHA1 = "", ""
		_ = e.db.SetConanPackageDigests(t.Context(), f.ID, "", "")
	}

	manifestBefore, _ := e.db.GetConanPackage(t.Context(), "repo-1", models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: rrev1, Filename: manifestFile,
	})

	snapshot := decode[map[string]string](t, e.mustDo(http.MethodGet, v1Recipe, "", http.StatusOK))
	if want := md5Hex("conanfile.py@" + rrev1); snapshot["conanfile.py"] != want {
		t.Errorf("snapshot md5 = %q, want %q", snapshot["conanfile.py"], want)
	}

	after, _ := e.db.GetConanPackage(t.Context(), "repo-1", models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: rrev1, Filename: "conanfile.py",
	})
	if after.MD5 == "" || after.SHA1 != sha1Hex("conanfile.py@"+rrev1) {
		t.Errorf("digests were not recorded: %+v", after)
	}

	manifestAfter, _ := e.db.GetConanPackage(t.Context(), "repo-1", models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: rrev1, Filename: manifestFile,
	})
	if !manifestAfter.UpdatedAt.Equal(manifestBefore.UpdatedAt) {
		t.Error("filling in digests moved the revision time")
	}
}
