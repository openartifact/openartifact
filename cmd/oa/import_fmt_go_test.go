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
	"strings"
	"testing"

	xmod "golang.org/x/mod/module"
)

// The fake registry speaks the GOPROXY upload protocol: PUT <module>/@v/<version>
// with both parts case-encoded, a module zip whose <module>@<version>/go.mod
// names the module, 409 on a duplicate and a 201 {version,module} answer that
// carries no id or digest.
func init() { //nolint:gochecknoinits // the fake destination route registry is filled by each format tests
	fakeOARoutes[string(formatGo)] = func(f *fakeOA, mux *http.ServeMux) {
		mux.HandleFunc("PUT /{path...}", func(w http.ResponseWriter, r *http.Request) { goFakeUpload(f, w, r) })
	}
}

func goFakeUpload(f *fakeOA, w http.ResponseWriter, r *http.Request) {
	f.writes++

	reject := func(code int, msg string) { f.reply(w, code, map[string]string{"error": msg}) }

	escPath, escVersion, ok := strings.Cut(r.PathValue("path"), "/@v/")
	if !ok {
		reject(http.StatusNotFound, "not a module upload")
		return
	}

	modPath, err := xmod.UnescapePath(escPath)
	if err != nil {
		reject(http.StatusBadRequest, "module path is not case-encoded: "+err.Error())
		return
	}

	version, err := xmod.UnescapeVersion(escVersion)
	if err != nil {
		reject(http.StatusBadRequest, "version is not case-encoded: "+err.Error())
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "application/zip" {
		reject(http.StatusBadRequest, "content type "+ct)
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		reject(http.StatusBadRequest, "incomplete body")
		return
	}

	if err := goFakeCheckZip(data, modPath, version); err != "" {
		reject(http.StatusBadRequest, err)
		return
	}

	repo := f.repos[r.Header.Get("X-Fake-Repo")]
	for _, it := range repo.items {
		if it.name == modPath && it.version == version {
			reject(http.StatusConflict, modPath+"@"+version+" already exists")
			return
		}
	}

	it := &fakeItem{
		id: f.nextID(), path: version + ".zip", name: modPath, version: version,
		sha: sum256(data), size: len(data), created: r.Header.Get(headerImportCreatedAt),
	}
	it.sha = f.reported(it)
	repo.items = append(repo.items, it)

	f.reply(w, http.StatusCreated, map[string]string{"version": version, "module": modPath})
}

// goFakeCheckZip returns why a body is not a module zip for modPath@version.
func goFakeCheckZip(data []byte, modPath, version string) string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "not a zip archive"
	}

	for _, zf := range zr.File {
		if zf.Name != modPath+"@"+version+"/go.mod" {
			continue
		}

		rc, err := zf.Open()
		if err != nil {
			return "unreadable go.mod"
		}

		defer func() { _ = rc.Close() }()

		mod, _ := io.ReadAll(rc)
		if !strings.HasPrefix(string(mod), "module "+modPath+"\n") {
			return "go.mod names another module"
		}

		return ""
	}

	return "zip has no " + modPath + "@" + version + "/go.mod"
}

// goModuleZip builds a module zip the way `go mod download` lays it out.
func goModuleZip(t *testing.T, modPath, version string, withGoMod bool) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	root := modPath + "@" + version + "/"
	files := [][2]string{{"main.go", "package lib // " + modPath + " " + version + "\n"}}

	if withGoMod {
		files = append(files, [2]string{"go.mod", "module " + modPath + "\n\ngo 1.22\n"})
	}

	for _, f := range files {
		w, err := zw.Create(root + f[0])
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func goOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"mods": {format: "go", repoType: "hosted"}})
}

func goCfg(t *testing.T, kind string, src *httptest.Server, extra ...string) importConfig {
	t.Helper()

	args := []string{kind, "--source-url", src.URL, "--repo", "src:mods"}
	if kind == "artifactory" {
		args = append(args, "--source-user", "svc")
	}

	cfg := parseCfg(t, append(args, extra...)...)
	if kind == "artifactory" {
		cfg.SourceToken = "secret"
	}

	return cfg
}

func goArtifactory(t *testing.T, files ...fakeFile) *httptest.Server {
	t.Helper()

	return newFakeArtifactory(t, fakeSourceRepo{name: "src", format: "go", kind: "local", files: files})
}

// goStored reports whether the destination lists name@version.
func goStored(oa *fakeOA, name, version string) *fakeItem {
	for _, it := range oa.repos["mods"].items {
		if it.name == name && it.version == version {
			return it
		}
	}

	return nil
}

func TestImportGoArtifactory(t *testing.T) {
	const lib = "github.com/acme/lib"

	v100, v110, rc := goModuleZip(t, lib, "v1.0.0", true), goModuleZip(t, lib, "v1.1.0", true), goModuleZip(t, lib, "v1.2.0-RC1", true)
	azure := goModuleZip(t, "github.com/Azure/sdk/v2", "v2.3.0", true)
	raw := goModuleZip(t, "github.com/Raw/Mod", "v1.0.0", true)

	arti := goArtifactory(t,
		fakeFile{path: lib + "/@v/v1.0.0.zip", data: v100, created: artiStamp, props: map[string][]string{"build.number": {"9"}}},
		fakeFile{path: lib + "/@v/v1.0.0.mod", data: []byte("module " + lib + "\n"), created: artiStamp},
		fakeFile{path: lib + "/@v/v1.0.0.info", data: []byte(`{"Version":"v1.0.0"}`), created: artiStamp},
		fakeFile{path: lib + "/@v/v1.1.0.zip", data: v110, created: artiStamp},
		fakeFile{path: lib + "/@v/v1.2.0-!r!c1.zip", data: rc, created: artiStamp},
		fakeFile{path: lib + "/@v/list", data: []byte("v1.0.0\nv1.1.0\n"), created: artiStamp},
		fakeFile{path: "github.com/!azure/sdk/v2/@v/v2.3.0.zip", data: azure, created: artiStamp},
		// A source that kept the capitals instead of the GOPROXY "!x" encoding.
		fakeFile{path: "github.com/Raw/Mod/@v/v1.0.0.zip", data: raw, created: artiStamp},
	)
	oa := goOA(t)

	rep := runImportTest(t, goCfg(t, "artifactory", arti), arti, oa)
	if rep.failed() || rep.Uploaded != 5 || rep.Ignored != 3 {
		t.Fatalf("report = %+v", rep)
	}

	for _, want := range []struct {
		name, version string
		data          []byte
	}{
		{lib, "v1.0.0", v100}, {lib, "v1.1.0", v110}, {lib, "v1.2.0-RC1", rc},
		{"github.com/Azure/sdk/v2", "v2.3.0", azure}, {"github.com/Raw/Mod", "v1.0.0", raw},
	} {
		it := goStored(oa, want.name, want.version)
		if it == nil || it.sha != sum256(want.data) || it.created != artiWant || it.path != want.version+".zip" {
			t.Fatalf("%s@%s: item = %+v", want.name, want.version, it)
		}
	}

	if it := goStored(oa, lib, "v1.0.0"); it.props["build.number"] != "9" {
		t.Fatalf("props = %v", it.props)
	}

	if rep.Bytes != int64(len(v100)+len(v110)+len(rc)+len(azure)+len(raw)) {
		t.Fatalf("bytes = %d", rep.Bytes)
	}
}

func TestImportGoNexus(t *testing.T) {
	zipData := goModuleZip(t, "example.org/tool", "v0.4.2", true)
	nexus := newFakeNexus(t, fakeSourceRepo{name: "src", format: "go", kind: "hosted", files: []fakeFile{
		{path: "example.org/tool/@v/v0.4.2.zip", data: zipData, created: nexusStamp, contentType: "application/zip"},
		{path: "example.org/tool/@v/v0.4.2.mod", data: []byte("module example.org/tool\n"), created: nexusStamp},
		{path: "example.org/tool/@v/list", data: []byte("v0.4.2\n"), created: nexusStamp},
	}})
	oa := goOA(t)

	rep := runImportTest(t, goCfg(t, "nexus", nexus), nexus, oa)
	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 2 {
		t.Fatalf("report = %+v", rep)
	}

	if it := goStored(oa, "example.org/tool", "v0.4.2"); it == nil || it.sha != sum256(zipData) || it.created != nexusWant {
		t.Fatalf("item = %+v", it)
	}
}

func goTwoModuleSource(t *testing.T) *httptest.Server {
	t.Helper()

	return goArtifactory(t,
		fakeFile{path: "example.org/a/@v/v1.0.0.zip", data: goModuleZip(t, "example.org/a", "v1.0.0", true), created: artiStamp},
		fakeFile{path: "example.org/b/@v/v2.0.0.zip", data: goModuleZip(t, "example.org/b", "v2.0.0", true), created: artiStamp},
	)
}

func TestImportGoRerunIsIdempotent(t *testing.T) {
	arti, oa := goTwoModuleSource(t), goOA(t)
	cfg := goCfg(t, "artifactory", arti)

	runImportTest(t, cfg, arti, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, arti, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 2 || oa.writes != writes {
		t.Fatalf("report = %+v, writes += %d", rep, oa.writes-writes)
	}
}

func TestImportGoResumesFromStateFile(t *testing.T) {
	arti, oa := goTwoModuleSource(t), goOA(t)
	cfg := goCfg(t, "artifactory", arti, "--state-file", filepath.Join(t.TempDir(), "state.jsonl"))

	if rep := runImportTest(t, cfg, arti, oa); rep.Uploaded != 2 {
		t.Fatalf("first run = %+v", rep)
	}

	oa.repos["mods"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, arti, oa)
	if rep.Resumed != 2 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}
}

func TestImportGoDryRunWritesNothing(t *testing.T) {
	arti, oa := goTwoModuleSource(t), goOA(t)

	rep := runImportTest(t, goCfg(t, "artifactory", arti, "--dry-run"), arti, oa)
	if rep.failed() || rep.Uploaded != 2 || !rep.DryRun || oa.writes != 0 || len(oa.repos["mods"].items) != 0 {
		t.Fatalf("report = %+v writes=%d", rep, oa.writes)
	}
}

// The server has no delete for a module version: the importer must say the
// corrupt copy is still there rather than pretend it was cleaned up.
func TestImportGoChecksumMismatchIsFailure(t *testing.T) {
	arti := goArtifactory(t,
		fakeFile{path: "example.org/ok/@v/v1.0.0.zip", data: goModuleZip(t, "example.org/ok", "v1.0.0", true), created: artiStamp},
		fakeFile{
			path: "example.org/bad/@v/v1.0.0.zip", data: goModuleZip(t, "example.org/bad", "v1.0.0", true), created: artiStamp,
			sha256: sum256([]byte("original")),
		})
	oa := goOA(t)

	rep := runImportTest(t, goCfg(t, "artifactory", arti), arti, oa)
	if rep.Uploaded != 1 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep)
	}

	f := rep.Failures[0]
	if !strings.Contains(f.Error, "checksum mismatch") || !strings.Contains(f.Error, "cannot remove") || !strings.HasSuffix(f.Path, "bad/@v/v1.0.0.zip") {
		t.Fatalf("failure = %+v", f)
	}

	if oa.deletes != 0 {
		t.Fatalf("deletes = %d, the server has no delete route for modules", oa.deletes)
	}
}

func TestImportGoServerSideChecksumMismatchIsFailure(t *testing.T) {
	arti, oa := goTwoModuleSource(t), goOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, goCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 2 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestImportGoConflictIsReported(t *testing.T) {
	arti := goArtifactory(t,
		fakeFile{path: "example.org/a/@v/v1.0.0.zip", data: goModuleZip(t, "example.org/a", "v1.0.0", true), created: artiStamp})
	oa := goOA(t)
	oa.repos["mods"].items = []*fakeItem{{
		id: "old", path: "v1.0.0.zip", name: "example.org/a", version: "v1.0.0", sha: sum256([]byte("another build")),
	}}

	rep := runImportTest(t, goCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "already exists with different content") {
		t.Fatalf("report = %+v", rep)
	}

	if it := goStored(oa, "example.org/a", "v1.0.0"); it == nil || it.sha != sum256([]byte("another build")) {
		t.Fatalf("existing module version was touched: %+v", it)
	}
}

// A zip without go.mod is refused by the server (the importer cannot add one
// without changing the zip the module hash is computed from).
func TestImportGoZipWithoutGoModIsReported(t *testing.T) {
	arti := goArtifactory(t,
		fakeFile{path: "example.org/old/@v/v0.1.0.zip", data: goModuleZip(t, "example.org/old", "v0.1.0", false), created: artiStamp})
	oa := goOA(t)

	rep := runImportTest(t, goCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "no example.org/old@v0.1.0/go.mod") {
		t.Fatalf("report = %+v", rep)
	}

	if len(oa.repos["mods"].items) != 0 {
		t.Fatal("a rejected zip was stored")
	}
}

func TestImportGoImportable(t *testing.T) {
	for p, want := range map[string]bool{
		"github.com/acme/lib/@v/v1.0.0.zip":          true,
		"github.com/!azure/sdk/v2/@v/v2.3.0.zip":     true,
		"github.com/acme/lib/@v/v1.0.0.mod":          false,
		"github.com/acme/lib/@v/v1.0.0.info":         false,
		"github.com/acme/lib/@v/list":                false,
		"github.com/acme/lib/@latest":                false,
		"github.com/acme/lib/@v/v1.0.0.ziphash":      false,
		"github.com/acme/lib/@v/.zip":                false,
		"github.com/acme/lib/@v/sub/v1.0.0.zip":      false,
		"/@v/v1.0.0.zip":                             false,
		"archive.zip":                                false,
		"cache/download/sumdb/sum.golang.org/latest": false,
	} {
		if got := importable(formatGo, p); got != want {
			t.Errorf("importable(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestImportGoCoordinates(t *testing.T) {
	cases := map[string]struct{ module, version string }{
		"github.com/acme/lib/@v/v1.0.0.zip":              {"github.com/acme/lib", "v1.0.0"},
		"github.com/!azure/sdk/v2/@v/v2.3.0.zip":         {"github.com/Azure/sdk/v2", "v2.3.0"},
		"github.com/acme/lib/@v/v1.2.0-!r!c1.zip":        {"github.com/acme/lib", "v1.2.0-RC1"},
		"github.com/Raw/Mod/@v/v1.0.0-RC1.zip":           {"github.com/Raw/Mod", "v1.0.0-RC1"},
		"github.com/acme/lib/@v/v1.0.0+incompatible.zip": {"github.com/acme/lib", "v1.0.0+incompatible"},
		"example.org/x/@v/v0.0.0-20240102030405-abcdef123456.zip": {
			"example.org/x", "v0.0.0-20240102030405-abcdef123456",
		},
	}

	for p, want := range cases {
		module, version, err := goCoordinates(p)
		if err != nil || module != want.module || version != want.version {
			t.Errorf("goCoordinates(%q) = %q, %q, %v; want %q, %q", p, module, version, err, want.module, want.version)
		}
	}

	for _, p := range []string{
		"not a path/@v/v1.0.0.zip", // spaces are not allowed in module paths
		"example.org/x/@v/latest.zip",
		"example.org/x/@v/v1.2.zip", // shorthand, not a module version
		"example.org/x/@v/v1.0!.zip",
	} {
		if _, _, err := goCoordinates(p); err == nil {
			t.Errorf("goCoordinates(%q) accepted an invalid coordinate", p)
		}
	}
}
