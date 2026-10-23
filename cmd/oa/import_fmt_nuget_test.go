// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

const nugetBOM = "\xef\xbb\xbf"

// nugetNuspec renders the root .nuspec of a package.
func nugetNuspec(id, version string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/01/nuspec.xsd">
  <metadata>
    <id>` + id + `</id>
    <version>` + version + `</version>
    <authors>acme</authors>
  </metadata>
</package>`
}

// nugetZip builds a .nupkg from name/content pairs, in order.
func nugetZip(t *testing.T, entries ...[2]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// nugetPackage builds a package whose first entry is a nested .nuspec of
// another id: only the root-level one names the package.
func nugetPackage(t *testing.T, id, version string) []byte {
	t.Helper()

	return nugetZip(t,
		[2]string{"tools/decoy.nuspec", nugetNuspec("decoy", "9.9.9")},
		[2]string{"[Content_Types].xml", "<Types/>"},
		[2]string{id + ".nuspec", nugetNuspec(id, version)},
		[2]string{"lib/net8.0/" + id + ".dll", "MZ" + id},
	)
}

var (
	nugetFakeID      = regexp.MustCompile(`<id>([^<]*)</id>`)
	nugetFakeVersion = regexp.MustCompile(`<version>([^<]*)</version>`)
)

// nugetFakeIdentity reads id and version from the root .nuspec the way the
// server does, independently of the importer's own reader.
func nugetFakeIdentity(data []byte) (string, string, bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", false
	}

	for _, f := range zr.File {
		if strings.Contains(f.Name, "/") || !strings.HasSuffix(f.Name, ".nuspec") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return "", "", false
		}

		raw, _ := io.ReadAll(rc)
		_ = rc.Close()

		id, version := nugetFakeID.FindSubmatch(raw), nugetFakeVersion.FindSubmatch(raw)
		if id == nil || version == nil {
			return "", "", false
		}

		normalized, ok := pkgnuget.Normalize(string(version[1]))

		return strings.ToLower(string(id[1])), normalized, ok
	}

	return "", "", false
}

func init() { //nolint:gochecknoinits // the fake destination's route registry is filled by each format's tests
	fakeOARoutes["nuget"] = nugetFakeRoutes
}

// nugetFakeRoutes implements the hosted NuGet endpoints the importer uses:
// PUT /v3/package with a multipart `package` field (201 without a body, 409
// on a stored id and normalized version) and DELETE /v3/package/<id>/<version>.
func nugetFakeRoutes(f *fakeOA, mux *http.ServeMux) {
	mux.HandleFunc("PUT /v3/package", func(w http.ResponseWriter, r *http.Request) {
		f.writes++

		repo := r.Header.Get("X-Fake-Repo")

		file, _, err := r.FormFile("package")
		if err != nil {
			http.Error(w, "package form field required", http.StatusBadRequest)
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "failed to read package", http.StatusBadRequest)
			return
		}

		id, version, ok := nugetFakeIdentity(data)
		if !ok {
			http.Error(w, "invalid package", http.StatusBadRequest)
			return
		}

		for _, it := range f.repos[repo].items {
			if it.name == id && it.version == version {
				http.Error(w, "package version already exists", http.StatusConflict)
				return
			}
		}

		it := f.store(repo, id+"."+version+".nupkg", data, r.Header.Get(headerImportCreatedAt))
		it.name, it.version = id, version
		it.sha = f.reported(it)

		w.WriteHeader(http.StatusCreated)
	})

	mux.HandleFunc("DELETE /v3/package/{id}/{version}", func(w http.ResponseWriter, r *http.Request) {
		f.writes++
		f.deletes++

		version, _ := pkgnuget.Normalize(r.PathValue("version"))
		repo := f.repos[r.Header.Get("X-Fake-Repo")]

		for i, it := range repo.items {
			if it.name == strings.ToLower(r.PathValue("id")) && it.version == version {
				repo.items = append(repo.items[:i], repo.items[i+1:]...)
				w.WriteHeader(http.StatusOK)

				return
			}
		}

		http.NotFound(w, r)
	})
}

func nugetOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"nuget": {format: "nuget", repoType: "hosted"}})
}

// nugetNexusFiles lists a Nexus nuget repository: assets are <id>/<version>
// without an extension, and the version is spelled as the publisher wrote it.
func nugetNexusFiles(t *testing.T) []fakeFile {
	t.Helper()

	// The BOM is what Visual Studio and `nuget pack` put in front of a nuspec.
	bom := nugetZip(t, [2]string{"Legacy.nuspec", nugetBOM + nugetNuspec("Legacy", "1.0.0.0")})

	return []fakeFile{
		{
			path: "Acme.Core/1.2.3", data: nugetPackage(t, "Acme.Core", "1.2.3"), created: nexusStamp, contentType: "application/zip",
			compName: "Acme.Core", compVersion: "1.2.3",
		},
		{
			path: "Acme.Utils/2.0.0-Beta", data: nugetPackage(t, "Acme.Utils", "2.0-Beta+abc"), created: nexusStamp, contentType: "application/zip",
			compName: "Acme.Utils", compVersion: "2.0.0-Beta",
		},
		{
			path: "Legacy/1.0.0.0", data: bom, created: nexusStamp, contentType: "application/zip",
			compName: "Legacy", compVersion: "1.0.0.0",
		},
		{path: "v3/index.json", data: []byte("{}"), created: nexusStamp},
	}
}

func nugetNexus(t *testing.T, files []fakeFile) (*httptest.Server, importConfig) {
	t.Helper()

	srv := newFakeNexus(t, fakeSourceRepo{name: "nuget-hosted", format: "nuget", kind: "hosted", files: files})

	return srv, parseCfg(t, "nexus", "--source-url", srv.URL, "--repo", "nuget-hosted:nuget")
}

func TestImportNuGetFromNexus(t *testing.T) {
	files := nugetNexusFiles(t)
	nexus, cfg := nugetNexus(t, files)
	oa := nugetOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 3 || rep.Ignored != 1 {
		t.Fatalf("report = %+v", rep)
	}

	// Ids are lower-cased and versions normalized, as the server lists them.
	for i, want := range []struct{ file, name, version string }{
		{"acme.core.1.2.3.nupkg", "acme.core", "1.2.3"},
		{"acme.utils.2.0.0-beta.nupkg", "acme.utils", "2.0.0-beta"},
		{"legacy.1.0.0.nupkg", "legacy", "1.0.0"},
	} {
		it := oa.find("nuget", want.file)
		if it == nil || it.name != want.name || it.version != want.version {
			t.Fatalf("package %s = %+v", want.file, it)
		}

		if it.created != nexusWant || it.sha != sum256(files[i].data) {
			t.Fatalf("%s created=%q sha=%s, want %q and the source digest", want.file, it.created, it.sha, nexusWant)
		}
	}
}

func TestImportNuGetFromArtifactory(t *testing.T) {
	pkg := nugetPackage(t, "Acme.Core", "1.2.3")
	arti := newFakeArtifactory(t, fakeSourceRepo{name: "nuget-local", format: "nuget", kind: "local", files: []fakeFile{
		{path: "Acme.Core.1.2.3.nupkg", data: pkg, created: artiStamp, props: map[string][]string{
			"nuget.id": {"Acme.Core"}, "nuget.version": {"1.2.3"}, "build.name": {"core-ci"}, "team": {"secret-team"},
		}},
		{path: "Acme.Core.1.2.3.snupkg", data: []byte("symbols"), created: artiStamp},
		{path: "Acme.Core.1.2.3.symbols.nupkg", data: []byte("old symbols"), created: artiStamp},
		{path: ".nuget/Acme.Core.1.2.3.nupkg", data: []byte("index"), created: artiStamp},
	}})
	oa := nugetOA(t)

	cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc", "--repo", "nuget-local:nuget")
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, arti, oa)

	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 3 {
		t.Fatalf("report = %+v (symbol packages and the .nuget index must be ignored)", rep)
	}

	it := oa.find("nuget", "acme.core.1.2.3.nupkg")
	if it == nil || it.created != artiWant || it.sha != sum256(pkg) {
		t.Fatalf("item = %+v", it)
	}

	if len(it.props) != 1 || it.props["build.name"] != "core-ci" {
		t.Fatalf("props = %v, want only build.name", it.props)
	}
}

func TestImportNuGetNamesThePackageByItsRootNuspec(t *testing.T) {
	// Neither the file name nor the nested decoy .nuspec says what this is.
	pkg := nugetPackage(t, "Real.Id", "4.5.6")
	arti := newFakeArtifactory(t, fakeSourceRepo{name: "nuget-local", format: "nuget", kind: "local", files: []fakeFile{
		{path: "drop/whatever.nupkg", data: pkg, created: artiStamp},
	}})
	oa := nugetOA(t)

	cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc", "--repo", "nuget-local:nuget")
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, arti, oa)

	it := oa.find("nuget", "real.id.4.5.6.nupkg")
	if rep.failed() || it == nil || it.sha != sum256(pkg) {
		t.Fatalf("report = %+v item = %+v", rep, it)
	}
}

func TestImportNuGetRejectsAnUnreadablePackageWithoutUploading(t *testing.T) {
	nexus, cfg := nugetNexus(t, []fakeFile{{path: "Broken/1.0.0", data: []byte("not a zip"), created: nexusStamp}})
	oa := nugetOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "not a valid NuGet package") || oa.writes != 0 {
		t.Fatalf("report = %+v writes=%d", rep, oa.writes)
	}
}

func TestImportNuGetChecksumMismatchRemovesTheCopy(t *testing.T) {
	files := []fakeFile{
		{path: "Good/1.0.0", data: nugetPackage(t, "Good", "1.0.0"), created: nexusStamp},
		{path: "Bad/1.0.0", data: nugetPackage(t, "Bad", "1.0.0"), created: nexusStamp, sha256: sum256([]byte("original"))},
	}
	nexus, cfg := nugetNexus(t, files)
	oa := nugetOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Uploaded != 1 || rep.Failed != 1 {
		t.Fatalf("uploaded=%d failed=%d: %+v", rep.Uploaded, rep.Failed, rep.Failures)
	}

	if f := rep.Failures[0]; !strings.Contains(f.Error, "checksum mismatch") || !strings.Contains(f.Error, "bad copy removed") {
		t.Fatalf("failure = %+v", f)
	}

	if oa.find("nuget", "bad.1.0.0.nupkg") != nil || oa.deletes != 1 || oa.find("nuget", "good.1.0.0.nupkg") == nil {
		t.Fatalf("bad copy not removed alone (deletes=%d, items=%d)", oa.deletes, len(oa.repos["nuget"].items))
	}
}

func TestImportNuGetServerDigestMismatchRemovesTheCopy(t *testing.T) {
	nexus, cfg := nugetNexus(t, []fakeFile{{path: "Acme.Core/1.2.3", data: nugetPackage(t, "Acme.Core", "1.2.3"), created: nexusStamp}})
	oa := nugetOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}

	if len(oa.repos["nuget"].items) != 0 || oa.deletes != 1 {
		t.Fatalf("corrupt copy left behind (items=%d deletes=%d)", len(oa.repos["nuget"].items), oa.deletes)
	}
}

func TestImportNuGetIsIdempotent(t *testing.T) {
	nexus, cfg := nugetNexus(t, nugetNexusFiles(t))
	oa := nugetOA(t)

	runImportTest(t, cfg, nexus, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 3 || oa.writes != writes {
		t.Fatalf("second run uploaded=%d present=%d writes+=%d: %+v", rep.Uploaded, rep.Present, oa.writes-writes, rep.Failures)
	}
}

// Nexus lists NuGet assets with a SHA-1 and a SHA-512 only: a re-run must
// still see what it already imported, by id and version.
func TestImportNuGetIsIdempotentWithoutSourceSHA256(t *testing.T) {
	nexus, cfg := nugetNexus(t, nugetNexusFiles(t))
	oa := nugetOA(t)

	if rep := runImportWithoutSHA256(t, cfg, nexus, oa); rep.failed() || rep.Uploaded != 3 {
		t.Fatalf("first run: %+v", rep)
	}

	writes := oa.writes
	rep := runImportWithoutSHA256(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 3 || oa.writes != writes {
		t.Fatalf("second run uploaded=%d present=%d writes+=%d: %+v", rep.Uploaded, rep.Present, oa.writes-writes, rep.Failures)
	}
}

func TestNuGetPresent(t *testing.T) {
	stored := indexEntry{ID: "p1", SHA256: "aa"}
	ix := &targetIndex{
		format: formatNuGet,
		byPath: map[string]indexEntry{"acme.core.1.2.3.nupkg": stored, "acme.utils.2.0.0-beta.nupkg": stored},
		bySHA:  map[string]indexEntry{"aa": stored},
	}

	for _, tc := range []struct {
		name  string
		asset sourceAsset
		want  bool
	}{
		{"digest", sourceAsset{SHA256: "aa", Path: "x/y.nupkg"}, true},
		{"other digest of a listed id and version", sourceAsset{SHA256: "bb", Path: "Acme.Core/1.2.3"}, false},
		{"nexus component", sourceAsset{Path: "Acme.Core/1.2.3", Component: sourceComponent{Name: "Acme.Core", Version: "1.2.3"}}, true},
		{"component version spelled differently", sourceAsset{Component: sourceComponent{Name: "ACME.core", Version: "1.2.3.0"}}, true},
		{"nexus path", sourceAsset{Path: "Acme.Core/1.2.3"}, true},
		{"prerelease path", sourceAsset{Path: "Acme.Utils/2.0.0-Beta"}, true},
		{"artifactory properties", sourceAsset{Path: "drop/x.nupkg", Props: map[string][]string{"nuget.id": {"Acme.Core"}, "nuget.version": {"1.2.3"}}}, true},
		{"artifactory file name", sourceAsset{Path: "feed/Acme.Core.1.2.3.nupkg"}, true},
		{"unknown version", sourceAsset{Path: "Acme.Core/9.9.9"}, false},
		{"unknown id", sourceAsset{Path: "Other/1.2.3"}, false},
		{"no identity", sourceAsset{Path: "whatever"}, false},
		{"file name without a version", sourceAsset{Path: "Acme.Core.nupkg"}, false},
	} {
		if _, got := nugetPresent(ix, tc.asset); got != tc.want {
			t.Errorf("%s: present = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNuGetSourceIdentity(t *testing.T) {
	for _, tc := range []struct {
		path, id, version string
	}{
		{"Acme.Core.1.2.3.nupkg", "Acme.Core", "1.2.3"},
		{"feed/sub/Newtonsoft.Json.13.0.1.nupkg", "Newtonsoft.Json", "13.0.1"},
		{"Lib.7.Net.1.0.0-rc.1.NUPKG", "Lib.7.Net", "1.0.0-rc.1"},
		{"Acme.Core/1.2.3", "Acme.Core", "1.2.3"},
		{"Acme.Core.nupkg", "", ""},
	} {
		id, version := nugetSourceIdentity(sourceAsset{Path: tc.path})
		if id != tc.id || version != tc.version {
			t.Errorf("nugetSourceIdentity(%q) = %q, %q; want %q, %q", tc.path, id, version, tc.id, tc.version)
		}
	}
}

func TestImportNuGetResumesFromStateFile(t *testing.T) {
	nexus, cfg := nugetNexus(t, nugetNexusFiles(t))
	cfg.StateFile = filepath.Join(t.TempDir(), "state.jsonl")
	oa := nugetOA(t)

	if rep := runImportTest(t, cfg, nexus, oa); rep.Uploaded != 3 {
		t.Fatalf("first run: %+v", rep)
	}

	oa.repos["nuget"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Resumed != 3 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}
}

func TestImportNuGetDryRunWritesNothing(t *testing.T) {
	files := nugetNexusFiles(t)
	nexus, cfg := nugetNexus(t, files)
	cfg.DryRun = true
	oa := nugetOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 3 || !rep.DryRun || oa.writes != 0 || len(oa.repos["nuget"].items) != 0 {
		t.Fatalf("report = %+v writes=%d", rep, oa.writes)
	}

	if want := int64(len(files[0].data) + len(files[1].data) + len(files[2].data)); rep.Bytes != want {
		t.Fatalf("planned bytes = %d, want %d", rep.Bytes, want)
	}
}

func TestImportNuGetReportsConflictAndKeepsTheStoredPackage(t *testing.T) {
	nexus, cfg := nugetNexus(t, []fakeFile{{path: "Acme.Core/1.2.3", data: nugetPackage(t, "Acme.Core", "1.2.3"), created: nexusStamp}})
	oa := nugetOA(t)

	stored := oa.store("nuget", "acme.core.1.2.3.nupkg", []byte("an earlier, different package"), "")
	stored.name, stored.version = "acme.core", "1.2.3"
	want := stored.sha

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "already exists with different content") {
		t.Fatalf("report = %+v", rep)
	}

	if oa.deletes != 0 || len(oa.repos["nuget"].items) != 1 || oa.repos["nuget"].items[0].sha != want {
		t.Fatalf("existing package touched (deletes=%d)", oa.deletes)
	}
}

func TestNuGetIdentityRejects(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"not a zip", []byte("plain text"), "not a valid NuGet package"},
		{"no nuspec", nugetZip(t, [2]string{"lib/a.dll", "x"}), "no root .nuspec"},
		{"nested nuspec only", nugetZip(t, [2]string{"tools/a.nuspec", nugetNuspec("a", "1.0.0")}), "no root .nuspec"},
		{"two root nuspecs", nugetZip(t, [2]string{"a.nuspec", nugetNuspec("a", "1.0.0")}, [2]string{"b.nuspec", nugetNuspec("b", "1.0.0")}), "several root .nuspec"},
		{"bad version", nugetZip(t, [2]string{"a.nuspec", nugetNuspec("a", "one")}), `version "one"`},
		{"empty id", nugetZip(t, [2]string{"a.nuspec", nugetNuspec("", "1.0.0")}), `id ""`},
		{"broken xml", nugetZip(t, [2]string{"a.nuspec", "<package><metadata>"}), "reading .nuspec"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := nugetIdentity(bytes.NewReader(tc.data), int64(len(tc.data)))

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestNuGetIdentityNormalizesTheVersion(t *testing.T) {
	data := nugetZip(t, [2]string{"Pkg.nuspec", nugetBOM + nugetNuspec(" Pkg ", "01.2.0.0+build5")})

	id, version, err := nugetIdentity(bytes.NewReader(data), int64(len(data)))
	if err != nil || id != "Pkg" || version != "1.2.0" {
		t.Fatalf("identity = %q %q, %v", id, version, err)
	}
}

func TestImportableNuGet(t *testing.T) {
	for p, want := range map[string]bool{
		"Acme.Core.1.2.3.nupkg":               true,
		"acme.core.1.2.3.NUPKG":               true,
		"drop/Acme.Core.1.2.3.nupkg":          true,
		"Acme.Core/1.2.3":                     true,
		"OaSmoke.Pkg/1.2.0":                   true, // as listed by a real Nexus 3.76
		"Acme.Utils/2.0.0-Beta":               true,
		"Legacy/1.0.0.0":                      true,
		"Acme.Core.1.2.3.snupkg":              false,
		"Acme.Core.1.2.3.symbols.nupkg":       false,
		"Acme.Core.1.2.3.nuspec":              false,
		"v3/index.json":                       false,
		"Acme.Core":                           false,
		"Acme.Core/1.2.3/Acme.Core.nuspec":    false,
		"Acme.Core/latest":                    false,
		".nuget/Acme.Core.1.2.3.nupkg":        false,
		"Acme.Core.1.2.3.nupkg.sha512":        false,
		"Acme.Core/1.2.3/Acme.Core.1.2.3.zip": false,
	} {
		if got := importable(formatNuGet, p); got != want {
			t.Errorf("importable(nuget, %q) = %v, want %v", p, got, want)
		}
	}
}
