// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/httpcache"
)

func (d *groupDB) ListCondaPackageNames(_ context.Context, repoID string) ([]string, error) {
	return d.condaNames[repoID], d.condaErr
}

func (d *groupDB) HasCondaPackageName(_ context.Context, repoID, name string) (bool, error) {
	return slices.Contains(d.condaNames[repoID], name), d.condaErr
}

// condaMemberDoc is what a fake conda member serves for a path.
type condaMemberDoc struct {
	status int
	body   []byte
	etag   string
}

// condaFixture is a conda group over fake members. A member serves its docs
// like a real one: a GET with If-None-Match answers 304 when the ETag matches,
// and the number of times a body was sent is counted.
type condaFixture struct {
	*fixture

	docs   map[string]map[string]condaMemberDoc
	bodies map[string]int // member name -> bodies sent
	asked  map[string][]string
	db     *groupDB
}

func newCondaFixture(t *testing.T, members ...*models.Repository) *condaFixture {
	t.Helper()

	f := &condaFixture{
		fixture: newFixture(t, "conda", members...),
		docs:    map[string]map[string]condaMemberDoc{},
		bodies:  map[string]int{},
		asked:   map[string][]string{},
	}
	f.grp.MergeProxyVersions = false

	f.db = f.h.db.(*groupDB)
	f.db.condaNames = map[string][]string{}

	return f
}

func (f *condaFixture) put(member, path string, status int, body []byte, etag string) {
	if f.docs[member] == nil {
		f.docs[member] = map[string]condaMemberDoc{}
	}

	f.docs[member][path] = condaMemberDoc{status: status, body: body, etag: etag}
}

func (f *condaFixture) backend() Backend {
	return Backend{
		Authorize: func(_ *http.Request, m *models.Repository, _ string) bool { return !f.denied[m.Name] },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
			f.asked[m.Name] = append(f.asked[m.Name], p)

			doc, ok := f.docs[m.Name][p]
			if !ok {
				http.NotFound(w, r)
				return
			}

			if doc.status != http.StatusOK {
				http.Error(w, "member failure", doc.status)
				return
			}

			if doc.etag != "" {
				w.Header().Set("ETag", doc.etag)

				if httpcache.ETagMatches(r.Header.Get("If-None-Match"), doc.etag) {
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}

			f.bodies[m.Name]++

			_, _ = w.Write(doc.body)
		},
	}
}

func (f *condaFixture) request(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, path, f.backend())

	return w
}

func (f *condaFixture) get(path string) *httptest.ResponseRecorder {
	return f.request(http.MethodGet, path, nil)
}

func cEntry(name string) string {
	return fmt.Sprintf(`{"name":%q,"version":"1.0","build":"0","build_number":0,"sha256":"%064d","size":1}`, name, 0)
}

// cRepodata builds a repodata.json from "file=name" pairs in .conda and
// .tar.bz2 sections.
func cRepodata(subdir string, files ...string) []byte {
	sections := map[string][]string{"packages": nil, "packages.conda": nil}

	for _, f := range files {
		file, name, _ := strings.Cut(f, "=")

		key := "packages.conda"
		if strings.HasSuffix(file, ".tar.bz2") {
			key = "packages"
		}

		sections[key] = append(sections[key], fmt.Sprintf("%q:%s", file, cEntry(name)))
	}

	return fmt.Appendf(nil, `{"info":{"subdir":%q},"packages":{%s},"packages.conda":{%s}}`,
		subdir, strings.Join(sections["packages"], ","), strings.Join(sections["packages.conda"], ","))
}

func cZstd(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = enc.Write(b)
	_ = enc.Close()

	return buf.Bytes()
}

func cDecode(t *testing.T, body []byte) (map[string]json.RawMessage, map[string]json.RawMessage, map[string]string) {
	t.Helper()

	var doc struct {
		Info     map[string]string          `json:"info"`
		Packages map[string]json.RawMessage `json:"packages"`
		Conda    map[string]json.RawMessage `json:"packages.conda"` //nolint:tagliatelle // conda repodata wire format
	}

	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("not a repodata document: %v\n%s", err, body)
	}

	return doc.Packages, doc.Conda, doc.Info
}

func condaKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

// condaGroup is a hosted member (internal-lib in noarch, demo) and a proxy
// (the same demo, internal-lib 9.9.9 and a package of its own).
func condaGroup(t *testing.T) *condaFixture {
	t.Helper()

	f := newCondaFixture(t, member("hosted", "conda", "hosted"), member("mirror", "conda", "proxy"))

	f.put("hosted", "linux-64/repodata.json", 200, cRepodata("linux-64", "demo-1.0-0.conda=demo"), `"h1"`)
	f.put("hosted", "noarch/repodata.json", 200, cRepodata("noarch", "internal-lib-1.0-0.conda=internal-lib"), `"h2"`)
	f.db.condaNames["id-hosted"] = []string{"demo", "internal-lib"}

	proxy := cRepodata("linux-64",
		"demo-1.0-0.conda=demo", "demo-2.0-0.conda=demo", "internal-lib-9.9.9-0.conda=internal-lib",
		"liar-1.0-0.conda=internal-lib", "numpy-1.0-0.conda=numpy", "numpy-0.9-0.tar.bz2=numpy")
	f.put("mirror", "linux-64/repodata.json.zst", 200, cZstd(t, proxy), `"m1"`)
	f.put("mirror", "linux-64/repodata.json", 200, proxy, `"m1-plain"`)

	return f
}

func TestCondaGroup_RepodataMergesHostedWins(t *testing.T) {
	f := condaGroup(t)

	w := f.get("linux-64/repodata.json")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}

	if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Errorf("Content-Length %q, body %d", w.Header().Get("Content-Length"), w.Body.Len())
	}

	tarbz2, conda, info := cDecode(t, w.Body.Bytes())

	if info["subdir"] != "linux-64" {
		t.Errorf("info %v", info)
	}

	// demo: the hosted copy only (the proxy's 2.0 is hidden); internal-lib lives
	// in noarch on the hosted member, yet its proxy copies are hidden here too;
	// liar's entry names a hosted package.
	if got := strings.Join(condaKeys(conda), " "); got != "demo-1.0-0.conda numpy-1.0-0.conda" {
		t.Errorf("packages.conda = %s", got)
	}

	if got := strings.Join(condaKeys(tarbz2), " "); got != "numpy-0.9-0.tar.bz2" {
		t.Errorf("packages = %s", got)
	}

	// The proxy was read through its compressed copy.
	if !slices.Contains(f.asked["mirror"], "linux-64/repodata.json.zst") || slices.Contains(f.asked["mirror"], "linux-64/repodata.json") {
		t.Errorf("proxy asked %v", f.asked["mirror"])
	}
}

func TestCondaGroup_MergeProxyVersionsKeepsTheUnion(t *testing.T) {
	f := condaGroup(t)
	f.grp.MergeProxyVersions = true

	_, conda, _ := cDecode(t, f.get("linux-64/repodata.json").Body.Bytes())

	for _, want := range []string{"demo-2.0-0.conda", "internal-lib-9.9.9-0.conda", "liar-1.0-0.conda"} {
		if _, ok := conda[want]; !ok {
			t.Errorf("%s missing from the union: %v", want, condaKeys(conda))
		}
	}
}

func TestCondaGroup_ZstdIsTheSameDocument(t *testing.T) {
	f := condaGroup(t)

	plain := f.get("linux-64/repodata.json")
	zst := f.get("linux-64/repodata.json.zst")

	if zst.Code != http.StatusOK || zst.Header().Get("Content-Type") != "application/zstd" {
		t.Fatalf("%d %s", zst.Code, zst.Header().Get("Content-Type"))
	}

	dec, err := zstd.NewReader(bytes.NewReader(zst.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	out, err := io.ReadAll(dec)
	if err != nil || !bytes.Equal(out, plain.Body.Bytes()) {
		t.Fatalf("decompressed zst differs from the plain document (%v)", err)
	}

	if plain.Header().Get("ETag") == zst.Header().Get("ETag") {
		t.Error("the encodings share a validator")
	}
}

func TestCondaGroup_RevalidationDoesNotReadTheMembers(t *testing.T) {
	f := condaGroup(t)

	first := f.get("linux-64/repodata.json.zst")
	etag := first.Header().Get("ETag")

	if etag == "" || first.Header().Get("Cache-Control") == "no-store" {
		t.Fatalf("ETag %q, Cache-Control %q", etag, first.Header().Get("Cache-Control"))
	}

	f.bodies = map[string]int{}

	for _, inm := range []string{etag, "*"} {
		w := f.request(http.MethodGet, "linux-64/repodata.json.zst", map[string]string{"If-None-Match": inm})
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("If-None-Match %s: %d with %d bytes", inm, w.Code, w.Body.Len())
		}
	}

	if f.bodies["hosted"]+f.bodies["mirror"] != 0 {
		t.Errorf("members were read to answer a revalidation: %v", f.bodies)
	}

	if w := f.request(
		http.MethodHead,
		"linux-64/repodata.json.zst",
		nil,
	); w.Code != http.StatusOK || w.Body.Len() != 0 ||
		f.bodies["mirror"] != 0 {
		t.Errorf("HEAD: %d, %d bytes, bodies %v", w.Code, w.Body.Len(), f.bodies)
	}

	// The validator tracks the members' and the hosted names.
	f.put("mirror", "linux-64/repodata.json.zst", 200, f.docs["mirror"]["linux-64/repodata.json.zst"].body, `"m2"`)

	if got := f.get("linux-64/repodata.json.zst").Header().Get("ETag"); got == etag {
		t.Error("a member's new ETag did not change the group's")
	}

	etag = f.get("linux-64/repodata.json.zst").Header().Get("ETag")
	f.db.condaNames["id-hosted"] = append(f.db.condaNames["id-hosted"], "numpy")

	if got := f.get("linux-64/repodata.json.zst").Header().Get("ETag"); got == etag {
		t.Error("a new hosted name in another platform did not change the group's validator")
	}
}

func TestCondaGroup_ProxyWithoutZstdFallsBackToJSONAndHostedIsOnlyAskedForJSON(t *testing.T) {
	f := newCondaFixture(t, member("hosted", "conda", "hosted"), member("mirror", "conda", "proxy"))
	f.put("hosted", "noarch/repodata.json", 200, cRepodata("noarch", "a-1.0-0.conda=a"), `"h"`)
	f.put("mirror", "noarch/repodata.json", 200, cRepodata("noarch", "b-1.0-0.conda=b"), `"m"`)

	w := f.get("noarch/repodata.json")
	_, got, _ := cDecode(t, w.Body.Bytes())

	if strings.Join(condaKeys(got), " ") != "a-1.0-0.conda b-1.0-0.conda" {
		t.Errorf("merged %v", condaKeys(got))
	}

	if !slices.Equal(f.asked["hosted"], []string{"noarch/repodata.json", "noarch/repodata.json"}) {
		t.Errorf("hosted asked %v", f.asked["hosted"])
	}
}

func TestCondaGroup_CurrentRepodata(t *testing.T) {
	f := newCondaFixture(t, member("hosted", "conda", "hosted"), member("mirror", "conda", "proxy"))
	f.put("hosted", "noarch/repodata.json", 200, cRepodata("noarch", "a-1.0-0.conda=a"), `"h"`)
	f.put("mirror", "noarch/current_repodata.json.zst", 200, cZstd(t, cRepodata("noarch", "b-2.0-0.conda=b")), `"m"`)

	_, got, _ := cDecode(t, f.get("noarch/current_repodata.json").Body.Bytes())
	if strings.Join(condaKeys(got), " ") != "a-1.0-0.conda b-2.0-0.conda" {
		t.Errorf("current_repodata %v", condaKeys(got))
	}
}

func TestCondaGroup_FailingMemberYieldsAPartialUncachedDocument(t *testing.T) {
	f := condaGroup(t)
	f.put("mirror", "linux-64/repodata.json.zst", http.StatusBadGateway, nil, "")

	w := f.get("linux-64/repodata.json")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
		t.Fatalf("%d, Cache-Control %q, ETag %q", w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
	}

	_, conda, _ := cDecode(t, w.Body.Bytes())
	if got := strings.Join(condaKeys(conda), " "); got != "demo-1.0-0.conda" {
		t.Errorf("packages.conda = %s", got)
	}

	f.put("hosted", "linux-64/repodata.json", http.StatusInternalServerError, nil, "")

	if w := f.get("linux-64/repodata.json"); w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "member failure") {
		t.Errorf("every member failing: %d %q", w.Code, w.Body.String())
	}
}

func TestCondaGroup_UnreadableMemberBodyIsPartialNotCached(t *testing.T) {
	f := condaGroup(t)
	f.put("mirror", "linux-64/repodata.json.zst", 200, []byte("<html>portal</html>"), `"m1"`)

	w := f.get("linux-64/repodata.json")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
		t.Fatalf("%d, Cache-Control %q, ETag %q", w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
	}

	if _, conda, _ := cDecode(t, w.Body.Bytes()); len(conda) != 1 {
		t.Errorf("hosted member lost: %v", condaKeys(conda))
	}
}

func TestCondaGroup_AbsentEverywhereIs404(t *testing.T) {
	f := condaGroup(t)

	if w := f.get("osx-arm64/repodata.json"); w.Code != http.StatusNotFound {
		t.Errorf("no member has the subdir: %d, want 404", w.Code)
	}
}

func TestCondaGroup_HostedNamesLookupFailureFailsClosed(t *testing.T) {
	f := condaGroup(t)
	f.db.condaErr = errors.New("db down")

	if w := f.get("linux-64/repodata.json"); w.Code != http.StatusBadGateway {
		t.Errorf("repodata with unreadable hosted names: %d, want 502", w.Code)
	}

	if w := f.get("linux-64/internal-lib-9.9.9-0.conda"); w.Code != http.StatusBadGateway {
		t.Errorf("file with unreadable hosted names: %d, want 502", w.Code)
	}
}

func TestCondaGroup_UnservedPathsAreNotFoundWithoutAskingMembers(t *testing.T) {
	f := condaGroup(t)

	for _, p := range []string{
		"linux-64/repodata.json.bz2", "linux-64/repodata.jlap", "linux-64/repodata_shards.msgpack.zst", "channeldata.json",
		"linux-64/", "../repodata.json", "repodata.json", "index.html",
	} {
		if w := f.get(p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked: %v", f.asked)
	}
}

func TestCondaGroup_PackageFilesFollowHostedWins(t *testing.T) {
	f := condaGroup(t)

	f.put("hosted", "linux-64/demo-1.0-0.conda", 200, []byte("hosted demo"), "")
	f.put("mirror", "linux-64/demo-1.0-0.conda", 200, []byte("mirror demo"), "")
	f.put("mirror", "linux-64/demo-2.0-0.conda", 200, []byte("mirror demo 2"), "")
	f.put("mirror", "linux-64/internal-lib-9.9.9-0.conda", 200, []byte("evil"), "")
	f.put("mirror", "linux-64/numpy-1.0-0.conda", 200, []byte("numpy"), "")
	f.put("hosted", "noarch/internal-lib-1.0-0.conda", 200, []byte("internal"), "")

	for path, want := range map[string]string{
		"linux-64/demo-1.0-0.conda":           "hosted demo", // the hosted file first
		"noarch/internal-lib-1.0-0.conda":     "internal",
		"linux-64/numpy-1.0-0.conda":          "numpy", // a name no hosted member holds
		"linux-64/internal-lib-9.9.9-0.conda": "",      // hosted name in another platform
		"linux-64/demo-2.0-0.conda":           "",      // hosted name, proxy-only version
	} {
		w := f.get(path)
		if want == "" {
			if w.Code != http.StatusNotFound {
				t.Errorf("%s: %d %q, want 404", path, w.Code, w.Body.String())
			}

			continue
		}

		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s: %d %q, want %q", path, w.Code, w.Body.String(), want)
		}
	}

	f.grp.MergeProxyVersions = true

	if w := f.get("linux-64/demo-2.0-0.conda"); w.Body.String() != "mirror demo 2" {
		t.Errorf("merge_proxy_versions: %q", w.Body.String())
	}
}

func TestCondaGroup_PrivateHostedMemberDoesNotHideOrLeak(t *testing.T) {
	f := condaGroup(t)
	f.denied["hosted"] = true

	_, conda, _ := cDecode(t, f.get("linux-64/repodata.json").Body.Bytes())
	if _, ok := conda["demo-2.0-0.conda"]; !ok {
		t.Errorf("an unreadable hosted member hid the proxy's package: %v", condaKeys(conda))
	}

	if _, ok := conda["demo-1.0-0.conda"]; !ok || len(f.asked["hosted"]) != 0 {
		t.Errorf("members asked: %v", f.asked)
	}
}

func TestCondaGroup_WritesAreRefused(t *testing.T) {
	f := condaGroup(t)

	if w := f.request(http.MethodPost, "linux-64", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
}
