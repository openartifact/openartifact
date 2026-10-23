// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func cargoFakeState(oa *fakeOA) *cargoFake {
	st, _ := cargoFakes.Load(oa)

	return st.(*cargoFake)
}

// cargoRunLogged runs an import and returns the progress output as well.
func cargoRunLogged(t *testing.T, cfg importConfig, src *httptest.Server, oa *fakeOA) (*importReport, string) {
	t.Helper()

	var out bytes.Buffer

	imp, err := newImporter(cfg, newImportSource(cfg, src.Client()), &importTarget{c: oa.client()}, &out)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(imp.state.close)

	rep := imp.run(context.Background())

	return rep, out.String()
}

// cargoIndexEntry is one line of a sparse index file.
func cargoIndexEntry(name, vers string, crate []byte, yanked bool) string {
	return fmt.Sprintf(`{"name":%q,"vers":%q,"deps":[],"cksum":%q,"features":{},"yanked":%t}`, name, vers, sum256(crate), yanked)
}

func cargoIndexFile(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

func cargoYankedSet(oa *fakeOA) []string {
	var out []string

	for _, it := range oa.repos["crates"].items {
		if cargoFakeState(oa).yanked["crates/"+it.path] {
			out = append(out, it.path)
		}
	}

	return out
}

// demoVersions is a crate with three versions and its sparse index, in which
// 1.0.0 and 1.2.0 are yanked.
func demoVersions(t *testing.T) (map[string][]byte, []byte) {
	t.Helper()

	crates := map[string][]byte{
		"1.0.0": simpleCrate(t, "demo", "1.0.0"),
		"1.1.0": simpleCrate(t, "demo", "1.1.0"),
		"1.2.0": simpleCrate(t, "demo", "1.2.0"),
	}

	index := cargoIndexFile(
		cargoIndexEntry("demo", "1.0.0", crates["1.0.0"], true),
		cargoIndexEntry("demo", "1.1.0", crates["1.1.0"], false),
		cargoIndexEntry("demo", "1.2.0", crates["1.2.0"], true))

	return crates, index
}

func demoFiles(crates map[string][]byte) []fakeFile {
	return []fakeFile{
		{path: "crates/demo/demo-1.0.0.crate", data: crates["1.0.0"], created: artiStamp},
		{path: "crates/demo/demo-1.1.0.crate", data: crates["1.1.0"], created: artiStamp},
		{path: "crates/demo/demo-1.2.0.crate", data: crates["1.2.0"], created: artiStamp},
	}
}

func TestImportCargoYankedFromIndex(t *testing.T) {
	crates, index := demoVersions(t)
	other := simpleCrate(t, "other", "0.1.0")
	files := append(demoFiles(crates),
		fakeFile{path: "index/de/mo/demo", data: index, created: artiStamp},
		fakeFile{path: "crates/other/other-0.1.0.crate", data: other, created: artiStamp},
		fakeFile{path: "index/ot/he/other", data: cargoIndexFile(cargoIndexEntry("other", "0.1.0", other, false)), created: artiStamp})
	arti := cargoArtifactory(t, files...)
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.failed() || rep.Uploaded != 4 {
		t.Fatalf("report = %+v", rep)
	}

	want := []string{"demo-1.0.0.crate", "demo-1.2.0.crate"}
	if got := cargoYankedSet(oa); !reflect.DeepEqual(got, want) {
		t.Fatalf("yanked = %v, want %v", got, want)
	}

	if calls := cargoFakeState(oa).yankCalls; calls != 2 {
		t.Fatalf("yank requests = %d, want 2 (only what the source yanked)", calls)
	}
}

// The index endpoint of Artifactory wins over an index file stored in the
// repository.
func TestImportCargoYankedFromIndexEndpoint(t *testing.T) {
	crates, index := demoVersions(t)
	stale := cargoIndexFile(cargoIndexEntry("demo", "1.0.0", crates["1.0.0"], false))
	arti := newFakeArtifactory(t,
		fakeSourceRepo{name: "src", format: "cargo", kind: "local", files: append(demoFiles(crates),
			fakeFile{path: "index/de/mo/demo", data: stale, created: artiStamp})},
		// The fake serves /<repo>/<path>: the repository "api" stands for /api/...
		fakeSourceRepo{name: "api", format: "generic", kind: "local", files: []fakeFile{
			{path: "cargo/src/index/de/mo/demo", data: index, created: artiStamp}}})
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.failed() || rep.Uploaded != 3 {
		t.Fatalf("report = %+v", rep)
	}

	if got, want := cargoYankedSet(oa), []string{"demo-1.0.0.crate", "demo-1.2.0.crate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("yanked = %v, want %v", got, want)
	}
}

func TestImportCargoYankedFromProperty(t *testing.T) {
	arti := cargoArtifactory(t,
		fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp, props: map[string][]string{"cargo.yanked": {"true"}}},
		fakeFile{path: "crates/a/a-1.1.0.crate", data: simpleCrate(t, "a", "1.1.0"), created: artiStamp, props: map[string][]string{"cargo.yanked": {"false"}}},
		fakeFile{path: "crates/a/a-1.2.0.crate", data: simpleCrate(t, "a", "1.2.0"), created: artiStamp})
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.failed() || rep.Uploaded != 3 {
		t.Fatalf("report = %+v", rep)
	}

	if got, want := cargoYankedSet(oa), []string{"a-1.0.0.crate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("yanked = %v, want %v", got, want)
	}
}

func TestImportCargoYankIsReappliedOnRerun(t *testing.T) {
	crates, index := demoVersions(t)
	arti := cargoArtifactory(t, append(demoFiles(crates), fakeFile{path: "index/de/mo/demo", data: index, created: artiStamp})...)
	oa := cargoOA(t)
	cfg := cargoCfg(t, "artifactory", arti)

	runImportTest(t, cfg, arti, oa)

	rep := runImportTest(t, cfg, arti, oa)
	if rep.failed() || rep.Uploaded != 0 || rep.Present != 3 {
		t.Fatalf("report = %+v", rep)
	}

	if got, want := cargoYankedSet(oa), []string{"demo-1.0.0.crate", "demo-1.2.0.crate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("yanked = %v, want %v", got, want)
	}

	if calls := cargoFakeState(oa).yankCalls; calls != 4 {
		t.Fatalf("yank requests = %d, want 4 (2 per run, the request is idempotent)", calls)
	}
}

func TestImportCargoYankResumesFromStateFile(t *testing.T) {
	crates, index := demoVersions(t)
	arti := cargoArtifactory(t, append(demoFiles(crates), fakeFile{path: "index/de/mo/demo", data: index, created: artiStamp})...)
	oa := cargoOA(t)
	cfg := cargoCfg(t, "artifactory", arti, "--state-file", filepath.Join(t.TempDir(), "state.jsonl"))

	runImportTest(t, cfg, arti, oa)

	writes := oa.writes

	rep := runImportTest(t, cfg, arti, oa)
	if rep.failed() || rep.Resumed != 3 || oa.writes != writes {
		t.Fatalf("resumed=%d writes+=%d report=%+v, want everything skipped", rep.Resumed, oa.writes-writes, rep)
	}

	if calls := cargoFakeState(oa).yankCalls; calls != 2 {
		t.Fatalf("yank requests = %d, want 2 (the second run must not repeat them)", calls)
	}
}

// A crate stored by an earlier run is yanked once the source reports it yanked,
// and a source that no longer says so never unyanks it.
func TestImportCargoYankRepairsPresentCrateAndNeverUnyanks(t *testing.T) {
	crates, index := demoVersions(t)
	files := demoFiles(crates)
	plain := cargoArtifactory(t, files...)
	yanked := cargoArtifactory(t, append(files, fakeFile{path: "index/de/mo/demo", data: index, created: artiStamp})...)
	oa := cargoOA(t)

	runImportTest(t, cargoCfg(t, "artifactory", plain), plain, oa)

	if got := cargoYankedSet(oa); len(got) != 0 {
		t.Fatalf("yanked before the source says so: %v", got)
	}

	rep := runImportTest(t, cargoCfg(t, "artifactory", yanked), yanked, oa)
	if rep.failed() || rep.Present != 3 || rep.Uploaded != 0 {
		t.Fatalf("report = %+v", rep)
	}

	want := []string{"demo-1.0.0.crate", "demo-1.2.0.crate"}
	if got := cargoYankedSet(oa); !reflect.DeepEqual(got, want) {
		t.Fatalf("yanked = %v, want %v", got, want)
	}

	calls := cargoFakeState(oa).yankCalls

	rep = runImportTest(t, cargoCfg(t, "artifactory", plain), plain, oa)
	if rep.failed() || cargoFakeState(oa).yankCalls != calls {
		t.Fatalf("report = %+v, yank requests %d -> %d", rep, calls, cargoFakeState(oa).yankCalls)
	}

	if got := cargoYankedSet(oa); !reflect.DeepEqual(got, want) {
		t.Fatalf("yanked after a source without yanks = %v, want %v", got, want)
	}
}

func TestImportCargoDryRunDoesNotYank(t *testing.T) {
	crates, index := demoVersions(t)
	arti := cargoArtifactory(t, append(demoFiles(crates), fakeFile{path: "index/de/mo/demo", data: index, created: artiStamp})...)
	oa := cargoOA(t)

	rep, out := cargoRunLogged(t, cargoCfg(t, "artifactory", arti, "--dry-run"), arti, oa)
	if rep.failed() || oa.writes != 0 || cargoFakeState(oa).yankCalls != 0 {
		t.Fatalf("report = %+v writes=%d yanks=%d", rep, oa.writes, cargoFakeState(oa).yankCalls)
	}

	for _, want := range []string{"would yank src/crates/demo/demo-1.0.0.crate", "would yank src/crates/demo/demo-1.2.0.crate"} {
		if !strings.Contains(out, want) {
			t.Fatalf("progress lacks %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "would yank src/crates/demo/demo-1.1.0.crate") {
		t.Fatalf("a live version is reported as yanked:\n%s", out)
	}
}

func TestImportCargoYankRefusedIsReported(t *testing.T) {
	arti := cargoArtifactory(t,
		fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp, props: map[string][]string{"cargo.yanked": {"true"}}})
	oa := cargoOA(t)
	cargoFakeState(oa).yankStatus = 409

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.Uploaded != 1 || rep.Failed != 1 || len(rep.Failures) != 1 {
		t.Fatalf("report = %+v", rep)
	}

	f := rep.Failures[0]
	if f.Path != "crates/a/a-1.0.0.crate" || !strings.Contains(f.Error, "yanking") || !strings.Contains(f.Error, "409") {
		t.Fatalf("failure = %+v", f)
	}
}

func TestImportCargoUnreadableIndexIsReported(t *testing.T) {
	crate := simpleCrate(t, "demo", "1.0.0")
	arti := cargoArtifactory(t,
		fakeFile{path: "crates/demo/demo-1.0.0.crate", data: crate, created: artiStamp},
		fakeFile{path: "index/de/mo/demo", data: []byte("<html>not an index</html>"), created: artiStamp})
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.Uploaded != 1 || rep.Failed != 1 || cargoFakeState(oa).yankCalls != 0 {
		t.Fatalf("report = %+v", rep)
	}

	f := rep.Failures[0]
	if f.Path != "index/de/mo/demo" || !strings.Contains(f.Error, "yanked state") {
		t.Fatalf("failure = %+v", f)
	}
}

// A crate that failed to import is not yanked: it is not on the destination.
func TestImportCargoFailedImportIsNotYanked(t *testing.T) {
	arti := cargoArtifactory(t,
		fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: artiStamp, sha256: strings.Repeat("0", 64),
			props: map[string][]string{"cargo.yanked": {"true"}}})
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "artifactory", arti), arti, oa)
	if rep.Uploaded != 0 || rep.Failed != 1 || cargoFakeState(oa).yankCalls != 0 {
		t.Fatalf("report = %+v, yank requests = %d", rep, cargoFakeState(oa).yankCalls)
	}
}

// Nexus cannot host Cargo repositories, so it has nothing to yank from.
func TestImportCargoNexusHasNoYankedState(t *testing.T) {
	nexus := cargoNexus(t, fakeFile{path: "crates/a/a-1.0.0.crate", data: simpleCrate(t, "a", "1.0.0"), created: nexusStamp})
	oa := cargoOA(t)

	rep := runImportTest(t, cargoCfg(t, "nexus", nexus), nexus, oa)
	if rep.failed() || rep.Uploaded != 1 || cargoFakeState(oa).yankCalls != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestImportCargoCrateName(t *testing.T) {
	for p, want := range map[string][2]string{
		"crates/demo/demo-1.2.0.crate":                  {"demo", "1.2.0"},
		"crates/serde-json/serde-json-1.0.0-rc.1.crate": {"serde-json", "1.0.0-rc.1"},
		"pool/foo-bar-2.0.0.crate":                      {"foo-bar", "2.0.0"},
		"crates/demo/other-1.0.0.crate":                 {"other", "1.0.0"},
		"x-1.crate":                                     {"x", "1"},
		"weird.crate":                                   {"weird", ""},
	} {
		name, vers := cargoPathCoordinates(p)
		if name != want[0] || vers != want[1] || cargoCrateName(p) != want[0] {
			t.Errorf("%s -> (%q, %q), want %v", p, name, vers, want)
		}
	}
}

func TestImportCargoIndexPath(t *testing.T) {
	for name, want := range map[string]string{
		"a": "1/a", "ab": "2/ab", "abc": "3/a/abc", "Serde": "se/rd/serde", "tokio-util": "to/ki/tokio-util", "": "",
	} {
		if got := cargoIndexPath(name); got != want {
			t.Errorf("cargoIndexPath(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestImportCargoIndexURLs(t *testing.T) {
	a := sourceAsset{Repo: "cargo-local", Path: "crates/demo/demo-1.0.0.crate", URL: "http://arti.example:8081/artifactory/cargo-local/crates/demo/demo-1.0.0.crate"}

	want := []string{
		"http://arti.example:8081/artifactory/api/cargo/cargo-local/index/de/mo/demo",
		"http://arti.example:8081/artifactory/cargo-local/index/de/mo/demo",
	}
	if got := cargoIndexURLs(a, "de/mo/demo"); !reflect.DeepEqual(got, want) {
		t.Fatalf("urls = %v, want %v", got, want)
	}

	a.URL = "http://elsewhere.example/blob/1"
	if got := cargoIndexURLs(a, "de/mo/demo"); got != nil {
		t.Fatalf("urls for an unrelated asset URL = %v, want none", got)
	}
}

func TestImportCargoYankState(t *testing.T) {
	crate := simpleCrate(t, "Demo", "1.0.0")
	lines := []cargoIndexLine{
		{Name: "Demo", Vers: "1.0.0", Cksum: sum256(crate), Yanked: true},
		{Name: "Demo", Vers: "1.1.0", Cksum: "ff", Yanked: false},
	}

	for _, tc := range []struct {
		desc  string
		asset sourceAsset
		lines []cargoIndexLine
		want  [3]any
	}{
		{"matched by file name, name and case from the index", sourceAsset{Path: "crates/demo/demo-1.0.0.crate"}, lines, [3]any{"Demo", "1.0.0", true}},
		{"matched by checksum", sourceAsset{Path: "pool/renamed.crate", SHA256: sum256(crate)}, lines, [3]any{"Demo", "1.0.0", true}},
		{"live version", sourceAsset{Path: "crates/demo/demo-1.1.0.crate"}, lines, [3]any{"Demo", "1.1.0", false}},
		{"no index: coordinates from the path", sourceAsset{Path: "crates/demo/demo-2.0.0.crate"}, nil, [3]any{"demo", "2.0.0", false}},
		{"property says yanked", sourceAsset{Path: "crates/demo/demo-2.0.0.crate", Props: map[string][]string{"Cargo.Yanked": {"TRUE"}}}, nil, [3]any{"demo", "2.0.0", true}},
		{"property overrides a live index line", sourceAsset{Path: "crates/demo/demo-1.1.0.crate", Props: map[string][]string{"cargo.yank": {"true"}}}, lines, [3]any{"Demo", "1.1.0", true}},
	} {
		name, vers, yanked := cargoYankState(tc.asset, tc.lines)
		if got := [3]any{name, vers, yanked}; got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.desc, got, tc.want)
		}
	}
}

func TestImportCargoUnitsGroupVersionsOfACrate(t *testing.T) {
	assets := []sourceAsset{
		{Path: "crates/b/b-1.0.0.crate"}, {Path: "crates/a/a-1.0.0.crate"}, {Path: "crates/b/b-2.0.0.crate"},
		{Path: "crates/a/a-0.1.0.crate"}, {Path: "crates/c/c-1.0.0.crate"},
	}

	var got [][]string

	for _, u := range cargoUnits(assets) {
		var paths []string
		for _, a := range u {
			paths = append(paths, a.Path)
		}

		got = append(got, paths)
	}

	want := [][]string{
		{"crates/b/b-1.0.0.crate", "crates/b/b-2.0.0.crate"},
		{"crates/a/a-1.0.0.crate", "crates/a/a-0.1.0.crate"},
		{"crates/c/c-1.0.0.crate"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
}
