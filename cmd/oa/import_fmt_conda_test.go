// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// condaFakes keeps, per fakeOA, the subdir each stored package file lives in.
var condaFakes sync.Map //nolint:gochecknoglobals // per-server state of the fake, looked up by the test that built the server

type condaFake struct{ subdir map[*fakeItem]string }

// The fake channel speaks the conda upload protocol: POST /<subdir> with the
// package file name in X-Filename, 409 on a duplicate subdir/file, a 201
// {id,name,version,sha256} answer, and DELETE /<subdir>/<file>.
func init() { //nolint:gochecknoinits // the fake destination route registry is filled by each format tests
	fakeOARoutes[string(formatConda)] = func(f *fakeOA, mux *http.ServeMux) {
		st := &condaFake{subdir: map[*fakeItem]string{}}
		condaFakes.Store(f, st)
		mux.HandleFunc("POST /{subdir}", func(w http.ResponseWriter, r *http.Request) { st.upload(f, w, r) })
		mux.HandleFunc("DELETE /{subdir}/{file}", func(w http.ResponseWriter, r *http.Request) { st.remove(f, w, r) })
	}
}

// condaFakeParse splits <name>-<version>-<build>.conda|.tar.bz2 from the right,
// as the package names may themselves contain dashes.
func condaFakeParse(file string) (string, string, string, bool) {
	base, ok := strings.CutSuffix(file, ".conda")
	if !ok {
		base, ok = strings.CutSuffix(file, ".tar.bz2")
	}

	parts := strings.Split(base, "-")
	if !ok || len(parts) < 3 {
		return "", "", "", false
	}

	n := len(parts)

	return strings.Join(parts[:n-2], "-"), parts[n-2], parts[n-1], true
}

// find returns the stored copy of file in subdir.
func (c *condaFake) find(repo *fakeRepo, subdir, file string) (int, *fakeItem) {
	for i, it := range repo.items {
		if it.path == file && c.subdir[it] == subdir {
			return i, it
		}
	}

	return -1, nil
}

func (c *condaFake) upload(f *fakeOA, w http.ResponseWriter, r *http.Request) {
	f.writes++

	repo := f.repos[r.Header.Get("X-Fake-Repo")]
	subdir, file := r.PathValue("subdir"), r.Header.Get("X-Filename")

	name, version, _, ok := condaFakeParse(file)
	if !ok {
		f.reply(w, http.StatusBadRequest, map[string]string{"error": "invalid conda package file name " + file})
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		f.reply(w, http.StatusBadRequest, map[string]string{"error": "content type " + ct})
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		f.reply(w, http.StatusBadRequest, map[string]string{"error": "incomplete body"})
		return
	}

	if _, dup := c.find(repo, subdir, file); dup != nil {
		f.reply(w, http.StatusConflict, map[string]string{"error": "package already exists"})
		return
	}

	it := &fakeItem{
		id: f.nextID(), path: file, name: name, version: version,
		sha: sum256(data), size: len(data), created: r.Header.Get(headerImportCreatedAt),
	}
	it.sha = f.reported(it)
	c.subdir[it] = subdir
	repo.items = append(repo.items, it)

	f.reply(w, http.StatusCreated, map[string]string{"id": it.id, "name": name, "version": version, "sha256": it.sha})
}

func (c *condaFake) remove(f *fakeOA, w http.ResponseWriter, r *http.Request) {
	f.writes++

	repo := f.repos[r.Header.Get("X-Fake-Repo")]

	i, it := c.find(repo, r.PathValue("subdir"), r.PathValue("file"))
	if it == nil {
		http.NotFound(w, r)
		return
	}

	f.deletes++
	repo.items = append(repo.items[:i], repo.items[i+1:]...)
	delete(c.subdir, it)
	w.WriteHeader(http.StatusNoContent)
}

// condaSubdirOf is the subdir the fake channel stored it under.
func condaSubdirOf(oa *fakeOA, it *fakeItem) string {
	st, _ := condaFakes.Load(oa)

	return st.(*condaFake).subdir[it]
}

// condaStored returns the copy of file the channel holds in subdir.
func condaStored(oa *fakeOA, subdir, file string) *fakeItem {
	st, _ := condaFakes.Load(oa)
	_, it := st.(*condaFake).find(oa.repos["chan"], subdir, file)

	return it
}

func condaOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"chan": {format: "conda", repoType: "hosted"}})
}

func condaCfg(t *testing.T, kind string, src *httptest.Server, extra ...string) importConfig {
	t.Helper()

	args := []string{kind, "--source-url", src.URL, "--repo", "src:chan"}
	if kind == "artifactory" {
		args = append(args, "--source-user", "svc")
	}

	cfg := parseCfg(t, append(args, extra...)...)
	if kind == "artifactory" {
		cfg.SourceToken = "secret"
	}

	return cfg
}

func condaArtifactory(t *testing.T, files ...fakeFile) *httptest.Server {
	t.Helper()

	return newFakeArtifactory(t, fakeSourceRepo{name: "src", format: "conda", kind: "local", files: files})
}

// condaChannelFiles is a small channel: payloads in three subdirs (one package
// published, byte for byte, in two of them) beside the index files the channel
// regenerates.
func condaChannelFiles() []fakeFile {
	return []fakeFile{
		{path: "linux-64/numpy-1.26.4-py311h64a7726_0.conda", data: []byte("numpy-conda"), created: artiStamp},
		{path: "linux-64/zlib-1.3-h0_0.conda", data: []byte("zlib"), created: artiStamp},
		{path: "osx-64/zlib-1.3-h0_0.conda", data: []byte("zlib"), created: artiStamp},
		{path: "noarch/my-pkg-2.0-py_0.tar.bz2", data: []byte("my-pkg-bz2"), created: artiStamp},
		{path: "linux-64/repodata.json", data: []byte("{}"), created: artiStamp},
		{path: "linux-64/repodata.json.bz2", data: []byte("bz"), created: artiStamp},
		{path: "linux-64/repodata.json.zst", data: []byte("zst"), created: artiStamp},
		{path: "linux-64/current_repodata.json", data: []byte("{}"), created: artiStamp},
		{path: "linux-64/index.html", data: []byte("<html/>"), created: artiStamp},
		{path: "channeldata.json", data: []byte("{}"), created: artiStamp},
		{path: "index.html", data: []byte("<html/>"), created: artiStamp},
	}
}

func TestImportCondaArtifactory(t *testing.T) {
	files := condaChannelFiles()
	files[0].props = map[string][]string{"build.number": {"12"}}

	arti, oa := condaArtifactory(t, files...), condaOA(t)

	rep := runImportTest(t, condaCfg(t, "artifactory", arti), arti, oa)
	if rep.failed() || rep.Uploaded != 4 || rep.Ignored != 7 {
		t.Fatalf("report = %+v", rep)
	}

	for _, want := range []struct {
		subdir, file, name, version, data string
	}{
		{"linux-64", "numpy-1.26.4-py311h64a7726_0.conda", "numpy", "1.26.4", "numpy-conda"},
		{"linux-64", "zlib-1.3-h0_0.conda", "zlib", "1.3", "zlib"},
		{"osx-64", "zlib-1.3-h0_0.conda", "zlib", "1.3", "zlib"},
		{"noarch", "my-pkg-2.0-py_0.tar.bz2", "my-pkg", "2.0", "my-pkg-bz2"},
	} {
		it := condaStored(oa, want.subdir, want.file)
		if it == nil || it.name != want.name || it.version != want.version || it.sha != sum256([]byte(want.data)) || it.created != artiWant {
			t.Fatalf("%s/%s: item = %+v", want.subdir, want.file, it)
		}

		if got := condaSubdirOf(oa, it); got != want.subdir {
			t.Fatalf("%s stored under %q", want.file, got)
		}
	}

	if it := condaStored(oa, "linux-64", "numpy-1.26.4-py311h64a7726_0.conda"); it.props["build.number"] != "12" {
		t.Fatalf("props = %v", it.props)
	}

	if len(oa.repos["chan"].items) != 4 {
		t.Fatalf("destination holds %d items, want 4", len(oa.repos["chan"].items))
	}
}

func TestImportCondaNexus(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "src", format: "conda", kind: "hosted", files: []fakeFile{
		{path: "linux-64/tool-0.9-0.conda", data: []byte("tool"), created: nexusStamp},
		{path: "linux-64/repodata.json", data: []byte("{}"), created: nexusStamp},
		{path: "channeldata.json", data: []byte("{}"), created: nexusStamp},
	}})
	oa := condaOA(t)

	rep := runImportTest(t, condaCfg(t, "nexus", nexus), nexus, oa)
	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 2 {
		t.Fatalf("report = %+v", rep)
	}

	if it := condaStored(oa, "linux-64", "tool-0.9-0.conda"); it == nil || it.sha != sum256([]byte("tool")) || it.created != nexusWant {
		t.Fatalf("item = %+v", it)
	}
}

func TestImportCondaRerunIsIdempotent(t *testing.T) {
	arti, oa := condaArtifactory(t, condaChannelFiles()...), condaOA(t)
	cfg := condaCfg(t, "artifactory", arti)

	runImportTest(t, cfg, arti, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, arti, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 4 || oa.writes != writes {
		t.Fatalf("report = %+v, writes += %d", rep, oa.writes-writes)
	}
}

func TestImportCondaResumesFromStateFile(t *testing.T) {
	arti, oa := condaArtifactory(t, condaChannelFiles()...), condaOA(t)
	cfg := condaCfg(t, "artifactory", arti, "--state-file", filepath.Join(t.TempDir(), "state.jsonl"))

	if rep := runImportTest(t, cfg, arti, oa); rep.Uploaded != 4 {
		t.Fatalf("first run = %+v", rep)
	}

	oa.repos["chan"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, arti, oa)
	if rep.Resumed != 4 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}
}

func TestImportCondaDryRunWritesNothing(t *testing.T) {
	arti, oa := condaArtifactory(t, condaChannelFiles()...), condaOA(t)

	rep := runImportTest(t, condaCfg(t, "artifactory", arti, "--dry-run"), arti, oa)
	if rep.failed() || rep.Uploaded != 4 || !rep.DryRun || oa.writes != 0 || len(oa.repos["chan"].items) != 0 {
		t.Fatalf("report = %+v writes=%d", rep, oa.writes)
	}
}

// The copy that fails verification is deleted from its own subdir only: the
// same file name published in another subdir stays.
func TestImportCondaChecksumMismatchRemovesTheBadCopy(t *testing.T) {
	arti := condaArtifactory(t,
		fakeFile{path: "linux-64/x-1.0-0.conda", data: []byte("good"), created: artiStamp},
		fakeFile{path: "osx-64/x-1.0-0.conda", data: []byte("tampered"), created: artiStamp, sha256: sum256([]byte("original"))})
	oa := condaOA(t)

	rep := runImportTest(t, condaCfg(t, "artifactory", arti), arti, oa)
	if rep.Uploaded != 1 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep)
	}

	if f := rep.Failures[0]; !strings.Contains(f.Error, "checksum mismatch") || !strings.HasSuffix(f.Path, "osx-64/x-1.0-0.conda") {
		t.Fatalf("failure = %+v", f)
	}

	if condaStored(oa, "osx-64", "x-1.0-0.conda") != nil || condaStored(oa, "linux-64", "x-1.0-0.conda") == nil || oa.deletes != 1 {
		t.Fatalf("bad copy not removed from its own subdir: items=%+v deletes=%d", oa.repos["chan"].items, oa.deletes)
	}
}

func TestImportCondaServerSideChecksumMismatchRemovesTheCopy(t *testing.T) {
	arti := condaArtifactory(t, fakeFile{path: "linux-64/x-1.0-0.conda", data: []byte("x"), created: artiStamp})
	oa := condaOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, condaCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}

	if len(oa.repos["chan"].items) != 0 || oa.deletes != 1 {
		t.Fatalf("corrupt copy left behind: %+v (deletes=%d)", oa.repos["chan"].items, oa.deletes)
	}
}

func TestImportCondaConflictIsReported(t *testing.T) {
	arti := condaArtifactory(t, fakeFile{path: "linux-64/x-1.0-0.conda", data: []byte("new build"), created: artiStamp})
	oa := condaOA(t)

	old := &fakeItem{id: "old", path: "x-1.0-0.conda", name: "x", version: "1.0", sha: sum256([]byte("old build"))}
	oa.repos["chan"].items = []*fakeItem{old}

	st, _ := condaFakes.Load(oa)
	st.(*condaFake).subdir[old] = "linux-64"

	rep := runImportTest(t, condaCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "already exists with different content") {
		t.Fatalf("report = %+v", rep)
	}

	if it := condaStored(oa, "linux-64", "x-1.0-0.conda"); it != old || it.sha != sum256([]byte("old build")) || oa.deletes != 0 {
		t.Fatalf("existing package was touched: %+v (deletes=%d)", it, oa.deletes)
	}
}

func TestImportCondaInvalidPackageNameIsReported(t *testing.T) {
	arti := condaArtifactory(t, fakeFile{path: "linux-64/broken.conda", data: []byte("x"), created: artiStamp})
	oa := condaOA(t)

	rep := runImportTest(t, condaCfg(t, "artifactory", arti), arti, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "invalid conda package file name") || len(oa.repos["chan"].items) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestImportCondaImportable(t *testing.T) {
	for p, want := range map[string]bool{
		"linux-64/numpy-1.26.4-py311_0.conda":  true,
		"noarch/my-pkg-2.0-py_0.tar.bz2":       true,
		"linux-64/repodata.json":               false,
		"linux-64/repodata.json.bz2":           false,
		"linux-64/repodata.json.zst":           false,
		"linux-64/current_repodata.json":       false,
		"linux-64/repodata_from_packages.json": false,
		"linux-64/index.html":                  false,
		"channeldata.json":                     false,
		"index.html":                           false,
		"numpy-1.26.4-py311_0.conda":           false,
		"linux-64/.conda":                      false,
		"linux-64/sub/numpy-1.0-0.conda":       false,
		"/numpy-1.0-0.conda":                   false,
		"linux-64/numpy-1.0-0.tar.gz":          false,
	} {
		if got := importable(formatConda, p); got != want {
			t.Errorf("importable(%q) = %v, want %v", p, got, want)
		}
	}
}

// The listing names the subdir of every package file (its arch), and the same
// file name and bytes may exist in several subdirs.
func TestImportCondaPresentIsPerSubdir(t *testing.T) {
	sha := sum256([]byte("zlib"))
	rows := []listedPackage{
		{ID: "a", SHA256: sha, Filename: "zlib-1.3-h0_0.conda", Arch: "linux-64"},
		{ID: "b", SHA256: sum256([]byte("other")), Filename: "zlib-1.3-h0_0.conda", Arch: "osx-64"},
		{ID: "c", SHA256: sha, Filename: "legacy-1.0-0.conda"},
	}
	ix := &targetIndex{format: formatConda, byPath: map[string]indexEntry{}, all: rows}

	for _, r := range rows {
		ix.byPath[r.Filename] = indexEntry{ID: r.ID, SHA256: r.SHA256}
	}

	cases := map[string]struct {
		asset  sourceAsset
		wantID string
	}{
		"same subdir, same bytes":     {sourceAsset{Path: "linux-64/zlib-1.3-h0_0.conda", SHA256: sha}, "a"},
		"other subdir, same bytes":    {sourceAsset{Path: "osx-64/zlib-1.3-h0_0.conda", SHA256: sha}, ""},
		"same subdir, other bytes":    {sourceAsset{Path: "osx-64/zlib-1.3-h0_0.conda", SHA256: sum256([]byte("x"))}, ""},
		"unknown file name":           {sourceAsset{Path: "linux-64/new-1.0-0.conda", SHA256: sha}, ""},
		"listing without a subdir":    {sourceAsset{Path: "noarch/legacy-1.0-0.conda", SHA256: sha}, "c"},
		"source without a digest":     {sourceAsset{Path: "linux-64/zlib-1.3-h0_0.conda"}, ""},
		"same bytes, other file name": {sourceAsset{Path: "linux-64/zlib-1.3-h1_0.conda", SHA256: sha}, ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, ok := condaPresent(ix, tc.asset)
			if ok != (tc.wantID != "") || e.ID != tc.wantID {
				t.Fatalf("condaPresent = %+v, %v; want id %q", e, ok, tc.wantID)
			}
		})
	}
}
