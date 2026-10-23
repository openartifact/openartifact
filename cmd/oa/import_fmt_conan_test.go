// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// ─── fake destination: Conan 2 upload protocol ────────────────────────────────

// conanFakes finds the conan routes of a fakeOA from a test.
var conanFakes sync.Map //nolint:gochecknoglobals // *fakeOA -> *conanFake; the shared fake has no field for it

//nolint:gochecknoinits // adds the conan routes to every fakeOA (see fakeOARoutes)
func init() {
	fakeOARoutes["conan"] = func(f *fakeOA, mux *http.ServeMux) {
		cf := &conanFake{oa: f, files: map[string]*conanStored{}}
		conanFakes.Store(f, cf)

		const revision = "/v2/conans/{name}/{version}/{user}/{channel}/revisions/{rrev}"

		mux.HandleFunc("PUT "+revision+"/files/{file...}", cf.put)
		mux.HandleFunc("PUT "+revision+"/packages/{pid}/revisions/{prev}/files/{file...}", cf.put)

		// The URLs a Conan 1 client's upload_urls answer names.
		const v1Files = "/v1/files/{name}/{version}/{user}/{channel}/{rrev}"

		mux.HandleFunc("PUT "+v1Files+"/export/{file...}", cf.put)
		mux.HandleFunc("PUT "+v1Files+"/package/{pid}/{prev}/{file...}", cf.put)
		mux.HandleFunc("DELETE "+revision, cf.deleteRevision)
		mux.HandleFunc("DELETE "+revision+"/packages/{pid}/revisions/{prev}", cf.deleteRevision)
	}
}

type conanStored struct {
	id      string
	data    []byte
	sha     string
	created string
}

// conanFake implements what the server does for a file upload, through the v2
// routes and the v1 file URLs a Conan 1 client uploads to (the same handler
// there): a raw PUT of the file to its recipe or package revision answering 201
// {id, name, sha256}; a package revision is refused (404) until its recipe
// revision's manifest is stored; the recipe manifest is the row the package
// listing shows (one row per recipe revision). Releases may be immutable (409
// for different bytes at an existing address).
type conanFake struct {
	oa              *fakeOA
	files           map[string]*conanStored // keyed by request path below the repository
	order           []string                // keys in the order they were stored
	puts            int
	revisionDeletes int
	immutable       bool
}

func conanOf(t *testing.T, oa *fakeOA) *conanFake {
	t.Helper()

	v, _ := conanFakes.Load(oa)

	cf, ok := v.(*conanFake)
	if !ok {
		t.Fatal("no conan routes registered")
	}

	return cf
}

func (cf *conanFake) put(w http.ResponseWriter, r *http.Request) {
	f := cf.oa
	f.writes++
	cf.puts++

	name, version, user, channel, rrev := r.PathValue("name"), r.PathValue("version"), r.PathValue("user"), r.PathValue("channel"), r.PathValue("rrev")
	recipeManifest := fmt.Sprintf("/v2/conans/%s/%s/%s/%s/revisions/%s/files/conanmanifest.txt", name, version, user, channel, rrev)

	if strings.HasPrefix(r.URL.Path, "/v1/") {
		recipeManifest = fmt.Sprintf("/v1/files/%s/%s/%s/%s/%s/export/conanmanifest.txt", name, version, user, channel, rrev)
	}

	if r.PathValue("pid") != "" && cf.files[recipeManifest] == nil {
		f.reply(w, http.StatusNotFound, map[string]string{"error": "recipe revision not found"})
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		f.reply(w, http.StatusBadRequest, map[string]string{"error": "incomplete body"})
		return
	}

	key, sha := r.URL.Path, sum256(data)
	old := cf.files[key]

	if old != nil && old.sha != sha && cf.immutable {
		http.Error(w, "released versions are immutable in this repository", http.StatusConflict)
		return
	}

	st := &conanStored{data: data, sha: sha, created: r.Header.Get(headerImportCreatedAt)}
	if old != nil {
		st.id = old.id
	} else {
		st.id = f.nextID()
	}

	row := &fakeItem{sha: sha}

	if key == recipeManifest {
		row = f.store(r.Header.Get("X-Fake-Repo"), fmt.Sprintf("%s/%s@%s/%s#%s", name, version, user, channel, rrev), data, st.created)
		row.name, row.version = name, version
		st.id = row.id
	}

	cf.files[key] = st
	cf.order = append(cf.order, key)

	f.reply(w, http.StatusCreated, map[string]string{"id": st.id, "name": r.PathValue("file"), "sha256": f.reported(row)})
}

func (cf *conanFake) deleteRevision(w http.ResponseWriter, _ *http.Request) {
	cf.oa.writes++
	cf.oa.deletes++
	cf.revisionDeletes++
	w.WriteHeader(http.StatusOK)
}

// storedUnder lists the keys stored below prefix in upload order.
func (cf *conanFake) storedUnder(prefix string) []string {
	var out []string

	for _, k := range cf.order {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}

	return out
}

// ─── source ───────────────────────────────────────────────────────────────────

const (
	conanRevA  = "6a8b6c0c9d1f4a2e8f3b5d7c9e1a3b5d"
	conanRevB  = "0b2d4f6a8c0e1a3c5e7a9b1d3f5a7c9e"
	conanPIDa  = "1a2b3c4d5e6f708192a3b4c5d6e7f80912345678"
	conanPIDb  = "d0ca5e1a2b3c4d5e6f708192a3b4c5d6e7f80912"
	conanPRevA = "1f2e3d4c5b6a79881706f5e4d3c2b1a0"
	conanPRevB = "9e8d7c6b5a49382716f5e4d3c2b1a009"
)

// conanCase is a file of the source repository and the address it must end up
// at on the destination ("" when it is not importable).
type conanCase struct {
	file fakeFile
	want string
}

func conanRecipe(user, name, version, channel, rrev, file, content string, props ...string) conanCase {
	return conanCase{
		file: artiFile(strings.Join([]string{user, name, version, channel, rrev, "export", file}, "/"), content, props...),
		want: fmt.Sprintf("/v2/conans/%s/%s/%s/%s/revisions/%s/files/%s", name, version, user, channel, rrev, file),
	}
}

func conanPackage(user, name, version, channel, rrev, pid, prev, file, content string) conanCase {
	return conanCase{
		file: artiFile(strings.Join([]string{user, name, version, channel, rrev, "package", pid, prev, file}, "/"), content),
		want: fmt.Sprintf("/v2/conans/%s/%s/%s/%s/revisions/%s/packages/%s/revisions/%s/files/%s", name, version, user, channel, rrev, pid, prev, file),
	}
}

func conanIgnored(p string) conanCase { return conanCase{file: artiFile(p, "not payload")} }

// conanFixture is an Artifactory Conan 2 repository listing its files in the
// order that is worst for the destination: manifests first, package files
// before the recipe's, the package that sorts last first.
func conanFixture() []conanCase {
	const zlibA = "_/zlib/1.2.13/_/" + conanRevA

	return []conanCase{
		conanPackage("_", "zlib", "1.2.13", "_", conanRevA, conanPIDb, conanPRevB, "conanmanifest.txt", "pkg-b-manifest"),
		conanPackage("_", "zlib", "1.2.13", "_", conanRevA, conanPIDb, conanPRevB, "conan_package.tgz", "pkg-b-binary"),
		conanPackage("_", "zlib", "1.2.13", "_", conanRevA, conanPIDb, conanPRevB, "conaninfo.txt", "pkg-b-info"),
		conanPackage("_", "zlib", "1.2.13", "_", conanRevA, conanPIDa, conanPRevA, "conanmanifest.txt", "pkg-a-manifest"),
		conanPackage("_", "zlib", "1.2.13", "_", conanRevA, conanPIDa, conanPRevA, "conan_package.tgz", "pkg-a-binary"),
		conanPackage("_", "zlib", "1.2.13", "_", conanRevA, conanPIDa, conanPRevA, "conaninfo.txt", "pkg-a-info"),
		conanPackage("_", "zlib", "1.2.13", "_", conanRevA, conanPIDa, conanPRevA, "metadata/logs.txt", "pkg-a-logs"),
		conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "conanmanifest.txt", "zlib-manifest"),
		conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "metadata/info.json", "zlib-info"),
		conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "conanfile.py", "zlib-recipe"),
		conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "conandata.yml", "zlib-data"),
		conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "conan_export.tgz", "zlib-export"),
		conanRecipe("acme", "fmt", "10.1.0", "stable", conanRevB, "conanmanifest.txt", "fmt-manifest", "build.number", "42"),
		conanRecipe("acme", "fmt", "10.1.0", "stable", conanRevB, "conanfile.py", "fmt-recipe", "build.number", "43"),
		conanIgnored("_/zlib/1.2.13/_/index.json"),
		conanIgnored(zlibA + "/index.json"),
		conanIgnored(zlibA + "/package/" + conanPIDa + "/index.json"),
		conanIgnored(zlibA + "/export/.conan_tmp"),
	}
}

// conan1Want is the address a revisionless recipe or package file is PUT to:
// the v1 file URL, recipe revision "0" and package revision "0".
func conan1Want(user, name, version, channel, pid, file string) string {
	base := fmt.Sprintf("/v1/files/%s/%s/%s/%s/0/", name, version, user, channel)
	if pid == "" {
		return base + "export/" + file
	}

	return base + "package/" + pid + "/0/" + file
}

// conan1Source builds the files of a Conan 1 repository as one of the sources
// stores them.
type conan1Source struct {
	name    string
	nexus   bool
	stamp   string // the creation time the destination must receive
	payload int    // files of the fixture that must be imported
	// metadata: the source can hold files that sort after conanmanifest.txt (Nexus holds
	// only the six files of a Conan 1 upload, none of which does).
	metadata bool
	recipe   func(user, name, version, channel, file, content string, props ...string) conanCase
	pkg      func(user, name, version, channel, pid, file, content string) conanCase
	ignored  []string // paths that must not be imported
}

func conan1Sources() []conan1Source {
	return []conan1Source{
		{
			name:     "artifactory",
			stamp:    artiWant,
			payload:  14,
			metadata: true,
			recipe: func(user, name, version, channel, file, content string, props ...string) conanCase {
				p := strings.Join([]string{user, name, version, channel, "export", file}, "/")
				return conanCase{file: artiFile(p, content, props...), want: conan1Want(user, name, version, channel, "", file)}
			},
			pkg: func(user, name, version, channel, pid, file, content string) conanCase {
				p := strings.Join([]string{user, name, version, channel, "package", pid, file}, "/")
				return conanCase{file: artiFile(p, content), want: conan1Want(user, name, version, channel, pid, file)}
			},
			ignored: []string{
				"_/zlib/1.2.13/_/index.json",
				"_/zlib/1.2.13/_/export/index.json",
				"_/zlib/1.2.13/_/package/" + conanPIDa + "/index.json",
				"_/zlib/1.2.13/_/export/.conan_tmp",
			},
		},
		{
			name:    "nexus",
			nexus:   true,
			stamp:   nexusWant,
			payload: 12,
			recipe: func(user, name, version, channel, file, content string, _ ...string) conanCase {
				p := strings.Join([]string{"conans", user, name, version, channel, file}, "/")
				return conanCase{file: fakeFile{path: p, data: []byte(content), created: nexusStamp}, want: conan1Want(user, name, version, channel, "", file)}
			},
			pkg: func(user, name, version, channel, pid, file, content string) conanCase {
				p := strings.Join([]string{"conans", user, name, version, channel, "packages", pid, file}, "/")
				return conanCase{file: fakeFile{path: p, data: []byte(content), created: nexusStamp}, want: conan1Want(user, name, version, channel, pid, file)}
			},
			ignored: []string{
				"conans/_/zlib/1.2.13/_/download_urls",
				"conans/_/zlib/1.2.13/_/packages/" + conanPIDa + "/digest",
			},
		},
	}
}

// fixture is a Conan 1 repository in the order that is worst for the
// destination: manifests first, package files before the recipe's, the package
// that sorts last first.
func (s conan1Source) fixture() []conanCase {
	cases := []conanCase{
		s.pkg("_", "zlib", "1.2.13", "_", conanPIDb, "conanmanifest.txt", "pkg-b-manifest"),
		s.pkg("_", "zlib", "1.2.13", "_", conanPIDb, "conan_package.tgz", "pkg-b-binary"),
		s.pkg("_", "zlib", "1.2.13", "_", conanPIDb, "conaninfo.txt", "pkg-b-info"),
		s.pkg("_", "zlib", "1.2.13", "_", conanPIDa, "conanmanifest.txt", "pkg-a-manifest"),
		s.pkg("_", "zlib", "1.2.13", "_", conanPIDa, "conan_package.tgz", "pkg-a-binary"),
		s.pkg("_", "zlib", "1.2.13", "_", conanPIDa, "conaninfo.txt", "pkg-a-info"),
		s.recipe("_", "zlib", "1.2.13", "_", "conanmanifest.txt", "zlib-manifest"),
		s.recipe("_", "zlib", "1.2.13", "_", "conanfile.py", "zlib-recipe"),
		s.recipe("_", "zlib", "1.2.13", "_", "conan_sources.tgz", "zlib-sources"),
		s.recipe("_", "zlib", "1.2.13", "_", "conan_export.tgz", "zlib-export"),
		s.recipe("acme", "fmt", "10.1.0", "stable", "conanmanifest.txt", "fmt-manifest", "build.number", "42"),
		s.recipe("acme", "fmt", "10.1.0", "stable", "conanfile.py", "fmt-recipe", "build.number", "43"),
	}

	if s.metadata {
		cases = append(cases,
			s.pkg("_", "zlib", "1.2.13", "_", conanPIDa, "metadata/logs.txt", "pkg-a-logs"),
			s.recipe("_", "zlib", "1.2.13", "_", "metadata/info.json", "zlib-info"),
		)
	}

	for _, p := range s.ignored {
		cases = append(cases, conanCase{file: s.file(p)})
	}

	return cases
}

func (s conan1Source) file(p string) fakeFile {
	if s.nexus {
		return fakeFile{path: p, data: []byte("not payload"), created: nexusStamp}
	}

	return artiFile(p, "not payload")
}

func conanFiles(cases []conanCase) []fakeFile {
	out := make([]fakeFile, len(cases))
	for i, c := range cases {
		out[i] = c.file
	}

	return out
}

func conanPayload(cases []conanCase) []conanCase {
	return slices.DeleteFunc(slices.Clone(cases), func(c conanCase) bool { return c.want == "" })
}

type conanEnv struct {
	cases []conanCase
	stamp string // the creation time every file must arrive with
	src   *httptest.Server
	oa    *fakeOA
	cf    *conanFake
	cfg   importConfig
}

// conanNexusSource serves cases as Nexus repository "src" and returns the
// config importing it into repository "dst".
func conanNexusSource(t *testing.T, cases []conanCase, extra ...string) (*httptest.Server, importConfig) {
	t.Helper()

	src := newFakeNexus(t, fakeSourceRepo{name: "src", format: "conan", kind: "hosted", files: conanFiles(cases)})

	return src, parseCfg(t, append([]string{"nexus", "--source-url", src.URL, "--repo", "src:dst"}, extra...)...)
}

// serve serves cases the way the source stores them.
func (s conan1Source) serve(t *testing.T, cases []conanCase, extra ...string) (*httptest.Server, importConfig) {
	t.Helper()

	if s.nexus {
		return conanNexusSource(t, cases, extra...)
	}

	return conanSource(t, cases, extra...)
}

// newConan1Env imports the fixture of one source of Conan 1 content.
func newConan1Env(t *testing.T, s conan1Source, extra ...string) conanEnv {
	t.Helper()

	cases := s.fixture()
	src, cfg := s.serve(t, cases, extra...)
	oa := newFakeOA(t, map[string]*fakeRepo{"dst": {format: "conan", repoType: "hosted"}})

	return conanEnv{cases: cases, stamp: s.stamp, src: src, oa: oa, cf: conanOf(t, oa), cfg: cfg}
}

// conanSource serves cases as Artifactory repository "src" and returns the
// config importing it into repository "dst".
func conanSource(t *testing.T, cases []conanCase, extra ...string) (*httptest.Server, importConfig) {
	t.Helper()

	src := newFakeArtifactory(t, fakeSourceRepo{name: "src", format: "conan", kind: "local", files: conanFiles(cases)})
	cfg := parseCfg(t, append([]string{"artifactory", "--source-url", src.URL, "--source-user", "svc", "--repo", "src:dst"}, extra...)...)
	cfg.SourceToken = "secret"

	return src, cfg
}

func newConanEnv(t *testing.T, cases []conanCase, extra ...string) conanEnv {
	t.Helper()

	src, cfg := conanSource(t, cases, extra...)
	oa := newFakeOA(t, map[string]*fakeRepo{"dst": {format: "conan", repoType: "hosted"}})

	return conanEnv{cases: cases, stamp: artiWant, src: src, oa: oa, cf: conanOf(t, oa), cfg: cfg}
}

func (e conanEnv) run(t *testing.T) *importReport {
	t.Helper()

	return runImportTest(t, e.cfg, e.src, e.oa)
}

// assertLanded checks every payload file is stored at its address with its
// bytes and creation time, and that nothing else is.
func (e conanEnv) assertLanded(t *testing.T) {
	t.Helper()

	payload := conanPayload(e.cases)

	for _, c := range payload {
		st := e.cf.files[c.want]
		if st == nil || string(st.data) != string(c.file.data) || st.created != e.stamp {
			t.Errorf("%s -> %s: stored %+v", c.file.path, c.want, st)
		}
	}

	if len(e.cf.files) != len(payload) {
		t.Errorf("destination holds %d files, want %d", len(e.cf.files), len(payload))
	}
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestImportConanArtifactory(t *testing.T) {
	for _, listing := range []string{"aql", "storage"} {
		t.Run(listing, func(t *testing.T) {
			e := newConanEnv(t, conanFixture(), "--artifactory-listing", listing)

			rep := e.run(t)
			if rep.failed() || rep.Uploaded != len(conanPayload(e.cases)) || rep.Ignored != len(e.cases)-len(conanPayload(e.cases)) {
				t.Fatalf("report = %+v", rep)
			}

			e.assertLanded(t)
		})
	}
}

func TestImportConanListsOneRowPerRecipeRevision(t *testing.T) {
	e := newConanEnv(t, conanFixture())
	e.run(t)

	if got := len(e.oa.repos["dst"].items); got != 2 {
		t.Fatalf("listing has %d rows, want one per recipe revision", got)
	}

	for _, want := range []struct{ filename, name, version, content string }{
		{"zlib/1.2.13@_/_#" + conanRevA, "zlib", "1.2.13", "zlib-manifest"},
		{"fmt/10.1.0@acme/stable#" + conanRevB, "fmt", "10.1.0", "fmt-manifest"},
	} {
		it := e.oa.find("dst", want.filename)
		if it == nil || it.name != want.name || it.version != want.version || it.sha != sum256([]byte(want.content)) || it.created != artiWant {
			t.Errorf("row %s = %+v", want.filename, it)
		}
	}
}

func TestImportConanUploadsEachRevisionInTheOrderTheServerNeeds(t *testing.T) {
	e := newConanEnv(t, conanFixture())

	if rep := e.run(t); rep.failed() {
		t.Fatalf("report = %+v (a package before its recipe manifest is refused with 404)", rep)
	}

	rev := func(user, name, version, channel, rrev string) string {
		return fmt.Sprintf("/v2/conans/%s/%s/%s/%s/revisions/%s/", name, version, user, channel, rrev)
	}

	recipe := rev("_", "zlib", "1.2.13", "_", conanRevA)
	pkgA := recipe + "packages/" + conanPIDa + "/revisions/" + conanPRevA + "/files/"
	pkgB := recipe + "packages/" + conanPIDb + "/revisions/" + conanPRevB + "/files/"

	want := []string{
		recipe + "files/conan_export.tgz", recipe + "files/conandata.yml", recipe + "files/conanfile.py", recipe + "files/metadata/info.json", recipe + "files/conanmanifest.txt",
		pkgA + "conan_package.tgz", pkgA + "conaninfo.txt", pkgA + "metadata/logs.txt", pkgA + "conanmanifest.txt",
		pkgB + "conan_package.tgz", pkgB + "conaninfo.txt", pkgB + "conanmanifest.txt",
	}

	if got := e.cf.storedUnder(recipe); !slices.Equal(got, want) {
		t.Fatalf("upload order:\n got %v\nwant %v", got, want)
	}
}

func TestImportConanUnitsGroupRecipeRevisionsAndOrderTheirFiles(t *testing.T) {
	var assets []sourceAsset

	for _, c := range conanFixture() {
		assets = append(assets, sourceAsset{Path: c.file.path})
	}

	assets = slices.DeleteFunc(assets, func(a sourceAsset) bool { return !importable(formatConan, a.Path) })
	units := conanUnits(assets)

	if len(units) != 2 {
		t.Fatalf("%d units, want one per recipe revision", len(units))
	}

	const zlib = "_/zlib/1.2.13/_/" + conanRevA

	want := []string{
		zlib + "/export/conan_export.tgz", zlib + "/export/conandata.yml", zlib + "/export/conanfile.py",
		zlib + "/export/metadata/info.json", zlib + "/export/conanmanifest.txt",
		zlib + "/package/" + conanPIDa + "/" + conanPRevA + "/conan_package.tgz",
		zlib + "/package/" + conanPIDa + "/" + conanPRevA + "/conaninfo.txt",
		zlib + "/package/" + conanPIDa + "/" + conanPRevA + "/metadata/logs.txt",
		zlib + "/package/" + conanPIDa + "/" + conanPRevA + "/conanmanifest.txt",
		zlib + "/package/" + conanPIDb + "/" + conanPRevB + "/conan_package.tgz",
		zlib + "/package/" + conanPIDb + "/" + conanPRevB + "/conaninfo.txt",
		zlib + "/package/" + conanPIDb + "/" + conanPRevB + "/conanmanifest.txt",
	}

	var got []string
	for _, a := range units[0] {
		got = append(got, a.Path)
	}

	if !slices.Equal(got, want) {
		t.Fatalf("first unit:\n got %v\nwant %v", got, want)
	}

	if len(units[1]) != 2 || !strings.HasSuffix(units[1][1].Path, "/export/conanmanifest.txt") {
		t.Fatalf("second unit = %v, want the recipe files with the manifest last", units[1])
	}
}

func TestImportConanRerunStoresOnlyTheManifestsAgain(t *testing.T) {
	e := newConanEnv(t, conanFixture())
	e.run(t)

	payload := len(conanPayload(e.cases))
	puts := e.cf.puts

	rep := e.run(t)

	// The listing shows one row per recipe revision, so only the recipe
	// manifests can be recognised; the other files are stored again, in place.
	if rep.failed() || rep.Present != 2 || rep.Uploaded != payload-2 {
		t.Fatalf("second run = %+v, want 2 recipe manifests present and %d files stored again", rep, payload-2)
	}

	if got := e.cf.puts - puts; got != payload-2 {
		t.Fatalf("second run PUT %d files, want %d", got, payload-2)
	}

	e.assertLanded(t)

	if got := len(e.oa.repos["dst"].items); got != 2 {
		t.Fatalf("listing has %d rows after the second run", got)
	}
}

func TestImportConanResumesFromStateFile(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.jsonl")
	e := newConanEnv(t, conanFixture(), "--state-file", state)
	payload := len(conanPayload(e.cases))

	if rep := e.run(t); rep.Uploaded != payload {
		t.Fatalf("first run = %+v", rep)
	}

	// Only the state file knows the files are done.
	e.cf.files, e.oa.repos["dst"].items = map[string]*conanStored{}, nil
	puts := e.cf.puts

	rep := e.run(t)
	if rep.Resumed != payload || rep.Uploaded != 0 || e.cf.puts != puts {
		t.Fatalf("resumed=%d uploaded=%d puts+=%d, want resume only", rep.Resumed, rep.Uploaded, e.cf.puts-puts)
	}
}

func TestImportConanDryRunWritesNothing(t *testing.T) {
	e := newConanEnv(t, conanFixture(), "--dry-run")

	rep := e.run(t)
	if rep.failed() || !rep.DryRun || rep.Uploaded != len(conanPayload(e.cases)) {
		t.Fatalf("report = %+v", rep)
	}

	if e.oa.writes != 0 || e.cf.puts != 0 || len(e.cf.files) != 0 {
		t.Fatalf("dry run wrote: writes=%d puts=%d", e.oa.writes, e.cf.puts)
	}
}

func TestImportConanCopiesPropertiesToTheRecipeRevision(t *testing.T) {
	e := newConanEnv(t, conanFixture())

	if rep := e.run(t); rep.failed() {
		t.Fatalf("report = %+v", rep)
	}

	row := e.oa.find("dst", "fmt/10.1.0@acme/stable#"+conanRevB)
	if row == nil || row.props["build.number"] != "42" {
		t.Fatalf("recipe revision row = %+v, want the manifest's properties", row)
	}

	manifest := conanRecipe("acme", "fmt", "10.1.0", "stable", conanRevB, "conanmanifest.txt", "").want
	if e.cf.files[manifest].id != row.id {
		t.Fatalf("manifest id %q is not the listing row %q", e.cf.files[manifest].id, row.id)
	}

	// A present manifest gets its properties again.
	row.props = nil

	if rep := e.run(t); rep.failed() || row.props["build.number"] != "42" {
		t.Fatalf("second run = %+v, props = %v", rep, row.props)
	}
}

func TestImportConanChecksumMismatchLeavesTheCopyForTheNextRun(t *testing.T) {
	cases := conanFixture()
	badPath := "_/zlib/1.2.13/_/" + conanRevA + "/package/" + conanPIDb + "/" + conanPRevB + "/conan_package.tgz"

	i := slices.IndexFunc(cases, func(c conanCase) bool { return c.file.path == badPath })
	good := cases[i]
	cases[i].file.data, cases[i].file.sha256 = []byte("tampered"), sum256(good.file.data)

	e := newConanEnv(t, cases)

	rep := e.run(t)
	if rep.Failed != 1 || rep.Uploaded != len(conanPayload(cases))-1 || rep.Failures[0].Path != badPath {
		t.Fatalf("report = %+v", rep)
	}

	// OpenArtifact only deletes whole revisions, which would take the good files
	// with it: the bad copy is reported and left, nothing is deleted.
	msg := rep.Failures[0].Error
	for _, part := range []string{"checksum mismatch", "could not remove the bad copy", "cannot remove a stored copy"} {
		if !strings.Contains(msg, part) {
			t.Errorf("error %q lacks %q", msg, part)
		}
	}

	if e.cf.revisionDeletes != 0 || e.oa.deletes != 0 {
		t.Fatalf("a revision was deleted (%d, %d)", e.cf.revisionDeletes, e.oa.deletes)
	}

	// A run from the repaired source replaces the bad copy in place.
	repaired, cfg := conanSource(t, conanFixture())

	rep = runImportTest(t, cfg, repaired, e.oa)
	if rep.failed() || string(e.cf.files[good.want].data) != string(good.file.data) {
		t.Fatalf("repair run = %+v, stored %q", rep, e.cf.files[good.want].data)
	}
}

func TestImportConanServerSideChecksumMismatchIsFailure(t *testing.T) {
	recipe := conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "conanfile.py", "zlib-recipe")
	e := newConanEnv(t, []conanCase{recipe})
	e.oa.lieSHA = strings.Repeat("0", 64)

	rep := e.run(t)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestImportConanImmutableReleaseConflictIsReported(t *testing.T) {
	recipe := conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "conanfile.py", "zlib-recipe")
	e := newConanEnv(t, []conanCase{recipe})

	released := []byte("released earlier")
	e.cf.files[recipe.want] = &conanStored{id: "old", data: released, sha: sum256(released)}
	e.cf.immutable = true

	rep := e.run(t)

	var msg string
	if len(rep.Failures) == 1 {
		msg = rep.Failures[0].Error
	}

	if rep.Failed != 1 || !strings.Contains(msg, errUploadConflict.Error()) || !strings.Contains(msg, "immutable") {
		t.Fatalf("report = %+v", rep)
	}

	if string(e.cf.files[recipe.want].data) != string(released) {
		t.Fatal("the existing release was changed")
	}
}

func TestImportConanRepeatsAFailedUpload(t *testing.T) {
	recipe := conanRecipe("_", "zlib", "1.2.13", "_", conanRevA, "conanfile.py", "zlib-recipe")
	e := newConanEnv(t, []conanCase{recipe})

	flaky := newFlaky(t, e.oa.srv.Config.Handler, func(r *http.Request) bool { return r.Method == http.MethodPut }, 1, failStatus(http.StatusServiceUnavailable))
	(&fakeSleeper{}).apply(e.cfg.retrier)

	rep := runImportVia(t, e.cfg, e.src, e.oa, flaky.Server)
	if rep.failed() || rep.Uploaded != 1 || rep.Retries != 1 || flaky.hits() != 2 {
		t.Fatalf("report = %+v, upload attempts = %d", rep, flaky.hits())
	}

	e.assertLanded(t)
}

// ─── Conan 1 (no revisions): Artifactory and Nexus ────────────────────────────

func eachConan1Source(t *testing.T, fn func(t *testing.T, s conan1Source)) {
	t.Helper()

	for _, s := range conan1Sources() {
		t.Run(s.name, func(t *testing.T) { fn(t, s) })
	}
}

func TestImportConan1LandsAsRevisionZeroThroughTheV1Endpoint(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		e := newConan1Env(t, s)

		rep := e.run(t)
		payload := len(conanPayload(e.cases))

		if rep.failed() || rep.Uploaded != payload || rep.Ignored != len(e.cases)-payload || payload != s.payload {
			t.Fatalf("report = %+v", rep)
		}

		e.assertLanded(t)
	})
}

func TestImportConan1ListsOneRowPerRecipeRevisionZero(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		e := newConan1Env(t, s)
		e.run(t)

		if got := len(e.oa.repos["dst"].items); got != 2 {
			t.Fatalf("listing has %d rows, want one per recipe revision", got)
		}

		for _, want := range []struct{ filename, name, version, content string }{
			{"zlib/1.2.13@_/_#0", "zlib", "1.2.13", "zlib-manifest"},
			{"fmt/10.1.0@acme/stable#0", "fmt", "10.1.0", "fmt-manifest"},
		} {
			it := e.oa.find("dst", want.filename)
			if it == nil || it.name != want.name || it.version != want.version || it.sha != sum256([]byte(want.content)) || it.created != s.stamp {
				t.Errorf("row %s = %+v", want.filename, it)
			}
		}
	})
}

func TestImportConan1UploadsTheRecipeThenEachPackageWithManifestsLast(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		e := newConan1Env(t, s)

		if rep := e.run(t); rep.failed() {
			t.Fatalf("report = %+v (a package before its recipe manifest is refused with 404)", rep)
		}

		const zlib = "/v1/files/zlib/1.2.13/_/_/0/"

		pkgA, pkgB := zlib+"package/"+conanPIDa+"/0/", zlib+"package/"+conanPIDb+"/0/"
		recipe := []string{zlib + "export/conan_export.tgz", zlib + "export/conan_sources.tgz", zlib + "export/conanfile.py"}
		pkgFilesA := []string{pkgA + "conan_package.tgz", pkgA + "conaninfo.txt"}

		if s.metadata {
			recipe = append(recipe, zlib+"export/metadata/info.json")
			pkgFilesA = append(pkgFilesA, pkgA+"metadata/logs.txt")
		}

		want := slices.Concat(
			recipe, []string{zlib + "export/conanmanifest.txt"},
			pkgFilesA, []string{pkgA + "conanmanifest.txt"},
			[]string{pkgB + "conan_package.tgz", pkgB + "conaninfo.txt", pkgB + "conanmanifest.txt"},
		)

		if got := e.cf.storedUnder(zlib); !slices.Equal(got, want) {
			t.Fatalf("upload order:\n got %v\nwant %v", got, want)
		}
	})
}

func TestImportConan1RerunStoresOnlyTheManifestsAgain(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		e := newConan1Env(t, s)
		e.run(t)

		payload := len(conanPayload(e.cases))
		puts := e.cf.puts

		rep := e.run(t)
		if rep.failed() || rep.Present != 2 || rep.Uploaded != payload-2 || e.cf.puts-puts != payload-2 {
			t.Fatalf("second run = %+v, PUTs %d; want 2 recipe manifests present and %d files stored again", rep, e.cf.puts-puts, payload-2)
		}

		e.assertLanded(t)

		if got := len(e.oa.repos["dst"].items); got != 2 {
			t.Fatalf("listing has %d rows after the second run", got)
		}
	})
}

func TestImportConan1ResumesFromStateFile(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		e := newConan1Env(t, s, "--state-file", filepath.Join(t.TempDir(), "state.jsonl"))
		payload := len(conanPayload(e.cases))

		if rep := e.run(t); rep.Uploaded != payload {
			t.Fatalf("first run = %+v", rep)
		}

		e.cf.files, e.oa.repos["dst"].items = map[string]*conanStored{}, nil
		puts := e.cf.puts

		rep := e.run(t)
		if rep.Resumed != payload || rep.Uploaded != 0 || e.cf.puts != puts {
			t.Fatalf("resumed=%d uploaded=%d puts+=%d, want resume only", rep.Resumed, rep.Uploaded, e.cf.puts-puts)
		}
	})
}

func TestImportConan1DryRunWritesNothing(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		e := newConan1Env(t, s, "--dry-run")

		rep := e.run(t)
		if rep.failed() || !rep.DryRun || rep.Uploaded != len(conanPayload(e.cases)) {
			t.Fatalf("report = %+v", rep)
		}

		if e.oa.writes != 0 || e.cf.puts != 0 || len(e.cf.files) != 0 {
			t.Fatalf("dry run wrote: writes=%d puts=%d", e.oa.writes, e.cf.puts)
		}
	})
}

func TestImportConan1CopiesArtifactoryPropertiesToTheRecipeRevision(t *testing.T) {
	e := newConan1Env(t, conan1Sources()[0])

	if rep := e.run(t); rep.failed() {
		t.Fatalf("report = %+v", rep)
	}

	row := e.oa.find("dst", "fmt/10.1.0@acme/stable#0")
	if row == nil || row.props["build.number"] != "42" {
		t.Fatalf("recipe revision row = %+v, want the manifest's properties", row)
	}
}

func TestImportConan1ChecksumMismatchLeavesTheCopyForTheNextRun(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		cases := s.fixture()
		bad := conan1Want("_", "zlib", "1.2.13", "_", conanPIDb, "conan_package.tgz")

		i := slices.IndexFunc(cases, func(c conanCase) bool { return c.want == bad })
		good := cases[i]
		cases[i].file.data, cases[i].file.sha256 = []byte("tampered"), sum256(good.file.data)

		src, cfg := s.serve(t, cases)
		oa := newFakeOA(t, map[string]*fakeRepo{"dst": {format: "conan", repoType: "hosted"}})
		cf := conanOf(t, oa)

		rep := runImportTest(t, cfg, src, oa)
		if rep.Failed != 1 || rep.Uploaded != len(conanPayload(cases))-1 || rep.Failures[0].Path != good.file.path {
			t.Fatalf("report = %+v", rep)
		}

		if msg := rep.Failures[0].Error; !strings.Contains(msg, "checksum mismatch") || !strings.Contains(msg, "cannot remove a stored copy") {
			t.Errorf("error %q", msg)
		}

		if cf.revisionDeletes != 0 || oa.deletes != 0 {
			t.Fatalf("a revision was deleted (%d, %d)", cf.revisionDeletes, oa.deletes)
		}

		repaired, cfg := s.serve(t, s.fixture())

		rep = runImportTest(t, cfg, repaired, oa)
		if rep.failed() || string(cf.files[bad].data) != string(good.file.data) {
			t.Fatalf("repair run = %+v, stored %q", rep, cf.files[bad].data)
		}
	})
}

func TestImportConan1ImmutableReleaseConflictIsReported(t *testing.T) {
	eachConan1Source(t, func(t *testing.T, s conan1Source) {
		t.Helper()

		recipe := s.recipe("_", "zlib", "1.2.13", "_", "conanfile.py", "zlib-recipe")
		src, cfg := s.serve(t, []conanCase{recipe})
		oa := newFakeOA(t, map[string]*fakeRepo{"dst": {format: "conan", repoType: "hosted"}})
		cf := conanOf(t, oa)

		released := []byte("released earlier")
		cf.files[recipe.want] = &conanStored{id: "old", data: released, sha: sum256(released)}
		cf.immutable = true

		rep := runImportTest(t, cfg, src, oa)
		if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, errUploadConflict.Error()) || string(cf.files[recipe.want].data) != string(released) {
			t.Fatalf("report = %+v", rep)
		}
	})
}

// TestImportConanMixedLayoutsInOneRepository: an Artifactory repository that
// holds revisioned and revisionless content is imported in one run, each
// through its own endpoint.
func TestImportConanMixedLayoutsInOneRepository(t *testing.T) {
	cases := append(conanFixture(), conan1Sources()[0].fixture()...)
	e := newConanEnv(t, cases)

	rep := e.run(t)
	if rep.failed() || rep.Uploaded != len(conanPayload(cases)) {
		t.Fatalf("report = %+v", rep)
	}

	e.assertLanded(t)

	if got := len(e.oa.repos["dst"].items); got != 4 {
		t.Fatalf("listing has %d rows, want the four recipe revisions", got)
	}
}

func TestImportConanRegistration(t *testing.T) {
	def := formatDefs[formatConan]

	if !slices.Equal(def.nexus, []string{"conan"}) || !slices.Equal(def.artifactory, []string{"conan"}) || !def.overwrites || def.components {
		t.Fatalf("definition = nexus %v artifactory %v overwrites %v components %v", def.nexus, def.artifactory, def.overwrites, def.components)
	}

	err := def.remove(context.Background(), nil, "dst", sourceAsset{}, uploadResult{})
	if !errors.Is(err, errImportUsage) || !strings.Contains(err.Error(), "cannot remove") {
		t.Fatalf("remove = %v, want errNoRemove", err)
	}
}

func TestImportConanImportable(t *testing.T) {
	const (
		recipe = "_/zlib/1.2.13/_/" + conanRevA
		pkg    = recipe + "/package/" + conanPIDa + "/" + conanPRevA
	)

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"recipe file", recipe + "/export/conanfile.py", true},
		{"recipe manifest", recipe + "/export/conanmanifest.txt", true},
		{"recipe file in a subdirectory", recipe + "/export/sub/data.json", true},
		{"package file", pkg + "/conan_package.tgz", true},
		{"user and channel", "acme/fmt/10.1.0/stable/" + conanRevB + "/export/conanfile.py", true},
		{"conan 1 recipe", "_/zlib/1.2.13/_/export/conanfile.py", true},
		{"conan 1 recipe in a subdirectory", "_/zlib/1.2.13/_/export/sub/data.json", true},
		{"conan 1 package", "_/zlib/1.2.13/_/package/" + conanPIDa + "/conan_package.tgz", true},
		{"conan 1 user and channel", "acme/fmt/10.1.0/stable/export/conanmanifest.txt", true},
		{"conan 1 package without a package id", "_/zlib/1.2.13/_/package/conaninfo.txt", false},
		{"conan 1 index", "_/zlib/1.2.13/_/export/index.json", false},
		{"conan 1 package index", "_/zlib/1.2.13/_/package/" + conanPIDa + "/index.json", false},
		{"conan 1 dot file", "_/zlib/1.2.13/_/export/.hidden", false},
		{"user conans, conan 1", "conans/zlib/1.2.13/_/export/conanfile.py", true},
		{"user conans, conan 2", "conans/zlib/1.2.13/_/" + conanRevA + "/export/conanfile.py", true},
		{"nexus recipe file", "conans/_/zlib/1.2.13/_/conanfile.py", true},
		{"nexus recipe manifest", "conans/_/zlib/1.2.13/_/conanmanifest.txt", true},
		{"nexus recipe tarball", "conans/acme/fmt/10.1.0/stable/conan_export.tgz", true},
		{"nexus package file", "conans/_/zlib/1.2.13/_/packages/" + conanPIDa + "/conan_package.tgz", true},
		{"nexus package info", "conans/acme/fmt/10.1.0/stable/packages/" + conanPIDa + "/conaninfo.txt", true},
		{"nexus other asset", "conans/_/zlib/1.2.13/_/download_urls", false},
		{"nexus other package asset", "conans/_/zlib/1.2.13/_/packages/" + conanPIDa + "/digest", false},
		{"nexus package without a package id", "conans/_/zlib/1.2.13/_/packages/conaninfo.txt", false},
		{"nexus file in a subdirectory", "conans/_/zlib/1.2.13/_/sub/conanfile.py", false},
		{"nexus too short", "conans/zlib/1.2.13/_/conanfile.py", false},
		{"channel index", "_/zlib/1.2.13/_/index.json", false},
		{"revision index", recipe + "/index.json", false},
		{"package revision index", pkg + "/index.json", false},
		{"dot file", recipe + "/export/.hidden", false},
		{"dot directory", recipe + "/.tmp/conanfile.py", false},
		{"no file below export", recipe + "/export", false},
		{"package without a revision", recipe + "/package/" + conanPIDa + "/conan_package.tgz", false},
		{"neither export nor package", recipe + "/other/conanfile.py", false},
		{"empty segment", recipe + "/export//conanfile.py", false},
		{"too short", "_/zlib/1.2.13/_/conanfile.py", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := importable(formatConan, tc.path); got != tc.want {
				t.Fatalf("importable(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestImportConanPresentIsTheRecipeManifestOnly: the package listing has one row
// per recipe revision carrying the digest of its manifest.
func TestImportConanPresentIsTheRecipeManifestOnly(t *testing.T) {
	manifest := sum256([]byte("manifest"))
	byPath := map[string]indexEntry{
		"zlib/1.2.13@_/_#" + conanRevA:        {ID: "r1", SHA256: manifest},
		"fmt/10.1.0@acme/stable#" + conanRevB: {ID: "r2", SHA256: manifest},
		"zlib/1.2.13@_/_#0":                   {ID: "r3", SHA256: manifest},
		"fmt/10.1.0@acme/stable#0":            {ID: "r4", SHA256: manifest},
	}
	ix := &targetIndex{format: formatConan, byPath: byPath}

	cases := []struct {
		name string
		path string
		sha  string
		want string // listing id, "" for absent
	}{
		{"recipe manifest", "_/zlib/1.2.13/_/" + conanRevA + "/export/conanmanifest.txt", manifest, "r1"},
		{"recipe manifest with user and channel", "acme/fmt/10.1.0/stable/" + conanRevB + "/export/conanmanifest.txt", manifest, "r2"},
		{"conan 1 recipe manifest is revision 0", "_/zlib/1.2.13/_/export/conanmanifest.txt", manifest, "r3"},
		{"conan 1 recipe manifest with user and channel", "acme/fmt/10.1.0/stable/export/conanmanifest.txt", manifest, "r4"},
		{"nexus recipe manifest", "conans/_/zlib/1.2.13/_/conanmanifest.txt", manifest, "r3"},
		{"nexus recipe manifest with user and channel", "conans/acme/fmt/10.1.0/stable/conanmanifest.txt", manifest, "r4"},
		{"conan 1 changed manifest", "_/zlib/1.2.13/_/export/conanmanifest.txt", sum256([]byte("other")), ""},
		{"conan 1 package manifest with the digest", "_/zlib/1.2.13/_/package/" + conanPIDa + "/conanmanifest.txt", manifest, ""},
		{"nexus package manifest with the digest", "conans/_/zlib/1.2.13/_/packages/" + conanPIDa + "/conanmanifest.txt", manifest, ""},
		{"conan 1 recipe of another version", "_/zlib/1.2.14/_/export/conanmanifest.txt", manifest, ""},
		{"changed manifest", "_/zlib/1.2.13/_/" + conanRevA + "/export/conanmanifest.txt", sum256([]byte("other")), ""},
		{"other revision", "_/zlib/1.2.13/_/" + conanRevB + "/export/conanmanifest.txt", manifest, ""},
		{"other recipe file with the digest", "_/zlib/1.2.13/_/" + conanRevA + "/export/conanfile.py", manifest, ""},
		{"package manifest with the digest", "_/zlib/1.2.13/_/" + conanRevA + "/package/" + conanPIDa + "/" + conanPRevA + "/conanmanifest.txt", manifest, ""},
		{"no digest", "_/zlib/1.2.13/_/" + conanRevA + "/export/conanmanifest.txt", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := ix.lookup(sourceAsset{Path: tc.path, SHA256: tc.sha})
			if ok != (tc.want != "") || e.ID != tc.want {
				t.Fatalf("lookup = %+v, %v; want id %q", e, ok, tc.want)
			}
		})
	}
}

// TestImportConanUploadAddress pins the request URL of every kind of file, in
// the order the server reads it: name/version/user/channel.
func TestImportConanUploadAddress(t *testing.T) {
	var got []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		got = append(got, r.Method+" "+r.URL.EscapedPath())

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "x", "sha256": "y"})
	}))
	t.Cleanup(srv.Close)

	tgt := &importTarget{c: &Client{baseURL: srv.URL, httpClient: srv.Client()}}
	upload := formatDefs[formatConan].upload

	for _, p := range []string{
		"_/zlib/1.2.13/_/" + conanRevA + "/export/conanfile.py",
		"acme/fmt/10.1.0/stable/" + conanRevB + "/export/sub dir/data.json",
		"acme/fmt/10.1.0/stable/" + conanRevB + "/package/" + conanPIDa + "/" + conanPRevA + "/conan_package.tgz",
		"_/zlib/1.2.13/_/export/conanfile.py",
		"acme/fmt/10.1.0/stable/export/sub dir/data.json",
		"acme/fmt/10.1.0/stable/package/" + conanPIDa + "/conan_package.tgz",
		"conans/_/zlib/1.2.13/_/conanmanifest.txt",
		"conans/acme/fmt/10.1.0/stable/packages/" + conanPIDa + "/conaninfo.txt",
	} {
		if _, err := upload(context.Background(), tgt, uploadRequest{Dst: "my repo", Asset: sourceAsset{Path: p}, Body: strings.NewReader("x")}); err != nil {
			t.Fatalf("upload %s: %v", p, err)
		}
	}

	want := []string{
		"PUT /repos/my%20repo/v2/conans/zlib/1.2.13/_/_/revisions/" + conanRevA + "/files/conanfile.py",
		"PUT /repos/my%20repo/v2/conans/fmt/10.1.0/acme/stable/revisions/" + conanRevB + "/files/sub%20dir/data.json",
		"PUT /repos/my%20repo/v2/conans/fmt/10.1.0/acme/stable/revisions/" + conanRevB + "/packages/" + conanPIDa + "/revisions/" + conanPRevA + "/files/conan_package.tgz",
		"PUT /repos/my%20repo/v1/files/zlib/1.2.13/_/_/0/export/conanfile.py",
		"PUT /repos/my%20repo/v1/files/fmt/10.1.0/acme/stable/0/export/sub%20dir/data.json",
		"PUT /repos/my%20repo/v1/files/fmt/10.1.0/acme/stable/0/package/" + conanPIDa + "/0/conan_package.tgz",
		"PUT /repos/my%20repo/v1/files/zlib/1.2.13/_/_/0/export/conanmanifest.txt",
		"PUT /repos/my%20repo/v1/files/fmt/10.1.0/acme/stable/0/package/" + conanPIDa + "/0/conaninfo.txt",
	}

	if !slices.Equal(got, want) {
		t.Fatalf("requests:\n got %v\nwant %v", got, want)
	}
}
