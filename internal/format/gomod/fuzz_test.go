// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/logger"
	"go.uber.org/zap"
	"golang.org/x/mod/modfile"
	xmod "golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// fuzzEntry is one file of a hand-built module zip.
type fuzzEntry struct{ name, content string }

// fuzzZip builds a zip holding the entries in order (duplicates and hostile
// names allowed: the writer does not validate them).
func fuzzZip(entries ...fuzzEntry) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			continue
		}

		_, _ = w.Write([]byte(e.content))
	}

	_ = zw.Close()

	return buf.Bytes()
}

// fuzzModuleZip lays a module out the way `go mod` does.
func fuzzModuleZip(module, version, goMod string) []byte {
	prefix := module + "@" + version + "/"

	return fuzzZip(fuzzEntry{prefix + "go.mod", goMod}, fuzzEntry{prefix + "main.go", "package m\n"})
}

// fuzzGoModBomb is a module zip whose go.mod inflates one byte past the cap
// from a few kilobytes.
func fuzzGoModBomb() []byte {
	return fuzzZip(fuzzEntry{"example.com/m@v1.0.0/go.mod", "module example.com/m\n" + strings.Repeat(" ", maxGoModFileBytes)})
}

// fuzzZipEntries lists the entry names of a zip, or nil when it does not open.
func fuzzZipEntries(data []byte) []string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}

	return names
}

// fuzzSmallZip is the declared uncompressed size up to which every entry point
// is run on a fuzz input.
const fuzzSmallZip = 64 << 10

// fuzzDeclaredSize sums the uncompressed sizes the zip directory declares.
func fuzzDeclaredSize(data []byte) uint64 {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0
	}

	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
	}

	return total
}

func isGoModEntry(name string) bool {
	return name == goModFileName || strings.HasSuffix(name, "/"+goModFileName)
}

// FuzzParseModuleZip feeds arbitrary bytes to the module zip parser behind
// upload and the repair scan. It checks that whatever is accepted is a root go.mod
// within the decompression cap whose module line is what was reported.
func FuzzParseModuleZip(f *testing.F) {
	const goMod = "module example.com/m\n\ngo 1.21\n"

	f.Add(fuzzModuleZip("example.com/m", "v1.0.0", goMod))
	f.Add(fuzzZip(fuzzEntry{"example.com/m@v1.0.0/go.mod", goMod}, fuzzEntry{"example.com/m@v1.0.0/sub/go.mod", "module example.com/m/sub\n"}))
	f.Add(fuzzZip(fuzzEntry{"example.com/m@v1.0.0/sub/go.mod", "module example.com/m/sub\n"}, fuzzEntry{"example.com/m@v1.0.0/go.mod", goMod}))
	f.Add(fuzzZip(fuzzEntry{"example.com/m@v1.0.0/foo-go.mod", goMod}))
	f.Add(fuzzZip(fuzzEntry{"example.com/m@v1.0.0/go.mod", "go 1.21\n"}))
	f.Add(fuzzZip(fuzzEntry{"example.com/m@v1.0.0/go.mod", "module \"example.com/q\" // c\n"}))
	f.Add(fuzzZip(fuzzEntry{"../go.mod", goMod}))
	f.Add(fuzzZip(fuzzEntry{"/go.mod", goMod}))
	f.Add(fuzzZip(fuzzEntry{"go.mod", goMod}))
	f.Add(fuzzZip(fuzzEntry{"a@b@c/d/../go.mod", goMod}, fuzzEntry{"a@b@c/e", ""}))
	f.Add(fuzzGoModBomb())
	f.Add(fuzzZip())
	f.Add([]byte("PK\x05\x06\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("not a zip"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		mf, err := parseModuleZip(data)
		if err == nil {
			checkModuleFile(t, data, mf)
		}

		// Inflating a zip that declares a lot of content once is enough: the
		// other entry points would repeat the same multi-megabyte read.
		if fuzzDeclaredSize(data) > fuzzSmallZip {
			return
		}

		checkRepairAnalysis(t, data, mf)
	})
}

// checkModuleFile asserts the invariants of an accepted module zip.
func checkModuleFile(t *testing.T, data []byte, mf *moduleFile) {
	t.Helper()

	if len(mf.content) > maxGoModFileBytes {
		t.Fatalf("go.mod content is %d bytes, over the %d cap", len(mf.content), maxGoModFileBytes)
	}

	if mf.modulePath == "" || mf.modulePath != modfile.ModulePath([]byte(mf.content)) {
		t.Fatalf("module path %q does not come from the go.mod content", mf.modulePath)
	}

	if !isGoModEntry(mf.entry) {
		t.Fatalf("entry %q is not a go.mod", mf.entry)
	}

	depth := strings.Count(mf.entry, "/")
	found := false

	for _, name := range fuzzZipEntries(data) {
		if name == mf.entry {
			found = true
		}

		if isGoModEntry(name) && strings.Count(name, "/") < depth {
			t.Fatalf("entry %q is shallower than the chosen root %q", name, mf.entry)
		}
	}

	if !found {
		t.Fatalf("entry %q is not in the zip", mf.entry)
	}
}

// checkRepairAnalysis runs the repair scan on the same bytes and checks its
// prefix invariants; the scan and the upload parser must agree on the go.mod.
func checkRepairAnalysis(t *testing.T, data []byte, mf *moduleFile) {
	t.Helper()

	c, err := analyzeModuleZip(data)
	if err != nil {
		return
	}

	if c.prefix == "" || !strings.Contains(c.prefix, "@") {
		t.Fatalf("candidate prefix %q has no module@version shape", c.prefix)
	}

	for _, name := range fuzzZipEntries(data) {
		if !strings.HasPrefix(name, c.prefix+"/") {
			t.Fatalf("entry %q is outside the candidate prefix %q", name, c.prefix)
		}
	}

	if len(c.goMod) > maxGoModFileBytes {
		t.Fatalf("candidate go.mod is %d bytes, over the %d cap", len(c.goMod), maxGoModFileBytes)
	}

	// A go.mod without a module line counts for the repair scan but not for
	// the upload parser; the reverse must never happen, and both must read
	// the same file.
	if mf != nil && (!c.hasGoMod || c.goMod != mf.content) {
		t.Fatalf("upload parser read a go.mod the repair scan did not (hasGoMod=%v)", c.hasGoMod)
	}

	checkRelinkPlan(t, c)
}

// checkRelinkPlan matches rows built from a candidate back to it.
func checkRelinkPlan(t *testing.T, c zipCandidate) {
	t.Helper()

	at := strings.IndexByte(c.prefix, '@')
	module, version := c.prefix[:at], c.prefix[at+1:]

	row := func(id, mod, content string) database.GomodAffectedRow {
		return database.GomodAffectedRow{ID: id, RepoName: "r", ModulePath: mod, Version: version, GoModContent: content}
	}

	rows := []database.GomodAffectedRow{row("same", module, c.goMod), row("other", module+"x", c.goMod)}
	if c.hasGoMod {
		rows = append(rows, row("diff", module, c.goMod+"x"))
	}

	plan, issues := planRelinks(rows, map[string]zipCandidate{"k1": c})

	if ids := plan["k1"]; !slices.Equal(ids, []string{"same"}) {
		t.Fatalf("plan[k1] = %v, want [same] (plan %v)", ids, plan)
	}

	if want := len(rows) - 1; len(issues) != want {
		t.Fatalf("%d issues, want %d", len(issues), want)
	}

	plan, issues = planRelinks(rows[:1], map[string]zipCandidate{"k1": c, "k2": c})
	if len(plan) != 0 || len(issues) != 1 {
		t.Fatalf("two identical candidates must be ambiguous: plan %v issues %v", plan, issues)
	}
}

// FuzzModuleTail feeds arbitrary GOPROXY request tails and module paths and
// versions to the tail splitter, the upload predicate and the case-encoding
// helpers behind the routes (module.UnescapePath/UnescapeVersion).
func FuzzModuleTail(f *testing.F) {
	for _, tail := range []string{
		"github.com/example/mymodule/@v/v1.0.0",
		"github.com/example/mymodule/@v/v1.0.0.zip",
		"github.com/!example/!my!module/@v/v1.0.0-!r!c1.info",
		"github.com/example/mymodule/@v/list",
		"github.com/example/mymodule/@latest",
		"/@latest",
		"/@v/x",
		"x/@v/",
		"a/@v/b/@v/c",
		"a/@v/b/@latest",
		"a/@latest/@latest",
		"",
	} {
		f.Add(tail, "v1.0.0")
	}

	f.Add("github.com/!example/m", "v1.0.0-!r!c1")
	f.Add("example.com/m/v2", "v2.0.0+incompatible")
	f.Add("example.com/m", "v0.0.0-20240102150405-0123456789ab")
	f.Add("a.b/../c", "v1.2.3/../x")
	f.Add("a.b/!!c", "!v1.0.0")

	f.Fuzz(func(t *testing.T, tail, version string) {
		checkTailSplit(t, tail)
		checkEscapeRoundTrip(t, tail, version)
		checkValidVersion(t, version)
	})
}

func checkTailSplit(t *testing.T, tail string) {
	t.Helper()

	mt, ok := splitModuleTail(tail)
	if !ok {
		if IsUploadPath(tail) {
			t.Fatalf("IsUploadPath(%q) = true for an unparsable tail", tail)
		}

		return
	}

	if mt.module == "" {
		t.Fatalf("empty module in %+v for %q", mt, tail)
	}

	if mt.latest {
		if mt.rest != "" || tail != mt.module+"/@latest" || IsUploadPath(tail) {
			t.Fatalf("bad @latest split %+v of %q (IsUploadPath=%v)", mt, tail, IsUploadPath(tail))
		}

		return
	}

	if mt.rest == "" || tail != mt.module+"/@v/"+mt.rest || strings.Contains(mt.module, "/@v/") || !IsUploadPath(tail) {
		t.Fatalf("bad @v split %+v of %q (IsUploadPath=%v)", mt, tail, IsUploadPath(tail))
	}
}

// checkEscapeRoundTrip asserts that one decoded value has exactly one URL
// spelling: whatever the routes accept re-encodes to the very same string, so
// two URLs never name the same module version.
func checkEscapeRoundTrip(t *testing.T, escapedPath, escapedVersion string) {
	t.Helper()

	if p, err := xmod.UnescapePath(escapedPath); err == nil {
		if back, err := xmod.EscapePath(p); err != nil || back != escapedPath {
			t.Fatalf("UnescapePath(%q) = %q re-escapes to %q, %v", escapedPath, p, back, err)
		}
	}

	if v, err := xmod.UnescapeVersion(escapedVersion); err == nil {
		if back, err := xmod.EscapeVersion(v); err != nil || back != escapedVersion {
			t.Fatalf("UnescapeVersion(%q) = %q re-escapes to %q, %v", escapedVersion, v, back, err)
		}
	}

	// Decoded values travel through the URL and come back unchanged.
	if xmod.CheckPath(escapedPath) == nil {
		esc, err := xmod.EscapePath(escapedPath)
		if err != nil {
			t.Fatalf("EscapePath(%q): %v", escapedPath, err)
		}

		if back, err := xmod.UnescapePath(esc); err != nil || back != escapedPath {
			t.Fatalf("path %q -> %q -> %q, %v", escapedPath, esc, back, err)
		}
	}
}

// checkValidVersion asserts what the upload route relies on: an accepted
// version is canonical semver, carries no build metadata but +incompatible and
// survives the URL case encoding.
func checkValidVersion(t *testing.T, v string) {
	t.Helper()

	if !validVersion(v) {
		return
	}

	if !semver.IsValid(v) {
		t.Fatalf("validVersion(%q) accepts invalid semver", v)
	}

	if build := semver.Build(v); build != "" && build != incompatibleSuffix {
		t.Fatalf("validVersion(%q) accepts build metadata %q", v, build)
	}

	esc, err := xmod.EscapeVersion(v)
	if err != nil {
		t.Fatalf("validVersion(%q) but EscapeVersion fails: %v", v, err)
	}

	if back, err := xmod.UnescapeVersion(esc); err != nil || back != v {
		t.Fatalf("version %q -> %q -> %q, %v", v, esc, back, err)
	}
}

// FuzzVersionOrder feeds arbitrary version strings to the ordering behind
// @v/list, @latest and .info: compareVersions must be a total order,
// sortVersionsDesc a permutation sorted by it, and latestVersion the highest
// release (or, without releases, the highest prerelease).
func FuzzVersionOrder(f *testing.F) {
	f.Add("v1.9.0", "v1.10.0", "v1.10.0-rc1")
	f.Add("v1.0.0", "v1.0.0+incompatible", "v1.0.0-")
	f.Add("v0.0.0-20240102150405-0123456789ab", "v0.0.0-20240102150405-0123456789ac", "v0.0.1")
	f.Add("", "latest", "v1")
	f.Add("v1.0.0-RC1", "v1.0.0-rc1", "v1.0.0-rc.1")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		vs := []string{a, b, c}

		for _, x := range vs {
			if compareVersions(x, x) != 0 {
				t.Fatalf("compareVersions(%q, %q) != 0", x, x)
			}

			for _, y := range vs {
				if compareVersions(x, y) != -compareVersions(y, x) {
					t.Fatalf("compareVersions not antisymmetric for %q, %q", x, y)
				}

				for _, z := range vs {
					if compareVersions(x, y) <= 0 && compareVersions(y, z) <= 0 && compareVersions(x, z) > 0 {
						t.Fatalf("compareVersions not transitive for %q <= %q <= %q", x, y, z)
					}
				}
			}
		}

		checkSortAndLatest(t, vs)
	})
}

func checkSortAndLatest(t *testing.T, vs []string) {
	t.Helper()

	mods := make([]*models.GoModule, len(vs))
	for i, v := range vs {
		mods[i] = &models.GoModule{Version: v}
	}

	latest := latestVersion(mods)

	sortVersionsDesc(mods)

	got := make([]string, len(mods))
	for i, m := range mods {
		got[i] = m.Version
	}

	for i := 1; i < len(got); i++ {
		if compareVersions(got[i-1], got[i]) < 0 {
			t.Fatalf("sortVersionsDesc left %q before %q", got[i-1], got[i])
		}
	}

	sorted := slices.Clone(vs)
	slices.Sort(sorted)
	slices.Sort(got)

	if !slices.Equal(sorted, got) {
		t.Fatalf("sortVersionsDesc is not a permutation: %v -> %v", vs, got)
	}

	// The highest release wins; only without releases does a prerelease.
	var releases, prereleases []string

	for _, v := range vs {
		if semver.Prerelease(v) == "" {
			releases = append(releases, v)
		} else {
			prereleases = append(prereleases, v)
		}
	}

	pool := releases
	if len(pool) == 0 {
		pool = prereleases
	}

	if latest == nil || !slices.ContainsFunc(pool, func(v string) bool { return compareVersions(v, latest.Version) == 0 }) {
		t.Fatalf("latestVersion(%v) = %v, want the highest of %v", vs, latest, pool)
	}

	for _, v := range pool {
		if compareVersions(v, latest.Version) > 0 {
			t.Fatalf("latestVersion(%v) = %q, but %q is higher", vs, latest.Version, v)
		}
	}
}

// fuzzModDB is the part of the database the module routes use.
type fuzzModDB struct {
	*formattest.DB

	mods      []*models.GoModule
	used      int64
	artifacts int
}

func (d *fuzzModDB) CreateGoModule(_ context.Context, m *models.GoModule) error {
	d.mods = append(d.mods, m)
	return nil
}

func (d *fuzzModDB) GetGoModule(_ context.Context, _, modulePath, version string) (*models.GoModule, error) {
	for _, m := range d.mods {
		if m.ModulePath == modulePath && m.Version == version {
			return m, nil
		}
	}

	return nil, nil
}

func (d *fuzzModDB) ListGoModuleVersions(_ context.Context, _, modulePath string) ([]*models.GoModule, error) {
	var out []*models.GoModule

	for _, m := range d.mods {
		if m.ModulePath == modulePath {
			out = append(out, m)
		}
	}

	return out, nil
}

func (d *fuzzModDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	d.used += size
	d.artifacts += count

	return true, nil
}

func (d *fuzzModDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.used -= size
	d.artifacts -= count

	return nil
}

// newFuzzRouter mounts the module routes the way cmd/server does (GET catch-all
// plus the shared PUT) over an in-memory database and blob store.
func newFuzzRouter() (http.Handler, *fuzzModDB, *formattest.BlobStore) {
	db := &fuzzModDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	h := NewHTTPHandler(&logger.Logger{SugaredLogger: zap.NewNop().Sugar()}, db, bs, "http://localhost")
	h.SetPublisher(&formattest.Publisher{})

	r := chi.NewRouter()
	r.Route("/repos/{name}", func(sub chi.Router) {
		h.RegisterRepoRoutes(sub)
		sub.Put("/*", h.HandlePut)
	})

	return r, db, bs
}

// fuzzServe sends one request whose path is set verbatim, so hostile bytes
// reach the router without a client's URL normalisation.
func fuzzServe(router http.Handler, method, tail string, body []byte) *httptest.ResponseRecorder {
	req := formattest.Request(method, "/", bytes.NewReader(body))
	req.URL.Path = "/repos/myrepo/" + tail

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w
}

// fuzzUploadCoordinates decodes the module and version a PUT tail names. A
// tail that does not decode yields its raw pieces, so the zip still claims
// whatever the URL says and only the URL is at fault.
func fuzzUploadCoordinates(tail string) (string, string) {
	mt, ok := splitModuleTail(tail)
	if !ok || mt.latest {
		return "example.com/m", "v1.0.0"
	}

	module, version := mt.module, mt.rest

	if m, err := xmod.UnescapePath(mt.module); err == nil {
		module = m
	}

	if v, err := xmod.UnescapeVersion(mt.rest); err == nil {
		version = v
	}

	return module, version
}

// FuzzUploadRoundTrip PUTs a module zip whose entries follow the (fuzzed) URL
// and checks the upload gate: nothing below 500, nothing persisted on a
// rejection, and an accepted module is canonical and downloadable again from
// the URL it was uploaded to.
func FuzzUploadRoundTrip(f *testing.F) {
	f.Add("github.com/example/mymodule/@v/v1.0.0", "go 1.21\n")
	f.Add("github.com/!example/!my!module/@v/v1.0.0-!r!c1", "")
	f.Add("example.com/m/v2/@v/v2.0.0+incompatible", "require x.y/z v1.0.0\n")
	f.Add("example.com/m/@v/v0.0.0-20240102150405-0123456789ab", "\n// module other.example\n")
	f.Add("example.com/m/@v/v1.0", "")
	f.Add("example.com/m/@v/v1.0.0.zip", "")
	f.Add("example.com/M/@v/v1.0.0", "")
	f.Add("example.com/m/@latest", "")
	f.Add("../../etc/passwd/@v/v1.0.0", "")
	f.Add("a.b/c/@v/v1.0.0", "module a.b/other\n")
	f.Add("a.b/c/@v/v1.0.0", "\x00\xff module")

	f.Fuzz(func(t *testing.T, tail, goModTail string) {
		router, db, bs := newFuzzRouter()

		module, version := fuzzUploadCoordinates(tail)
		zipData := fuzzModuleZip(module, version, "module "+module+"\n"+goModTail)

		w := fuzzServe(router, http.MethodPut, tail, zipData)
		if w.Code >= http.StatusInternalServerError {
			t.Fatalf("PUT %q answered %d: %s", tail, w.Code, w.Body.String())
		}

		if w.Code != http.StatusCreated {
			if len(db.mods) != 0 || len(bs.Blobs) != 0 || db.used != 0 || db.artifacts != 0 {
				t.Fatalf("PUT %q answered %d but left mods=%d blobs=%d used=%d artifacts=%d",
					tail, w.Code, len(db.mods), len(bs.Blobs), db.used, db.artifacts)
			}

			return
		}

		checkAcceptedUpload(t, router, db, tail, zipData)
	})
}

func checkAcceptedUpload(t *testing.T, router http.Handler, db *fuzzModDB, tail string, zipData []byte) {
	t.Helper()

	if len(db.mods) != 1 {
		t.Fatalf("PUT %q stored %d modules, want 1", tail, len(db.mods))
	}

	m := db.mods[0]

	escPath, errP := xmod.EscapePath(m.ModulePath)
	escVer, errV := xmod.EscapeVersion(m.Version)

	if errP != nil || errV != nil || escPath+"/@v/"+escVer != tail {
		t.Fatalf("accepted URL %q is not the canonical encoding of %s@%s (%v, %v)", tail, m.ModulePath, m.Version, errP, errV)
	}

	if !IsUploadPath(tail) || !validVersion(m.Version) || modfile.ModulePath([]byte(m.GoModContent)) != m.ModulePath {
		t.Fatalf("accepted module %s@%s violates the upload gate (IsUploadPath=%v)", m.ModulePath, m.Version, IsUploadPath(tail))
	}

	base := escPath + "/@v/" + escVer

	for _, c := range []struct {
		path string
		want func(body []byte) bool
	}{
		{base + ".zip", func(b []byte) bool { return bytes.Equal(b, zipData) }},
		{base + ".mod", func(b []byte) bool { return string(b) == m.GoModContent }},
		{base + ".info", func(b []byte) bool { return fuzzInfoVersion(b) == m.Version }},
		{escPath + "/@v/list", func(b []byte) bool { return string(b) == m.Version }},
		{escPath + "/@latest", func(b []byte) bool { return fuzzInfoVersion(b) == m.Version }},
	} {
		w := fuzzServe(router, http.MethodGet, c.path, nil)
		if w.Code != http.StatusOK || !c.want(w.Body.Bytes()) {
			t.Fatalf("GET %q after upload = %d %q", c.path, w.Code, w.Body.String())
		}
	}

	if w := fuzzServe(router, http.MethodPut, tail, zipData); w.Code != http.StatusConflict {
		t.Fatalf("second PUT %q = %d, want 409", tail, w.Code)
	}
}

func fuzzInfoVersion(body []byte) string {
	var info versionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return ""
	}

	return info.Version
}

// FuzzServeGet sends arbitrary GOPROXY tails to a repository holding a few
// modules (case-encoded ones among them). Every answer is 200 or 404, and a
// 200 is only ever given to the one canonical spelling of a stored version.
func FuzzServeGet(f *testing.F) {
	router, db, _ := newFuzzRouter()
	uploaded := map[string][]byte{}

	for _, c := range [][2]string{
		{"github.com/example/mymodule", "v1.0.0"},
		{"github.com/example/mymodule", "v1.2.0-rc.1"},
		{"github.com/Example/MyModule", "v1.0.0-RC1"},
		{"example.com/m/v2", "v2.0.0+incompatible"},
	} {
		data := fuzzModuleZip(c[0], c[1], "module "+c[0]+"\n")

		esc, _ := xmod.EscapePath(c[0])
		escV, _ := xmod.EscapeVersion(c[1])

		if w := fuzzServe(router, http.MethodPut, esc+"/@v/"+escV, data); w.Code != http.StatusCreated {
			f.Fatalf("seed upload %s@%s: %d %s", c[0], c[1], w.Code, w.Body.String())
		}

		uploaded[c[0]+"@"+c[1]] = data

		for _, suffix := range []string{".info", ".mod", ".zip"} {
			f.Add(esc + "/@v/" + escV + suffix)
		}

		f.Add(esc + "/@v/list")
		f.Add(esc + "/@latest")
	}

	f.Add("github.com/Example/MyModule/@v/v1.0.0-RC1.info")
	f.Add("github.com/example/mymodule/@v/v1.0.0")
	f.Add("github.com/example/mymodule/@v/.info")
	f.Add("github.com/example/mymodule/@v/v1.0.0.zip.zip")
	f.Add("github.com/example/mymodule/@v/../@v/v1.0.0.info")
	f.Add("/@v/list")
	f.Add("@latest")

	f.Fuzz(func(t *testing.T, tail string) {
		w := fuzzServe(router, http.MethodGet, tail, nil)

		switch w.Code {
		case http.StatusNotFound:
			return
		case http.StatusOK:
			checkServedTail(t, db, uploaded, tail, w.Body.Bytes())
		default:
			t.Fatalf("GET %q answered %d: %s", tail, w.Code, w.Body.String())
		}
	})
}

// checkServedTail asserts that a 200 names a stored module under its one
// canonical URL and carries that module's bytes.
func checkServedTail(t *testing.T, db *fuzzModDB, uploaded map[string][]byte, tail string, body []byte) {
	t.Helper()

	mt, ok := splitModuleTail(tail)
	if !ok {
		t.Fatalf("GET %q served although it is not a module tail", tail)
	}

	module, err := xmod.UnescapePath(mt.module)
	if err != nil {
		t.Fatalf("GET %q served for the undecodable module %q", tail, mt.module)
	}

	if esc, _ := xmod.EscapePath(module); esc != mt.module {
		t.Fatalf("GET %q served although %q is not the canonical module spelling %q", tail, mt.module, esc)
	}

	if mt.latest || mt.rest == "list" {
		if !slices.ContainsFunc(db.mods, func(m *models.GoModule) bool { return m.ModulePath == module }) {
			t.Fatalf("GET %q served for the unknown module %q", tail, module)
		}

		return
	}

	for _, ext := range []string{".info", ".mod", ".zip"} {
		escVer, found := strings.CutSuffix(mt.rest, ext)
		if !found {
			continue
		}

		version, err := xmod.UnescapeVersion(escVer)
		if err != nil {
			t.Fatalf("GET %q served for the undecodable version %q", tail, escVer)
		}

		if back, _ := xmod.EscapeVersion(version); back != escVer {
			t.Fatalf("GET %q served although %q is not the canonical version spelling %q", tail, escVer, back)
		}

		stored, _ := db.GetGoModule(context.Background(), "", module, version)
		if stored == nil {
			t.Fatalf("GET %q served for %s@%s, which is not stored", tail, module, version)
		}

		if ext == ".zip" && !bytes.Equal(body, uploaded[module+"@"+version]) {
			t.Fatalf("GET %q served another zip", tail)
		}

		if ext == ".mod" && string(body) != stored.GoModContent {
			t.Fatalf("GET %q served another go.mod", tail)
		}

		return
	}

	t.Fatalf("GET %q served an unknown resource", tail)
}
