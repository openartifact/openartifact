// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// cargoFakes keeps, per fakeOA, the publish metadata its registry received.
var cargoFakes sync.Map //nolint:gochecknoglobals // per-server state of the fake, looked up by the test that built the server

type cargoFake struct {
	published map[string]map[string]any
	// yanked holds repo/file of every crate version the registry has yanked;
	// yankCalls counts the yank requests received and yankStatus, when set,
	// is the status they are refused with (409: immutable releases).
	yanked     map[string]bool
	yankCalls  int
	yankStatus int
}

// cargoPublishedMeta is the metadata the fake registry received for a crate file.
func cargoPublishedMeta(t *testing.T, oa *fakeOA, repo, file string) map[string]any {
	t.Helper()

	st, _ := cargoFakes.Load(oa)

	meta := st.(*cargoFake).published[repo+"/"+file]
	if meta == nil {
		t.Fatalf("no publish metadata recorded for %s/%s", repo, file)
	}

	return meta
}

// The fake registry speaks `cargo publish`: PUT /api/v1/crates/new with
// <u32 LE json length><json><u32 LE crate length><crate>, 409 on a duplicate
// version and a 200 {"warnings":[]} answer without any id or digest.
func init() { //nolint:gochecknoinits // the fake destination route registry is filled by each format tests
	fakeOARoutes[string(formatCargo)] = func(f *fakeOA, mux *http.ServeMux) {
		st := &cargoFake{published: map[string]map[string]any{}, yanked: map[string]bool{}}
		cargoFakes.Store(f, st)
		mux.HandleFunc("PUT /api/v1/crates/new", func(w http.ResponseWriter, r *http.Request) { st.publish(f, w, r) })
		mux.HandleFunc("DELETE /api/v1/crates/{crate}/{version}/yank", func(w http.ResponseWriter, r *http.Request) { st.yank(f, w, r) })
	}
}

// yank is DELETE /api/v1/crates/{crate}/{version}/yank: 200 {"ok":true}, also for
// a version that is yanked already, 404 for an unknown version.
func (c *cargoFake) yank(f *fakeOA, w http.ResponseWriter, r *http.Request) {
	f.writes++
	c.yankCalls++

	if c.yankStatus != 0 {
		cargoFakeError(f, w, c.yankStatus, "repository has immutable releases")
		return
	}

	repo, file := r.Header.Get("X-Fake-Repo"), r.PathValue("crate")+"-"+r.PathValue("version")+".crate"
	if f.find(repo, file) == nil {
		http.NotFound(w, r)
		return
	}

	c.yanked[repo+"/"+file] = true

	f.reply(w, http.StatusOK, map[string]any{"ok": true})
}

func cargoFakeError(f *fakeOA, w http.ResponseWriter, code int, detail string) {
	f.reply(w, code, map[string]any{"errors": []map[string]string{{"detail": detail}}})
}

func (c *cargoFake) publish(f *fakeOA, w http.ResponseWriter, r *http.Request) {
	f.writes++

	repo := r.Header.Get("X-Fake-Repo")

	meta, crate, err := cargoFakeRead(r.Body)
	if err != nil {
		cargoFakeError(f, w, http.StatusBadRequest, err.Error())
		return
	}

	if problem := cargoFakeCheck(meta); problem != "" {
		cargoFakeError(f, w, http.StatusBadRequest, problem)
		return
	}

	file := meta["name"].(string) + "-" + meta["vers"].(string) + ".crate"
	if f.find(repo, file) != nil {
		cargoFakeError(f, w, http.StatusConflict, "crate version "+file+" already exists")
		return
	}

	it := f.store(repo, file, crate, r.Header.Get(headerImportCreatedAt))
	it.name, it.version = meta["name"].(string), meta["vers"].(string)
	it.sha = f.reported(it)
	c.published[repo+"/"+file] = meta

	f.reply(w, http.StatusOK, map[string]any{"warnings": map[string]any{"invalid_categories": []string{}}})
}

// cargoFakeRead splits a publish body and refuses anything but exact framing.
func cargoFakeRead(body io.Reader) (map[string]any, []byte, error) {
	readFrame := func(limit uint32) ([]byte, error) {
		var n [4]byte
		if _, err := io.ReadFull(body, n[:]); err != nil {
			return nil, errors.New("short frame header")
		}

		size := binary.LittleEndian.Uint32(n[:])
		if size > limit {
			return nil, errors.New("frame too large")
		}

		buf := make([]byte, size)
		if _, err := io.ReadFull(body, buf); err != nil {
			return nil, errors.New("short frame")
		}

		return buf, nil
	}

	raw, err := readFrame(1 << 20)
	if err != nil {
		return nil, nil, err
	}

	crate, err := readFrame(128 << 20)
	if err != nil {
		return nil, nil, err
	}

	if rest, _ := io.Copy(io.Discard, body); rest != 0 {
		return nil, nil, errors.New("trailing bytes after the crate")
	}

	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, nil, errors.New("metadata is not JSON")
	}

	return meta, crate, nil
}

// cargoFakeCheck verifies the metadata has the shape the registry's index builder
// relies on; it returns the first problem found.
func cargoFakeCheck(meta map[string]any) string {
	for _, k := range []string{"name", "vers"} {
		if s, _ := meta[k].(string); s == "" {
			return k + " missing"
		}
	}

	if _, ok := meta["features"].(map[string]any); !ok {
		return "features must be an object"
	}

	for _, k := range []string{"deps", "authors", "keywords", "categories"} {
		if _, ok := meta[k].([]any); !ok {
			return k + " must be an array"
		}
	}

	for _, k := range []string{"links", "rust_version"} {
		if _, ok := meta[k]; !ok {
			return k + " missing"
		}
	}

	deps, _ := meta["deps"].([]any)
	for _, d := range deps {
		dep, _ := d.(map[string]any)
		if problem := cargoFakeCheckDep(dep); problem != "" {
			return problem
		}
	}

	return ""
}

func cargoFakeCheckDep(dep map[string]any) string {
	for _, k := range []string{"name", "version_req"} {
		if s, _ := dep[k].(string); s == "" {
			return "dep " + k + " missing"
		}
	}

	if _, ok := dep["features"].([]any); !ok {
		return "dep features must be an array"
	}

	for _, k := range []string{"optional", "default_features"} {
		if _, ok := dep[k].(bool); !ok {
			return "dep " + k + " must be a boolean"
		}
	}

	switch dep["kind"] {
	case "normal", "dev", "build":
	default:
		return "dep kind invalid"
	}

	for _, k := range []string{"target", "registry", "explicit_name_in_toml"} {
		if _, ok := dep[k]; !ok {
			return "dep " + k + " missing"
		}
	}

	return ""
}

// ─── crate fixtures ───────────────────────────────────────────────────────────

type crateEntry struct{ name, body string }

// buildCrate returns a .crate (gzip-compressed tar) holding the entries in order.
func buildCrate(t *testing.T, entries ...crateEntry) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// simpleCrate is a minimal crate with no dependencies.
func simpleCrate(t *testing.T, name, vers string) []byte {
	t.Helper()

	root := name + "-" + vers + "/"

	return buildCrate(t,
		crateEntry{root + "Cargo.toml", "[package]\nname = \"" + name + "\"\nversion = \"" + vers + "\"\n"},
		crateEntry{root + "src/lib.rs", "// " + name + "\n"})
}

// fullCargoToml exercises every dependency shape; the "mine" dependency lives in
// the registry the crate is imported from (host arti.example:8081, repo cargo-local).
const fullCargoToml = `
[package]
name = "demo"
version = "1.2.0"
edition = "2021"
authors = ["Ann <ann@example.com>"]
description = "A demo crate"
documentation = "https://docs.example.com/demo"
homepage = "https://example.com"
readme = "README.md"
keywords = ["demo", "test"]
categories = ["development-tools"]
license = "MIT OR Apache-2.0"
repository = "https://git.example/demo"
rust-version = "1.70"
links = "demo"

[features]
default = ["serde"]
derive = ["serde/derive"]

[dependencies]
log = "0.4"
serde = { version = "1.0", features = ["derive"], optional = true }
rt = { package = "tokio", version = "1", default-features = false, features = ["rt"] }
other = { version = "2", registry-index = "sparse+https://other.example/index/" }
mine = { version = "0.3", registry-index = "sparse+http://arti.example:8081/artifactory/api/cargo/cargo-local/index/" }

[dev-dependencies]
tempfile = "3"

[build-dependencies]
cc = "1"

[target.'cfg(unix)'.dependencies]
libc = "0.2"
`

const fullCargoMetadata = `{
 "name": "demo", "vers": "1.2.0",
 "authors": ["Ann <ann@example.com>"],
 "description": "A demo crate",
 "documentation": "https://docs.example.com/demo",
 "homepage": "https://example.com",
 "readme": "# demo\n", "readme_file": "README.md",
 "keywords": ["demo", "test"], "categories": ["development-tools"],
 "license": "MIT OR Apache-2.0", "repository": "https://git.example/demo",
 "links": "demo", "rust_version": "1.70",
 "features": {"default": ["serde"], "derive": ["serde/derive"]},
 "deps": [
  {"name": "log", "version_req": "0.4", "features": [], "optional": false, "default_features": true,
   "target": null, "kind": "normal", "registry": "https://github.com/rust-lang/crates.io-index", "explicit_name_in_toml": null},
  {"name": "mine", "version_req": "0.3", "features": [], "optional": false, "default_features": true,
   "target": null, "kind": "normal", "registry": null, "explicit_name_in_toml": null},
  {"name": "other", "version_req": "2", "features": [], "optional": false, "default_features": true,
   "target": null, "kind": "normal", "registry": "sparse+https://other.example/index/", "explicit_name_in_toml": null},
  {"name": "tokio", "version_req": "1", "features": ["rt"], "optional": false, "default_features": false,
   "target": null, "kind": "normal", "registry": "https://github.com/rust-lang/crates.io-index", "explicit_name_in_toml": "rt"},
  {"name": "serde", "version_req": "1.0", "features": ["derive"], "optional": true, "default_features": true,
   "target": null, "kind": "normal", "registry": "https://github.com/rust-lang/crates.io-index", "explicit_name_in_toml": null},
  {"name": "tempfile", "version_req": "3", "features": [], "optional": false, "default_features": true,
   "target": null, "kind": "dev", "registry": "https://github.com/rust-lang/crates.io-index", "explicit_name_in_toml": null},
  {"name": "cc", "version_req": "1", "features": [], "optional": false, "default_features": true,
   "target": null, "kind": "build", "registry": "https://github.com/rust-lang/crates.io-index", "explicit_name_in_toml": null},
  {"name": "libc", "version_req": "0.2", "features": [], "optional": false, "default_features": true,
   "target": "cfg(unix)", "kind": "normal", "registry": "https://github.com/rust-lang/crates.io-index", "explicit_name_in_toml": null}
 ]
}`

func fullCrate(t *testing.T) []byte {
	t.Helper()

	return buildCrate(t,
		crateEntry{"demo-1.2.0/Cargo.toml", fullCargoToml},
		crateEntry{"demo-1.2.0/README.md", "# demo\n"},
		crateEntry{"demo-1.2.0/src/lib.rs", "pub fn demo() {}\n"})
}

func cargoSourceAsset(path string) sourceAsset {
	return sourceAsset{
		Repo: "cargo-local", Path: path,
		URL: "http://arti.example:8081/cargo-local/" + path,
	}
}

// ─── metadata mapping ─────────────────────────────────────────────────────────

func TestImportCargoMetadataFromManifest(t *testing.T) {
	sp, err := spool(bytes.NewReader(fullCrate(t)))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sp.Close() }()

	pub, meta, err := cargoMetadata(sp, cargoSourceAsset("crates/demo/demo-1.2.0.crate"))
	if err != nil {
		t.Fatal(err)
	}

	var got, want any

	if err := json.Unmarshal(meta, &got); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal([]byte(fullCargoMetadata), &want); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("publish metadata:\n got %s\nwant %s", meta, fullCargoMetadata)
	}

	if pub.Name != "demo" || pub.Vers != "1.2.0" {
		t.Fatalf("coordinates = %s %s", pub.Name, pub.Vers)
	}
}

func TestImportCargoMetadataReadme(t *testing.T) {
	manifest := func(readme string) crateEntry {
		return crateEntry{"d-1.0.0/Cargo.toml", "[package]\nname = \"d\"\nversion = \"1.0.0\"\nreadme = \"" + readme + "\"\n"}
	}

	cases := map[string]struct {
		entries    []crateEntry
		wantReadme string
		wantFile   string
	}{
		"embedded":         {[]crateEntry{manifest("README.md"), {"d-1.0.0/README.md", "hello"}}, "hello", "README.md"},
		"missing":          {[]crateEntry{manifest("README.md")}, "", "README.md"},
		"outside the root": {[]crateEntry{manifest("../README.md"), {"README.md", "nope"}}, "", "../README.md"},
		"too large":        {[]crateEntry{manifest("README.md"), {"d-1.0.0/README.md", strings.Repeat("x", cargoMaxReadme+1)}}, "", "README.md"},
		"not declared": {[]crateEntry{
			{"d-1.0.0/Cargo.toml", "[package]\nname = \"d\"\nversion = \"1.0.0\"\n"}, {"d-1.0.0/README.md", "hello"},
		}, "", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sp, err := spool(bytes.NewReader(buildCrate(t, tc.entries...)))
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = sp.Close() }()

			pub, _, err := cargoMetadata(sp, cargoSourceAsset("crates/d/d-1.0.0.crate"))
			if err != nil {
				t.Fatal(err)
			}

			if pub.Readme != tc.wantReadme || pub.ReadmeFile != tc.wantFile {
				t.Fatalf("readme = %q (file %q), want %q (file %q)", pub.Readme, pub.ReadmeFile, tc.wantReadme, tc.wantFile)
			}
		})
	}
}

// cargoDepSummary renders "kind name req" for every dependency.
func cargoDepSummary(pub cargoPublish) []string {
	out := make([]string, 0, len(pub.Deps))
	for _, d := range pub.Deps {
		out = append(out, d.Kind+" "+d.Name+" "+d.VersionReq)
	}

	return out
}

func TestImportCargoManifestShapes(t *testing.T) {
	cases := map[string]struct {
		manifest string
		want     []string
		wantErr  error
	}{
		"underscore tables": {
			"[package]\nname='a'\nversion='1.0.0'\n[dev_dependencies]\nx='1'\n[build_dependencies]\ny='2'\n",
			[]string{"dev x 1", "build y 2"}, nil,
		},
		"dotted table": {
			"[package]\nname='a'\nversion='1.0.0'\n[dependencies.x]\nversion='1'\nfeatures=['f']\n",
			[]string{"normal x 1"}, nil,
		},
		"path dependency has no requirement": {
			"[package]\nname='a'\nversion='1.0.0'\n[dependencies]\nx={path='../x'}\n",
			[]string{"normal x *"}, nil,
		},
		"targets sorted": {
			"[package]\nname='a'\nversion='1.0.0'\n[target.'cfg(windows)'.dependencies]\nw='1'\n[target.'cfg(unix)'.dependencies]\nu='1'\n",
			[]string{"normal u 1", "normal w 1"}, nil,
		},
		"no package":      {"[dependencies]\nx='1'\n", nil, errCargoNoPackage},
		"no version":      {"[package]\nname='a'\n", nil, errCargoNoPackage},
		"odd dependency":  {"[package]\nname='a'\nversion='1.0.0'\n[dependencies]\nx=3\n", nil, nil},
		"named registry":  {"[package]\nname='a'\nversion='1.0.0'\n[dependencies]\nx={version='1',registry='corp'}\n", nil, nil},
		"crates-io named": {"[package]\nname='a'\nversion='1.0.0'\n[dependencies]\nx={version='1',registry='crates-io'}\n", []string{"normal x 1"}, nil},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var manifest map[string]any
			if err := toml.Unmarshal([]byte(tc.manifest), &manifest); err != nil {
				t.Fatal(err)
			}

			pub, err := cargoPublishFromManifest(manifest, cargoRegistries{})

			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case tc.want == nil:
				if err == nil {
					t.Fatalf("expected an error, got deps %v", cargoDepSummary(pub))
				}
			case err != nil:
				t.Fatal(err)
			case !reflect.DeepEqual(cargoDepSummary(pub), tc.want):
				t.Fatalf("deps = %v, want %v", cargoDepSummary(pub), tc.want)
			}
		})
	}
}

func TestImportCargoSourceRegistryDetection(t *testing.T) {
	artifactory := cargoSourceAsset("crates/d/d-1.0.0.crate")
	nexus := sourceAsset{Repo: "crates", URL: "http://nexus.example:8081/repository/crates/crates/d/d-1.0.0.crate"}

	cases := map[string]struct {
		asset sourceAsset
		index string
		same  bool
	}{
		"artifactory index":        {artifactory, "sparse+http://arti.example:8081/artifactory/api/cargo/cargo-local/index/", true},
		"nexus index":              {nexus, "sparse+http://nexus.example:8081/repository/crates/", true},
		"host case":                {nexus, "sparse+http://NEXUS.example:8081/repository/crates/", true},
		"other repository":         {nexus, "sparse+http://nexus.example:8081/repository/other/", false},
		"other host":               {nexus, "sparse+http://other.example:8081/repository/crates/", false},
		"another registry type":    {artifactory, "sparse+https://index.crates.io/", false},
		"unparsable source":        {sourceAsset{Repo: "crates", URL: "::"}, "sparse+http://nexus.example:8081/repository/crates/", false},
		"repo as name prefix only": {nexus, "sparse+http://nexus.example:8081/repository/crates-mirror/", false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			regs := newCargoRegistries(tc.asset)

			got, err := regs.index(map[string]any{"registry-index": tc.index})
			if err != nil {
				t.Fatal(err)
			}

			if same := got == nil; same != tc.same {
				t.Fatalf("index %q: same registry = %v, want %v", tc.index, same, tc.same)
			}

			if !tc.same && *got != tc.index {
				t.Fatalf("foreign index rewritten to %q", *got)
			}
		})
	}
}

func TestImportCargoImportable(t *testing.T) {
	for p, want := range map[string]bool{
		"crates/demo/demo-1.0.0.crate":        true,
		"demo-1.0.0.crate":                    true,
		"config.json":                         false,
		"index/de/mo/demo":                    false,
		"crates/demo/demo-1.0.0.crate.sha256": false,
		"api/v1/crates/demo/1.0.0/download":   false,
		".index/demo":                         false,
	} {
		if got := importable(formatCargo, p); got != want {
			t.Errorf("importable(%q) = %v, want %v", p, got, want)
		}
	}
}

// ─── import runs ──────────────────────────────────────────────────────────────

func cargoOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"crates": {format: "cargo", repoType: "hosted"}})
}

func cargoCfg(t *testing.T, kind string, src *httptest.Server, extra ...string) importConfig {
	t.Helper()

	args := []string{kind, "--source-url", src.URL, "--repo", "src:crates"}
	if kind == "artifactory" {
		args = append(args, "--source-user", "svc")
	}

	cfg := parseCfg(t, append(args, extra...)...)
	if kind == "artifactory" {
		cfg.SourceToken = "secret"
	}

	return cfg
}

func cargoArtifactory(t *testing.T, files ...fakeFile) *httptest.Server {
	t.Helper()

	return newFakeArtifactory(t, fakeSourceRepo{name: "src", format: "cargo", kind: "local", files: files})
}

func cargoNexus(t *testing.T, files ...fakeFile) *httptest.Server {
	t.Helper()

	return newFakeNexus(t, fakeSourceRepo{name: "src", format: "cargo", kind: "hosted", files: files})
}

func TestImportCargoArtifactory(t *testing.T) {
	demo, other := fullCrate(t), simpleCrate(t, "demo", "1.0.0")
	arti := cargoArtifactory(t,
		fakeFile{path: "crates/demo/demo-1.2.0.crate", data: demo, created: artiStamp, props: map[string][]string{"build.number": {"7"}}},
		fakeFile{path: "crates/demo/demo-1.0.0.crate", data: other, created: artiStamp},
		fakeFile{path: "index/de/mo/demo", data: []byte(`{"name":"demo"}`), created: artiStamp},
		fakeFile{path: "config.json", data: []byte(`{"dl":"x"}`), created: artiStamp},
		fakeFile{path: ".cargo/state", data: []byte("x"), created: artiStamp},
	)
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.failed() || rep.Uploaded != 2 || rep.Ignored != 3 {
		t.Fatalf("report = %+v", rep)
	}

	it := oa.find("crates", "demo-1.2.0.crate")
	if it == nil || it.name != "demo" || it.version != "1.2.0" || it.sha != sum256(demo) || it.created != artiWant {
		t.Fatalf("item = %+v", it)
	}

	if it.props["build.number"] != "7" {
		t.Fatalf("props = %v", it.props)
	}

	meta := cargoPublishedMeta(t, oa, "crates", "demo-1.2.0.crate")
	if meta["readme"] != "# demo\n" || meta["rust_version"] != "1.70" || meta["links"] != "demo" {
		t.Fatalf("publish metadata = %v", meta)
	}

	if deps, _ := meta["deps"].([]any); len(deps) != 8 {
		t.Fatalf("published %d deps, want 8", len(deps))
	}

	if oa.find("crates", "demo-1.0.0.crate") == nil || len(oa.repos["crates"].items) != 2 {
		t.Fatalf("destination = %+v", oa.repos["crates"].items)
	}

	if rep.Bytes != int64(len(demo)+len(other)) {
		t.Fatalf("bytes = %d", rep.Bytes)
	}
}

func TestImportCargoNexus(t *testing.T) {
	crate := simpleCrate(t, "tool", "0.3.1")
	nexus := cargoNexus(t,
		fakeFile{path: "crates/tool/tool-0.3.1.crate", data: crate, created: nexusStamp, compName: "tool", compVersion: "0.3.1"},
		fakeFile{path: "config.json", data: []byte("{}"), created: nexusStamp},
	)
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "nexus", nexus), nexus, oa)
	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 1 {
		t.Fatalf("report = %+v", rep)
	}

	it := oa.find("crates", "tool-0.3.1.crate")
	if it == nil || it.sha != sum256(crate) || it.created != nexusWant {
		t.Fatalf("item = %+v", it)
	}
}

func TestImportCargoRerunIsIdempotent(t *testing.T) {
	arti := cargoArtifactory(t,
		fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp},
		fakeFile{path: "crates/b/b-2.0.0.crate", data: simpleCrate(t, "b", "2.0.0"), created: artiStamp})
	oa := cargoOA(t)
	cfg := cargoCfg(t, "artifactory", arti)

	runImportTest(t, cfg, arti, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, arti, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 2 || oa.writes != writes {
		t.Fatalf("report = %+v, writes += %d", rep, oa.writes-writes)
	}
}

func TestImportCargoResumesFromStateFile(t *testing.T) {
	arti := cargoArtifactory(t, fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp})
	oa := cargoOA(t)
	cfg := cargoCfg(t, "artifactory", arti, "--state-file", filepath.Join(t.TempDir(), "state.jsonl"))

	if rep := runImportTest(t, cfg, arti, oa); rep.Uploaded != 1 {
		t.Fatalf("first run = %+v", rep)
	}

	oa.repos["crates"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, arti, oa)
	if rep.Resumed != 1 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}
}

func TestImportCargoDryRunWritesNothing(t *testing.T) {
	arti := cargoArtifactory(t, fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp})
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti, "--dry-run"), arti, oa)
	if rep.failed() || rep.Uploaded != 1 || !rep.DryRun || oa.writes != 0 || len(oa.repos["crates"].items) != 0 {
		t.Fatalf("report = %+v writes=%d", rep, oa.writes)
	}
}

// The registry cannot delete a published version, so a crate that does not match
// what the source advertised must never be sent.
func TestImportCargoChecksumMismatchIsNotUploaded(t *testing.T) {
	good, bad := simpleCrate(t, "good", "1.0.0"), simpleCrate(t, "bad", "1.0.0")
	arti := cargoArtifactory(t,
		fakeFile{path: "crates/good/good-1.0.0.crate", data: good, created: artiStamp},
		fakeFile{path: "crates/bad/bad-1.0.0.crate", data: bad, created: artiStamp, sha256: sum256([]byte("original"))})
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.Uploaded != 1 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep)
	}

	if f := rep.Failures[0]; !strings.Contains(f.Error, "checksum mismatch") || !strings.HasSuffix(f.Path, "bad-1.0.0.crate") {
		t.Fatalf("failure = %+v", f)
	}

	if oa.find("crates", "bad-1.0.0.crate") != nil || oa.writes != 1 || oa.deletes != 0 {
		t.Fatalf("the corrupt crate reached the registry (writes=%d, deletes=%d)", oa.writes, oa.deletes)
	}
}

func TestImportCargoServerSideChecksumMismatchIsFailure(t *testing.T) {
	arti := cargoArtifactory(t, fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp})
	oa := cargoOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}

	if !strings.Contains(rep.Failures[0].Error, "cannot remove") {
		t.Fatalf("failure should say the copy cannot be removed: %s", rep.Failures[0].Error)
	}
}

func TestImportCargoConflictIsReported(t *testing.T) {
	arti := cargoArtifactory(t, fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp})
	oa := cargoOA(t)
	oa.repos["crates"].items = []*fakeItem{{
		id: "old", path: "a-1.0.0.crate", name: "a", version: "1.0.0", sha: sum256([]byte("another build")),
	}}

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "already exists with different content") {
		t.Fatalf("report = %+v", rep)
	}

	if it := oa.find("crates", "a-1.0.0.crate"); it == nil || it.sha != sum256([]byte("another build")) {
		t.Fatalf("existing crate was touched: %+v", it)
	}
}

func TestImportCargoUnreadableCrateIsNotUploaded(t *testing.T) {
	noManifest := buildCrate(t, crateEntry{"a-1.0.0/src/lib.rs", "x"})
	badToml := buildCrate(t, crateEntry{"a-1.0.0/Cargo.toml", "[package\n"})
	notGzip := []byte("this is not a crate")

	for name, data := range map[string][]byte{"no manifest": noManifest, "invalid toml": badToml, "not an archive": notGzip} {
		t.Run(name, func(t *testing.T) {
			arti := cargoArtifactory(t, fakeFile{path: "crates/a/a-1.0.0.crate", data: data, created: artiStamp})
			oa := cargoOA(t)

			rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
			if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "unreadable crate") || oa.writes != 0 {
				t.Fatalf("report = %+v writes=%d", rep, oa.writes)
			}
		})
	}
}
