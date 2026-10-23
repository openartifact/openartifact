// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// ─── fake PyPI upload endpoint ────────────────────────────────────────────────

// pypiFake is the PyPI side of the fake OpenArtifact: twine's POST /legacy/
// with the checks of the real server, and a record of the form fields it saw.
type pypiFake struct {
	f         *fakeOA
	forms     map[string]url.Values // "repo/file name" -> form fields
	deleted   []string
	conflicts int
}

// pypiFakes finds the pypi fake of a fakeOA: its route set creates it.
var pypiFakes sync.Map //nolint:gochecknoglobals // per-server state of the fake, looked up by the test that built the server

func init() { //nolint:gochecknoinits // the fake destination's route registry is filled by each format's tests
	fakeOARoutes["pypi"] = func(f *fakeOA, mux *http.ServeMux) {
		p := &pypiFake{f: f, forms: map[string]url.Values{}}
		pypiFakes.Store(f, p)

		mux.HandleFunc("POST /legacy/", p.upload)
		mux.HandleFunc("DELETE /files/{file}", p.remove)
	}
}

func pypiState(t *testing.T, oa *fakeOA) *pypiFake {
	t.Helper()

	p, ok := pypiFakes.Load(oa)
	if !ok {
		t.Fatal("no pypi routes on this fake")
	}

	return p.(*pypiFake)
}

var pypiFakeSeparators = regexp.MustCompile(`[-_.]+`)

func pypiFakeNorm(s string) string {
	return strings.ToLower(pypiFakeSeparators.ReplaceAllString(s, "-"))
}

// pypiFakeBelongs is the server's rule for which file names an upload of a
// project and version may use, written out independently of the importer.
func pypiFakeBelongs(filename, name, version string) bool {
	if stem, ok := strings.CutSuffix(filename, ".whl"); ok {
		parts := strings.Split(stem, "-")

		return len(parts) >= 5 && parts[1] == strings.ReplaceAll(version, "-", "_") && pypiFakeNorm(parts[0]) == pypiFakeNorm(name)
	}

	for _, ext := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".zip", ".egg", ".tar"} {
		if stem, ok := strings.CutSuffix(filename, ext); ok {
			dist, ok := strings.CutSuffix(stem, "-"+version)

			return ok && dist != "" && pypiFakeNorm(dist) == pypiFakeNorm(name)
		}
	}

	return false
}

func (p *pypiFake) bad(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

func (p *pypiFake) upload(w http.ResponseWriter, r *http.Request) {
	p.f.writes++

	repo := r.Header.Get("X-Fake-Repo")

	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "multipart/form-data" {
		p.bad(w, http.StatusBadRequest, "an upload is multipart/form-data")
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		p.bad(w, http.StatusBadRequest, "bad multipart body: "+err.Error())
		return
	}

	form := url.Values(r.MultipartForm.Value)
	name, version := form.Get("name"), form.Get("version")

	if form.Get(":action") != "file_upload" || form.Get("protocol_version") != "1" || name == "" || version == "" {
		p.bad(w, http.StatusBadRequest, "missing :action, protocol_version, name or version")
		return
	}

	file, hdr, err := r.FormFile("content")
	if err != nil {
		p.bad(w, http.StatusBadRequest, "the file travels in the content field")
		return
	}

	data, err := io.ReadAll(file)
	if err != nil || form.Get("sha256_digest") != sum256(data) {
		p.bad(w, http.StatusBadRequest, "sha256_digest does not match the file")
		return
	}

	if !pypiFakeBelongs(hdr.Filename, name, version) {
		p.bad(w, http.StatusBadRequest, "filename does not match package name and version")
		return
	}

	if p.f.find(repo, hdr.Filename) != nil {
		p.conflicts++
		p.bad(w, http.StatusConflict, "file already exists")

		return
	}

	it := &fakeItem{
		id: p.f.nextID(), path: hdr.Filename, name: pypiFakeNorm(name), version: version,
		sha: sum256(data), size: len(data), created: r.Header.Get(headerImportCreatedAt),
	}
	it.sha = p.f.reported(it)
	p.f.repos[repo].items = append(p.f.repos[repo].items, it)
	p.forms[repo+"/"+hdr.Filename] = form

	w.WriteHeader(http.StatusOK)
}

func (p *pypiFake) remove(w http.ResponseWriter, r *http.Request) {
	p.f.writes++
	p.f.deletes++

	repo := r.Header.Get("X-Fake-Repo")
	items := p.f.repos[repo].items

	for i, it := range items {
		if it.path == r.PathValue("file") {
			p.f.repos[repo].items = slices.Delete(items, i, i+1)
			p.deleted = append(p.deleted, it.path)
			w.WriteHeader(http.StatusOK)

			return
		}
	}

	p.bad(w, http.StatusNotFound, "file not found")
}

// ─── fixtures ─────────────────────────────────────────────────────────────────

// pypiMeta is a core metadata file: the header block, then a description that
// holds text which looks like headers and must not be read as such.
func pypiMeta(name, version string, more ...string) string {
	lines := append([]string{"Metadata-Version: 2.1", "Name: " + name, "Version: " + version}, more...)

	return strings.Join(lines, "\n") + "\n\nName: description-not-a-header\nVersion: 99\n"
}

func pypiZip(t *testing.T, entries ...[2]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}

		if _, err := io.WriteString(w, e[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func pypiTar(t *testing.T, entries ...[2]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}

		if _, err := io.WriteString(tw, e[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func pypiTarGz(t *testing.T, entries ...[2]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)

	if _, err := zw.Write(pypiTar(t, entries...)); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func pypiWheel(t *testing.T, dist, version, metadata string) []byte {
	t.Helper()

	info := dist + "-" + version + ".dist-info/"

	return pypiZip(t,
		[2]string{dist + "/__init__.py", "VERSION = " + `"` + version + `"`},
		[2]string{info + "METADATA", metadata},
		[2]string{info + "WHEEL", "Wheel-Version: 1.0\nRoot-Is-Purelib: true\nTag: py3-none-any\n"},
	)
}

func pypiSdist(t *testing.T, top, metadata string) []byte {
	t.Helper()

	return pypiTarGz(t, [2]string{top + "/PKG-INFO", metadata}, [2]string{top + "/setup.py", "from setuptools import setup\nsetup()\n"})
}

type pypiWant struct {
	filename, name, version string
	data                    []byte
	form                    map[string]string // form fields the upload must have carried
}

const (
	pypiWheelClassifiers = "Programming Language :: Python :: 3\nLicense :: OSI Approved :: MIT License"
	pypiLongLicense      = "Permission is hereby granted, free of charge, to any person obtaining a copy\n of this software and associated documentation files"
)

// pypiNexusFixtures is the content of a Nexus pypi repository in its real
// layout (packages/<project>/<version>/<file>), with a wheel and an sdist of
// one release, a dotted project name, a project Nexus has no component for and
// the index and signature files that must stay behind.
func pypiNexusFixtures(t *testing.T) ([]fakeFile, []pypiWant) {
	t.Helper()

	wheel := pypiWheel(t, "demo_pkg", "1.0.0", pypiMeta("demo-pkg", "1.0.0",
		"Summary: A demo", "Requires-Python: >=3.8", "Author: Ada", "Author-email: ada@example.org", "License: MIT",
		"Keywords: demo,fixture", "Classifier: Programming Language :: Python :: 3", "Classifier: License :: OSI Approved :: MIT License"))
	sdist := pypiSdist(t, "demo-pkg-1.0.0", pypiMeta("demo-pkg", "1.0.0", "Summary: A demo", "Requires-Python: >=3.8"))
	zope := pypiSdist(t, "zope.thing-2.1", pypiMeta("zope.thing", "2.1"))
	bare := pypiZip(t, [2]string{"NoComp-0.3/PKG-INFO", pypiMeta("NoComp", "0.3", "License: "+pypiLongLicense)})

	files := []fakeFile{
		{path: "packages/demo-pkg/1.0.0/demo_pkg-1.0.0-py3-none-any.whl", data: wheel, created: nexusStamp, contentType: "application/zip", compName: "demo-pkg", compVersion: "1.0.0"},
		{path: "packages/demo-pkg/1.0.0/demo-pkg-1.0.0.tar.gz", data: sdist, created: nexusStamp, contentType: "application/x-gzip", compName: "demo-pkg", compVersion: "1.0.0"},
		{path: "packages/zope.thing/2.1/zope.thing-2.1.tar.gz", data: zope, created: nexusStamp, contentType: "application/x-gzip", compName: "Zope.Thing", compVersion: "2.1"},
		{path: "packages/nocomp/0.3/nocomp-0.3.zip", data: bare, created: nexusStamp, contentType: "application/zip"},
		{path: "simple/demo-pkg/", data: []byte("<html></html>"), created: nexusStamp, contentType: "text/html"},
		{path: "packages/demo-pkg/1.0.0/demo-pkg-1.0.0.tar.gz.asc", data: []byte("-----BEGIN PGP SIGNATURE-----"), created: nexusStamp},
		{path: "packages/demo-pkg/1.0.0/demo-pkg-1.0.0.tar.gz.sha256", data: []byte("abc"), created: nexusStamp},
		{path: "packages/demo-pkg/1.0.0/demo_pkg-1.0.0-py3-none-any.whl.metadata", data: []byte("Name: demo-pkg"), created: nexusStamp},
	}

	wants := []pypiWant{
		{"demo_pkg-1.0.0-py3-none-any.whl", "demo-pkg", "1.0.0", wheel, map[string]string{
			"name": "demo-pkg", "filetype": "bdist_wheel", "pyversion": "py3", "requires_python": ">=3.8", "summary": "A demo",
			"author": "Ada", "author_email": "ada@example.org", "license": "MIT", "keywords": "demo,fixture",
			"classifiers": pypiWheelClassifiers,
		}},
		{"demo-pkg-1.0.0.tar.gz", "demo-pkg", "1.0.0", sdist, map[string]string{
			"name": "demo-pkg", "filetype": "sdist", "pyversion": "source", "requires_python": ">=3.8", "summary": "A demo",
		}},
		// Nexus' component names the project, in its own spelling, before the archive does.
		{"zope.thing-2.1.tar.gz", "zope-thing", "2.1", zope, map[string]string{"name": "Zope.Thing", "filetype": "sdist"}},
		// No component: the name comes from the archive's PKG-INFO; a whole license
		// text is not an identifier and stays out of the form.
		{"nocomp-0.3.zip", "nocomp", "0.3", bare, map[string]string{"name": "NoComp", "filetype": "sdist", "license": ""}},
	}

	return files, wants
}

func pypiNewOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"py": {format: "pypi", repoType: "hosted"}})
}

func pypiNexusServer(t *testing.T, files []fakeFile) *httptest.Server {
	t.Helper()

	return newFakeNexus(t, fakeSourceRepo{name: "pysrc", format: "pypi", kind: "hosted", files: files})
}

func pypiNexusCfg(t *testing.T, srv *httptest.Server, extra ...string) importConfig {
	t.Helper()

	return parseCfg(t, append([]string{"nexus", "--source-url", srv.URL, "--repo", "pysrc:py"}, extra...)...)
}

// pypiCheckUploaded asserts the destination holds exactly the wanted files,
// with the source's bytes and creation time, and that the upload forms carried
// the expected fields.
func pypiCheckUploaded(t *testing.T, oa *fakeOA, wants []pypiWant, created string) {
	t.Helper()

	if got := len(oa.repos["py"].items); got != len(wants) {
		t.Fatalf("destination holds %d files, want %d", got, len(wants))
	}

	p := pypiState(t, oa)

	for _, w := range wants {
		it := oa.find("py", w.filename)
		if it == nil {
			t.Fatalf("%s was not uploaded", w.filename)
		}

		if it.sha != sum256(w.data) || it.size != len(w.data) || it.name != w.name || it.version != w.version {
			t.Errorf("%s: stored as %s %s (%d bytes, sha %s), want %s %s with the source bytes", w.filename, it.name, it.version, it.size, it.sha, w.name, w.version)
		}

		if it.created != created {
			t.Errorf("%s: created-at header %q, want %q", w.filename, it.created, created)
		}

		form := p.forms["py/"+w.filename]
		for field, want := range w.form {
			if got := form.Get(field); got != want {
				t.Errorf("%s: form field %s = %q, want %q", w.filename, field, got, want)
			}
		}
	}
}

// ─── import behavior ──────────────────────────────────────────────────────────

func TestImportPyPINexusUploadsDistributions(t *testing.T) {
	files, wants := pypiNexusFixtures(t)
	nexus := pypiNexusServer(t, files)
	oa := pypiNewOA(t)

	rep := runImportTest(t, pypiNexusCfg(t, nexus), nexus, oa)
	if rep.failed() || rep.Uploaded != 4 || rep.Ignored != 4 {
		t.Fatalf("report = %+v", rep)
	}

	pypiCheckUploaded(t, oa, wants, nexusWant)
}

func TestImportPyPIArtifactory(t *testing.T) {
	for _, mode := range []string{"aql", "storage"} {
		t.Run(mode, func(t *testing.T) {
			named := pypiWheel(t, "acme_tools", "3.1", pypiMeta("acme-tools", "3.1"))
			fromMeta := pypiSdist(t, "my-lib-2.0", pypiMeta("my-lib", "2.0", "Requires-Python: >=3.10"))
			guessed := pypiTarGz(t, [2]string{"plain-0.1/README", "no metadata here"})
			misleading := pypiSdist(t, "wrongprops-1.0", pypiMeta("wrongprops", "1.0"))

			arti := newFakeArtifactory(t, fakeSourceRepo{name: "py-local", format: "pypi", kind: "local", files: []fakeFile{
				{path: "acme/acme_tools-3.1-py3-none-any.whl", data: named, created: artiStamp, props: map[string][]string{
					"pypi.name": {"Acme.Tools"}, "pypi.version": {"3.1"}, "team": {"x"},
				}},
				{path: "my-lib-2.0.tar.gz", data: fromMeta, created: artiStamp},
				{path: "plain-0.1.tar.gz", data: guessed, created: artiStamp},
				// Properties that cannot describe this file are not believed.
				{path: "wrongprops-1.0.tar.gz", data: misleading, created: artiStamp, props: map[string][]string{
					"pypi.name": {"other"}, "pypi.version": {"1.0"},
				}},
				{path: ".pypi/acme-tools.html", data: []byte("<html/>"), created: artiStamp},
				{path: "my-lib-2.0.tar.gz.asc", data: []byte("sig"), created: artiStamp},
			}})
			oa := pypiNewOA(t)

			cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc", "--repo", "py-local:py",
				"--artifactory-listing", mode, "--properties", "pypi.*")
			cfg.SourceToken = "secret"

			rep := runImportTest(t, cfg, arti, oa)
			if rep.failed() || rep.Uploaded != 4 {
				t.Fatalf("report = %+v", rep)
			}

			pypiCheckUploaded(t, oa, []pypiWant{
				{"acme_tools-3.1-py3-none-any.whl", "acme-tools", "3.1", named, map[string]string{"name": "Acme.Tools", "pyversion": "py3"}},
				{"my-lib-2.0.tar.gz", "my-lib", "2.0", fromMeta, map[string]string{"name": "my-lib", "requires_python": ">=3.10"}},
				{"plain-0.1.tar.gz", "plain", "0.1", guessed, map[string]string{"name": "plain", "version": "0.1", "requires_python": ""}},
				{"wrongprops-1.0.tar.gz", "wrongprops", "1.0", misleading, map[string]string{"name": "wrongprops"}},
			}, artiWant)

			// Properties go to the package the upload created.
			if it := oa.find("py", "acme_tools-3.1-py3-none-any.whl"); it.props["pypi.name"] != "Acme.Tools" || len(it.props) != 2 {
				t.Errorf("props = %v, want the allow-listed pypi.name and pypi.version", it.props)
			}
		})
	}
}

func TestImportPyPIIsIdempotent(t *testing.T) {
	files, wants := pypiNexusFixtures(t)
	nexus := pypiNexusServer(t, files)
	oa := pypiNewOA(t)
	cfg := pypiNexusCfg(t, nexus)

	runImportTest(t, cfg, nexus, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 4 || oa.writes != writes {
		t.Fatalf("second run: %+v, writes+=%d, want 4 present and no writes", rep, oa.writes-writes)
	}

	pypiCheckUploaded(t, oa, wants, nexusWant)
}

func TestImportPyPIResumesFromStateFile(t *testing.T) {
	files, _ := pypiNexusFixtures(t)
	nexus := pypiNexusServer(t, files)
	oa := pypiNewOA(t)
	state := filepath.Join(t.TempDir(), "state.jsonl")
	cfg := pypiNexusCfg(t, nexus, "--state-file", state)

	if rep := runImportTest(t, cfg, nexus, oa); rep.Uploaded != 4 {
		t.Fatalf("first run uploaded %d", rep.Uploaded)
	}

	oa.repos["py"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, nexus, oa)
	if rep.Resumed != 4 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}

	raw, err := os.ReadFile(state)
	if err != nil || strings.Count(string(raw), "\n") != 4 {
		t.Fatalf("state file = %q, %v", raw, err)
	}
}

func TestImportPyPIDryRunWritesNothing(t *testing.T) {
	files, _ := pypiNexusFixtures(t)
	nexus := pypiNexusServer(t, files)
	oa := pypiNewOA(t)

	rep := runImportTest(t, pypiNexusCfg(t, nexus, "--dry-run"), nexus, oa)
	if rep.failed() || rep.Uploaded != 4 || !rep.DryRun {
		t.Fatalf("report = %+v", rep)
	}

	if oa.writes != 0 || len(oa.repos["py"].items) != 0 {
		t.Fatalf("dry run wrote: writes=%d items=%d", oa.writes, len(oa.repos["py"].items))
	}
}

func TestImportPyPISourceChecksumMismatchRemovesCopy(t *testing.T) {
	files, wants := pypiNexusFixtures(t)
	files[1].sha256 = sum256([]byte("what the source claimed")) // demo-pkg-1.0.0.tar.gz
	nexus := pypiNexusServer(t, files)
	oa := pypiNewOA(t)

	rep := runImportTest(t, pypiNexusCfg(t, nexus), nexus, oa)
	if rep.Uploaded != 3 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep)
	}

	if f := rep.Failures[0]; !strings.Contains(f.Error, "checksum mismatch") || !strings.HasSuffix(f.Path, "demo-pkg-1.0.0.tar.gz") {
		t.Fatalf("failure = %+v", f)
	}

	if got := pypiState(t, oa).deleted; !slices.Equal(got, []string{"demo-pkg-1.0.0.tar.gz"}) || oa.find("py", "demo-pkg-1.0.0.tar.gz") != nil {
		t.Fatalf("deleted %v: the bad copy was not removed", got)
	}

	pypiCheckUploaded(t, oa, slices.DeleteFunc(slices.Clone(wants), func(w pypiWant) bool { return w.filename == "demo-pkg-1.0.0.tar.gz" }), nexusWant)
}

func TestImportPyPIStoredDigestMismatchRemovesCopies(t *testing.T) {
	files, _ := pypiNexusFixtures(t)
	nexus := pypiNexusServer(t, files)
	oa := pypiNewOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, pypiNexusCfg(t, nexus), nexus, oa)
	if rep.Failed != 4 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}

	if p := pypiState(t, oa); len(p.deleted) != 4 || len(oa.repos["py"].items) != 0 {
		t.Fatalf("deleted %v, %d files left", p.deleted, len(oa.repos["py"].items))
	}
}

func TestImportPyPIConflictWithDifferentContentIsReported(t *testing.T) {
	files, wants := pypiNexusFixtures(t)
	nexus := pypiNexusServer(t, files)
	oa := pypiNewOA(t)
	seeded := &fakeItem{id: oa.nextID(), path: "demo-pkg-1.0.0.tar.gz", name: "demo-pkg", version: "1.0.0", sha: sum256([]byte("someone else's sdist"))}
	oa.repos["py"].items = append(oa.repos["py"].items, seeded)

	rep := runImportTest(t, pypiNexusCfg(t, nexus), nexus, oa)
	if rep.Uploaded != 3 || rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "different content") {
		t.Fatalf("report = %+v", rep)
	}

	if oa.deletes != 0 || oa.find("py", "demo-pkg-1.0.0.tar.gz") != seeded {
		t.Fatalf("the existing file was touched (deletes=%d)", oa.deletes)
	}

	if got := len(oa.repos["py"].items); got != len(wants) {
		t.Fatalf("destination holds %d files, want %d", got, len(wants))
	}
}

func TestImportPyPIReportsUnresolvableIdentity(t *testing.T) {
	egg := pypiZip(t, [2]string{"EGG-INFO/PKG-INFO", pypiMeta("foo", "1.0")})

	for _, tc := range []struct{ name, path string }{
		{"no version in the name and no metadata", "weird.tar.gz"},
		// Modern eggs carry their python tag, which the server's naming rule cannot place.
		{"egg with a python tag", "foo-1.0-py3.8.egg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("not an archive")
			if strings.HasSuffix(tc.path, ".egg") {
				data = egg
			}

			nexus := pypiNexusServer(t, []fakeFile{{path: "packages/x/" + tc.path, data: data, created: nexusStamp}})
			oa := pypiNewOA(t)

			rep := runImportTest(t, pypiNexusCfg(t, nexus), nexus, oa)
			if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "cannot determine the project name and version of "+tc.path) {
				t.Fatalf("report = %+v", rep)
			}

			if oa.writes != 0 {
				t.Fatalf("a file without a usable identity was uploaded (%d writes)", oa.writes)
			}
		})
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func TestImportPyPIImportable(t *testing.T) {
	for p, want := range map[string]bool{
		"packages/demo/1.0/demo-1.0.tar.gz":                    true,
		"packages/demo/1.0/demo-1.0-py3-none-any.whl":          true,
		"packages/demo/1.0/demo-1.0.zip":                       true,
		"packages/demo/1.0/demo-1.0-py3.8.egg":                 true,
		"packages/demo/1.0/demo-1.0.tar.bz2":                   true,
		"packages/demo/1.0/demo-1.0.tar.xz":                    true,
		"packages/demo/1.0/demo-1.0.tar":                       true,
		"packages/demo/1.0/DEMO-1.0.TAR.GZ":                    true,
		"packages/demo/1.0/demo-1.0.tar.gz.asc":                false,
		"packages/demo/1.0/demo-1.0.tar.gz.sha256":             false,
		"packages/demo/1.0/demo-1.0-py3-none-any.whl.metadata": false,
		"packages/demo/1.0/demo-1.0.exe":                       false,
		"packages/demo/1.0/demo-1.0.msi":                       false,
		"packages/demo/1.0/demo-1.0.rpm":                       false,
		"simple/demo/":                                         false,
		"simple/demo/index.html":                               false,
		".pypi/demo.html":                                      false,
	} {
		if got := importablePyPI(p); got != want {
			t.Errorf("importablePyPI(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestImportPyPIIdentityPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		filename      string
		asset         sourceAsset
		md            pypiMetadata
		wantName, ver string
		wantErr       bool
	}{
		{
			name: "the source's component comes first", filename: "foo-1.0.tar.gz",
			asset:    sourceAsset{Component: sourceComponent{Name: "Foo", Version: "1.0"}, Props: map[string][]string{"pypi.name": {"foo"}, "pypi.version": {"1.0"}}},
			md:       pypiMetadata{Name: "foo", Version: "1.0"},
			wantName: "Foo", ver: "1.0",
		},
		{
			name: "a component that does not fit the file falls back to properties", filename: "foo-1.0.tar.gz",
			asset:    sourceAsset{Component: sourceComponent{Name: "bar", Version: "1.0"}, Props: map[string][]string{"pypi.name": {"Foo"}, "pypi.version": {"1.0"}}},
			wantName: "Foo", ver: "1.0",
		},
		{
			name: "properties that do not fit fall back to the archive metadata", filename: "foo-1.0.tar.gz",
			asset:    sourceAsset{Props: map[string][]string{"pypi.name": {"foo"}, "pypi.version": {"2.0"}}},
			md:       pypiMetadata{Name: "foo", Version: "1.0"},
			wantName: "foo", ver: "1.0",
		},
		{
			name: "wheel versions with a dash are written with an underscore", filename: "foo-1.0_1-py3-none-any.whl",
			md:       pypiMetadata{Name: "foo", Version: "1.0-1"},
			wantName: "foo", ver: "1.0-1",
		},
		{
			name: "the file name is the last resort", filename: "foo-bar-1.0.tar.gz",
			md:       pypiMetadata{Name: "unrelated", Version: "9"},
			wantName: "foo-bar", ver: "1.0",
		},
		{
			name: "a wheel's build tag does not confuse the file name reading", filename: "foo-1.0-7-py3-none-any.whl",
			wantName: "foo", ver: "1.0",
		},
		{name: "nothing fits", filename: "weird.tar.gz", wantErr: true},
		{name: "a wheel name that is too short", filename: "foo-1.0.whl", wantErr: true},
		{name: "an egg the server's naming cannot place", filename: "foo-1.0-py3.8.egg", md: pypiMetadata{Name: "foo", Version: "1.0"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, version, err := pypiIdentity(tc.asset, tc.filename, tc.md)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q %q, want an error", name, version)
				}

				return
			}

			if err != nil || name != tc.wantName || version != tc.ver {
				t.Fatalf("got %q %q, %v; want %q %q", name, version, err, tc.wantName, tc.ver)
			}

			if !pypiFakeBelongs(tc.filename, name, version) {
				t.Fatalf("the server would refuse %s as %s %s", tc.filename, name, version)
			}
		})
	}
}

func TestImportPyPIDistType(t *testing.T) {
	for filename, want := range map[string][2]string{
		"foo-1.0-py3-none-any.whl":            {"bdist_wheel", "py3"},
		"foo-1.0-1-cp312-cp312-win_amd64.whl": {"bdist_wheel", "cp312"},
		"foo-1.0-py3.8.egg":                   {"bdist_egg", "py3.8"},
		"foo-1.0-py2.7-linux-x86_64.egg":      {"bdist_egg", "py2.7"},
		"foo-1.0.egg":                         {"bdist_egg", ""},
		"foo-1.0.tar.gz":                      {"sdist", "source"},
		"foo-1.0.zip":                         {"sdist", "source"},
	} {
		if filetype, pyversion := pypiDistType(filename); filetype != want[0] || pyversion != want[1] {
			t.Errorf("pypiDistType(%q) = %q %q, want %q %q", filename, filetype, pyversion, want[0], want[1])
		}
	}
}

// pypiBZ2SDist is a tar.bz2 holding bz-pkg-1.0/PKG-INFO (Name: Bz.Pkg, Version:
// 1.0, Requires-Python: >=3.9); the standard library only reads bzip2.
const pypiBZ2SDist = "QlpoOTFBWSZTWV6usu4AAIVfgsyQQAP9sxGr0QB26/4wABAECCAAlA0lPFHlPTUAG1GRo2kDQaPT1QaUjE9Am0AAIwABNMSK9GQAIAyQIAgc5g0somQBBABs/ixfVN4BGPav+qJ4JmmQQnCAQqeQYFLhhgnfs2rQ1KWei78Giut8XHZVZHIxQRi/ByLTou8WiNNwbzF+uTpFIS00rVgJ4apOMzveZwIiA/i7kinChIL1dZdw"

func TestImportPyPIReadMetadata(t *testing.T) {
	info := pypiMeta("Pkg", "1.2", "Summary: s", "Requires-Python: >=3.9")
	bz2, err := base64.StdEncoding.DecodeString(pypiBZ2SDist)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		filename string
		data     []byte
		want     pypiMetadata
		wantErr  string
	}{
		{"wheel", "pkg-1.2-py3-none-any.whl", pypiWheel(t, "pkg", "1.2", info), pypiMetadata{Name: "Pkg", Version: "1.2", Summary: "s", RequiresPython: ">=3.9"}, ""},
		{"sdist", "pkg-1.2.tar.gz", pypiSdist(t, "pkg-1.2", info), pypiMetadata{Name: "Pkg", Version: "1.2", Summary: "s", RequiresPython: ">=3.9"}, ""},
		{"plain tar", "pkg-1.2.tar", pypiTar(t, [2]string{"pkg-1.2/PKG-INFO", info}), pypiMetadata{Name: "Pkg", Version: "1.2", Summary: "s", RequiresPython: ">=3.9"}, ""},
		{"bzip2 sdist", "bz-pkg-1.0.tar.bz2", bz2, pypiMetadata{Name: "Bz.Pkg", Version: "1.0", RequiresPython: ">=3.9"}, ""},
		{"zip sdist", "pkg-1.2.zip", pypiZip(t, [2]string{"pkg-1.2/PKG-INFO", info}), pypiMetadata{Name: "Pkg", Version: "1.2", Summary: "s", RequiresPython: ">=3.9"}, ""},
		{"egg", "pkg-1.2.egg", pypiZip(t, [2]string{"EGG-INFO/PKG-INFO", info}), pypiMetadata{Name: "Pkg", Version: "1.2", Summary: "s", RequiresPython: ">=3.9"}, ""},
		{
			"an sdist's egg-info is the fallback and the top-level file wins", "pkg-1.2.tar.gz",
			pypiTarGz(t,
				[2]string{"pkg-1.2/pkg.egg-info/PKG-INFO", pypiMeta("Pkg", "1.2", "Summary: from egg-info")},
				[2]string{"pkg-1.2/PKG-INFO", pypiMeta("Pkg", "1.2", "Summary: top level")}),
			pypiMetadata{Name: "Pkg", Version: "1.2", Summary: "top level"}, "",
		},
		{
			"egg-info alone", "pkg-1.2.tar.gz",
			pypiTarGz(t, [2]string{"pkg-1.2/pkg.egg-info/PKG-INFO", pypiMeta("Pkg", "1.2", "Summary: from egg-info")}),
			pypiMetadata{Name: "Pkg", Version: "1.2", Summary: "from egg-info"}, "",
		},
		{"no metadata in an sdist", "pkg-1.2.tar.gz", pypiTarGz(t, [2]string{"pkg-1.2/setup.py", "x"}), pypiMetadata{}, "no METADATA or PKG-INFO"},
		{"no metadata in a wheel", "pkg-1.2-py3-none-any.whl", pypiZip(t, [2]string{"pkg/x.py", "x"}), pypiMetadata{}, "no METADATA or PKG-INFO"},
		{"corrupt archive", "pkg-1.2.tar.gz", []byte("junk"), pypiMetadata{}, "cannot read the package metadata"},
		{"formats without a reader", "pkg-1.2.tar.xz", []byte("junk"), pypiMetadata{}, "no reader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, err := spool(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = sp.Close() }()

			got, err := pypiReadMetadata(sp, tc.filename)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}

				return
			}

			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("metadata = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestImportPyPIParseHeaders(t *testing.T) {
	raw := "Metadata-Version: 2.1\r\nName: Pkg\r\nSummary: one line\r\nKeywords: a,b\r\nLicense: line one\r\n line two\r\n\tline three\r\n" +
		"Classifier: A\r\nClassifier: B\r\nAuthor-email: Ada <ada@example.org>\r\n\r\nName: from the description\r\n"

	h := pypiParseHeaders(strings.NewReader(raw))

	if got := h.first("name"); got != "Pkg" {
		t.Errorf("name = %q: the description body must not be read", got)
	}

	if got := h.first("license"); got != "line one\nline two\nline three" {
		t.Errorf("license = %q", got)
	}

	if got := h["classifier"]; !slices.Equal(got, []string{"A", "B"}) {
		t.Errorf("classifiers = %v", got)
	}

	md := pypiMetadataFrom(h)
	if md.License != "" || md.AuthorEmail != "Ada <ada@example.org>" || md.Keywords != "a,b" || !slices.Equal(md.Classifiers, []string{"A", "B"}) {
		t.Errorf("metadata = %+v: a multi-line license must not become a form field", md)
	}

	if got := pypiMetadataFrom(pypiParseHeaders(strings.NewReader("License-Expression: MIT\nLicense: legacy text\n"))).License; got != "MIT" {
		t.Errorf("license = %q, want the SPDX expression to win", got)
	}

	if got := pypiParseHeaders(strings.NewReader("")); len(got) != 0 {
		t.Errorf("headers of nothing = %v", got)
	}
}

func TestImportPyPINormalizeAndSplit(t *testing.T) {
	for in, want := range map[string]string{"Foo.Bar_baz--qux": "foo-bar-baz-qux", "demo": "demo", "A_B": "a-b"} {
		if got := pypiNormalizeName(in); got != want {
			t.Errorf("pypiNormalizeName(%q) = %q, want %q", in, got, want)
		}
	}

	for _, tc := range []struct {
		filename, version, dist string
		ok                      bool
	}{
		{"foo-1.0.tar.gz", "1.0", "foo", true},
		{"foo-bar-1.0.tar.gz", "1.0", "foo-bar", true},
		{"foo-1.0.tar.gz", "2.0", "", false},
		{"-1.0.tar.gz", "1.0", "", false},
		{"foo-1.0-py3-none-any.whl", "1.0", "foo", true},
		{"foo-1.0_1-py3-none-any.whl", "1.0-1", "foo", true},
		{"foo-1.0-any.whl", "1.0", "", false},
		{"foo-1.0.weird", "1.0", "foo", true},
		{"foo-1.0.tar.Z", "1.0", "foo", true},
	} {
		if dist, ok := pypiSplitFilename(tc.filename, tc.version); ok != tc.ok || (ok && dist != tc.dist) {
			t.Errorf("pypiSplitFilename(%q, %q) = %q %v, want %q %v", tc.filename, tc.version, dist, ok, tc.dist, tc.ok)
		}
	}
}
