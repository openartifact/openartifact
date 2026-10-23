// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	nexusStamp = "2024-03-01T10:00:00.000+00:00"
	nexusWant  = "2024-03-01T10:00:00Z"
	artiStamp  = "2023-07-15T08:30:00.000Z"
	artiWant   = "2023-07-15T08:30:00Z"
)

func parseCfg(t *testing.T, args ...string) importConfig {
	t.Helper()

	cfg, err := parseImportFlags(args, func(string) string { return "" }, strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseImportFlags(%v): %v", args, err)
	}

	return cfg
}

func runImportTest(t *testing.T, cfg importConfig, src *httptest.Server, oa *fakeOA) *importReport {
	t.Helper()

	imp, err := newImporter(cfg, newImportSource(cfg, src.Client()), &importTarget{c: oa.client()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(imp.state.close)

	return imp.run(context.Background())
}

func mavenRepoFiles() []fakeFile {
	const base = "com/acme/lib/1.0/"

	return []fakeFile{
		{path: base + "lib-1.0.jar", data: []byte("jar-bytes"), created: nexusStamp},
		{path: base + "lib-1.0.jar.sha1", data: []byte("x"), created: nexusStamp},
		{path: base + "lib-1.0.pom", data: []byte("<project/>"), created: nexusStamp},
		{path: "com/acme/lib/maven-metadata.xml", data: []byte("<metadata/>"), created: nexusStamp},
		{path: base + "lib-1.0.pom.md5", data: []byte("y"), created: nexusStamp},
	}
}

func TestImportNexusMaven(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: mavenRepoFiles()})
	oa := newFakeOA(t, map[string]*fakeRepo{"mvn": {format: "maven", repoType: "hosted"}})
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn", "--concurrency", "3")

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() {
		t.Fatalf("unexpected failures: %+v", rep.Failures)
	}

	if rep.Uploaded != 2 || rep.Ignored != 3 {
		t.Fatalf("uploaded=%d ignored=%d, want 2 and 3 (sidecars and maven-metadata skipped)", rep.Uploaded, rep.Ignored)
	}

	jar := oa.find("mvn", "com/acme/lib/1.0/lib-1.0.jar")
	if jar == nil {
		t.Fatal("jar not uploaded")
	}

	if jar.sha != sum256([]byte("jar-bytes")) {
		t.Fatalf("stored sha %s does not match source", jar.sha)
	}

	if jar.created != nexusWant {
		t.Fatalf("created-at header = %q, want %q", jar.created, nexusWant)
	}

	if got := len(oa.repos["mvn"].items); got != 2 {
		t.Fatalf("destination holds %d items, want 2", got)
	}

	if rep.Bytes != int64(len("jar-bytes")+len("<project/>")) {
		t.Fatalf("bytes = %d", rep.Bytes)
	}
}

func TestImportIsIdempotentWithoutState(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: mavenRepoFiles()})
	oa := newFakeOA(t, map[string]*fakeRepo{"mvn": {format: "maven", repoType: "hosted"}})
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn")

	runImportTest(t, cfg, nexus, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Uploaded != 0 || rep.Present != 2 {
		t.Fatalf("second run uploaded=%d present=%d, want 0 and 2", rep.Uploaded, rep.Present)
	}

	if oa.writes != writes {
		t.Fatalf("second run wrote %d times", oa.writes-writes)
	}
}

func TestImportResumesFromStateFile(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: mavenRepoFiles()})
	oa := newFakeOA(t, map[string]*fakeRepo{"mvn": {format: "maven", repoType: "hosted"}})
	state := filepath.Join(t.TempDir(), "state.jsonl")
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn", "--state-file", state)

	if rep := runImportTest(t, cfg, nexus, oa); rep.Uploaded != 2 {
		t.Fatalf("first run uploaded %d", rep.Uploaded)
	}

	// Wipe the destination: only the state file knows the assets were done.
	oa.repos["mvn"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, nexus, oa)
	if rep.Resumed != 2 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}

	raw, err := os.ReadFile(state)
	if err != nil || strings.Count(string(raw), "\n") != 2 {
		t.Fatalf("state file = %q, %v", raw, err)
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: mavenRepoFiles()})
	oa := newFakeOA(t, map[string]*fakeRepo{})
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn", "--create-repos", "--dry-run")

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() {
		t.Fatalf("dry run failed: %+v", rep.Failures)
	}

	if oa.writes != 0 || len(oa.repos) != 0 {
		t.Fatalf("dry run wrote: writes=%d repos=%d", oa.writes, len(oa.repos))
	}

	if rep.Uploaded != 2 || !rep.DryRun {
		t.Fatalf("plan uploaded=%d dry=%v", rep.Uploaded, rep.DryRun)
	}
}

func TestImportChecksumMismatchIsFailure(t *testing.T) {
	files := []fakeFile{
		{path: "com/acme/lib/1.0/ok-1.0.jar", data: []byte("good"), created: nexusStamp},
		{path: "com/acme/lib/1.0/bad-1.0.jar", data: []byte("tampered"), created: nexusStamp, sha256: sum256([]byte("original"))},
	}
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: files})
	oa := newFakeOA(t, map[string]*fakeRepo{"mvn": {format: "maven", repoType: "hosted"}})
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn")

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Uploaded != 1 || rep.Failed != 1 || !rep.failed() {
		t.Fatalf("uploaded=%d failed=%d", rep.Uploaded, rep.Failed)
	}

	if f := rep.Failures[0]; !strings.Contains(f.Error, "checksum mismatch") || !strings.HasSuffix(f.Path, "bad-1.0.jar") {
		t.Fatalf("failure = %+v", f)
	}

	if oa.find("mvn", "com/acme/lib/1.0/bad-1.0.jar") != nil || oa.deletes != 1 {
		t.Fatalf("bad copy was not removed (deletes=%d)", oa.deletes)
	}
}

func TestImportServerSideChecksumMismatchIsFailure(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "files", format: "raw", kind: "hosted",
		files: []fakeFile{{path: "tools/a.bin", data: []byte("abc"), created: nexusStamp}}})
	oa := newFakeOA(t, map[string]*fakeRepo{"raw": {format: "raw", repoType: "hosted"}})
	oa.lieSHA = strings.Repeat("0", 64)
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "files:raw")

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}

	if len(oa.repos["raw"].items) != 0 {
		t.Fatal("corrupt copy left behind")
	}
}

func TestImportNexusDebDerivesDistribution(t *testing.T) {
	deb := []byte("!<arch>deb")
	files := []fakeFile{
		{path: "dists/jammy/main/binary-amd64/Packages", data: []byte("idx"), created: nexusStamp},
		{path: "dists/jammy/Release", data: []byte("rel"), created: nexusStamp},
		{path: "pool/p/pkg/pkg_1.0_amd64.deb", data: deb, created: nexusStamp},
		{path: "pool/p/pkg/readme.txt", data: []byte("t"), created: nexusStamp},
	}
	nexus := newFakeNexus(t, fakeSourceRepo{name: "ubuntu", format: "apt", kind: "hosted", files: files})
	oa := newFakeOA(t, map[string]*fakeRepo{"apt": {format: "deb", repoType: "hosted"}})
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "ubuntu:apt")

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 3 {
		t.Fatalf("report = %+v", rep)
	}

	it := oa.find("apt", "pkg_1.0_amd64.deb")
	if it == nil || it.dist != "jammy" || it.comp != "main" || it.created != nexusWant {
		t.Fatalf("item = %+v", it)
	}

	if again := runImportTest(t, cfg, nexus, oa); again.Present != 1 || again.Uploaded != 0 {
		t.Fatalf("rerun present=%d uploaded=%d", again.Present, again.Uploaded)
	}
}

func TestImportDebNeedsPlacement(t *testing.T) {
	files := []fakeFile{{path: "pool/p/pkg/pkg_1.0_amd64.deb", data: []byte("d"), created: nexusStamp}}
	nexus := newFakeNexus(t, fakeSourceRepo{name: "flat", format: "apt", kind: "hosted", files: files})
	oa := newFakeOA(t, map[string]*fakeRepo{"apt": {format: "deb", repoType: "hosted"}})

	rep := runImportTest(t, parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "flat:apt"), nexus, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "--deb-distribution") {
		t.Fatalf("report = %+v", rep)
	}

	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "flat:apt", "--deb-distribution", "stable", "--deb-component", "main")
	if rep := runImportTest(t, cfg, nexus, oa); rep.failed() || oa.find("apt", "pkg_1.0_amd64.deb").dist != "stable" {
		t.Fatalf("fallback flags not honored: %+v", rep)
	}
}

func artifactoryRepos() []fakeSourceRepo {
	return []fakeSourceRepo{
		{name: "rpm-local", format: "rpm", kind: "local", files: []fakeFile{
			{path: "x86_64/foo-1.0-1.x86_64.rpm", data: []byte("rpm-data"), created: artiStamp, props: map[string][]string{
				"rpm.name":     {"foo"},
				"build.number": {"42"},
				"vcs.url":      {"https://git.example/foo"},
				"team":         {"secret-team"},
				"vcs.tags":     {"a", "b"},
			}},
			{path: "repodata/repomd.xml", data: []byte("<repomd/>"), created: artiStamp},
		}},
		{name: "rpm-remote", format: "rpm", kind: "remote"},
	}
}

func artifactoryCfg(t *testing.T, srv *httptest.Server, extra ...string) importConfig {
	t.Helper()

	return parseCfg(t, append([]string{"artifactory", "--source-url", srv.URL, "--source-user", "svc", "--repo", "rpm-local:rpms"}, extra...)...)
}

func TestImportArtifactoryAQLCopiesProperties(t *testing.T) {
	// Source credentials are set on the config, as the env/file paths would.
	arti := newFakeArtifactory(t, artifactoryRepos()...)
	oa := newFakeOA(t, map[string]*fakeRepo{"rpms": {format: "rpm", repoType: "hosted"}})

	cfg := artifactoryCfg(t, arti)
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, arti, oa)
	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 1 {
		t.Fatalf("report = %+v", rep)
	}

	it := oa.find("rpms", "foo-1.0-1.x86_64.rpm")
	if it == nil || it.created != artiWant {
		t.Fatalf("item = %+v", it)
	}

	want := map[string]string{"rpm.name": "foo", "build.number": "42", "vcs.url": "https://git.example/foo", "vcs.tags": "a,b"}
	if len(it.props) != len(want) {
		t.Fatalf("props = %v, want %v (non-allow-listed 'team' must not be copied)", it.props, want)
	}

	for k, v := range want {
		if it.props[k] != v {
			t.Fatalf("prop %s = %q, want %q", k, it.props[k], v)
		}
	}
}

// TestImportArtifactoryOSSRefusesAQLSorting covers Artifactory OSS, which
// answers 400 to any AQL query with a sort clause: the listing must repeat the
// page without it, and the later pages must stay unsorted.
func TestImportArtifactoryOSSRefusesAQLSorting(t *testing.T) {
	arti := newFakeArtifactory(t, artifactoryRepos()...)
	target, err := url.Parse(arti.URL)
	if err != nil {
		t.Fatal(err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	var sortedQueries, unsortedQueries int

	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search/aql" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))

			if strings.Contains(string(body), ".sort(") {
				sortedQueries++

				http.Error(w, `{"errors":[{"status":400,"message":"Sorting is not supported by AQL in the open source version\n"}]}`, http.StatusBadRequest)

				return
			}

			unsortedQueries++
		}

		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(oss.Close)

	oa := newFakeOA(t, map[string]*fakeRepo{"rpms": {format: "rpm", repoType: "hosted"}})

	cfg := artifactoryCfg(t, oss)
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, oss, oa)
	if rep.failed() || rep.Uploaded != 1 {
		t.Fatalf("report = %+v", rep)
	}

	if sortedQueries != 1 || unsortedQueries != 1 {
		t.Fatalf("sorted queries = %d, unsorted = %d; want one of each", sortedQueries, unsortedQueries)
	}

	if oa.find("rpms", "foo-1.0-1.x86_64.rpm") == nil {
		t.Fatal("the package was not imported")
	}
}

func TestImportArtifactoryStorageListing(t *testing.T) {
	arti := newFakeArtifactory(t, artifactoryRepos()...)
	oa := newFakeOA(t, map[string]*fakeRepo{"rpms": {format: "rpm", repoType: "hosted"}})

	cfg := artifactoryCfg(t, arti, "--artifactory-listing", "storage")
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, arti, oa)
	if rep.failed() || rep.Uploaded != 1 {
		t.Fatalf("report = %+v", rep)
	}

	it := oa.find("rpms", "foo-1.0-1.x86_64.rpm")
	if it == nil || it.created != artiWant || it.props["rpm.name"] != "foo" || it.props["vcs.tags"] != "a,b" {
		t.Fatalf("item = %+v", it)
	}
}

func TestImportRefusesNonHostedSourceAndWrongTarget(t *testing.T) {
	arti := newFakeArtifactory(t, artifactoryRepos()...)
	oa := newFakeOA(t, map[string]*fakeRepo{"rpms": {format: "maven", repoType: "hosted"}, "prox": {format: "rpm", repoType: "proxy"}})

	cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc",
		"--repo", "rpm-remote:rpms", "--repo", "rpm-local:rpms", "--repo", "rpm-local:prox")
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, arti, oa)
	if rep.Failed != 3 || oa.writes != 0 {
		t.Fatalf("failed=%d writes=%d: %+v", rep.Failed, oa.writes, rep.Failures)
	}

	for i, frag := range []string{"hosted", "different format", "not a hosted"} {
		if !strings.Contains(rep.Failures[i].Error, frag) {
			t.Fatalf("failure %d = %q, want %q", i, rep.Failures[i].Error, frag)
		}
	}
}

func TestImportCreateRepos(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: mavenRepoFiles()})
	oa := newFakeOA(t, map[string]*fakeRepo{})

	missing := runImportTest(t, parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn"), nexus, oa)
	if missing.Failed != 1 || oa.writes != 0 {
		t.Fatalf("without --create-repos: %+v", missing)
	}

	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn", "--create-repos")
	if rep := runImportTest(t, cfg, nexus, oa); rep.failed() || rep.Uploaded != 2 {
		t.Fatalf("with --create-repos: %+v", rep)
	}

	if r := oa.repos["mvn"]; r == nil || r.format != "maven" || r.repoType != "hosted" {
		t.Fatalf("created repo = %+v", r)
	}
}

func TestImportRawKeepsExistingContentOnFailure(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "files", format: "raw", kind: "hosted",
		files: []fakeFile{{path: "a.bin", data: []byte("new"), created: nexusStamp, sha256: sum256([]byte("other"))}}})
	oa := newFakeOA(t, map[string]*fakeRepo{"raw": {format: "raw", repoType: "hosted"}})
	oa.store("raw", "a.bin", []byte("precious"), "")

	rep := runImportTest(t, parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "files:raw"), nexus, oa)

	if rep.Failed != 1 || oa.deletes != 0 || !strings.Contains(rep.Failures[0].Error, "left in place") {
		t.Fatalf("report = %+v deletes=%d", rep, oa.deletes)
	}
}

func TestParseImportFlags(t *testing.T) {
	env := func(k string) string {
		return map[string]string{envSourceToken: " tok-from-env \n", envSourceUser: "envuser"}[k]
	}
	tokenFile := filepath.Join(t.TempDir(), "tok")

	if err := os.WriteFile(tokenFile, []byte("tok-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		args      []string
		stdin     string
		wantToken string
		wantErr   string
	}{
		{"env token", []string{"nexus", "--source-url", "https://n.example", "--repo", "a"}, "", "tok-from-env", ""},
		{"file token", []string{"nexus", "--source-url", "https://n.example", "--repo", "a", "--source-token-file", tokenFile}, "", "tok-from-file", ""},
		{"stdin token", []string{"nexus", "--source-url", "https://n.example", "--repo", "a", "--source-token-file", "-"}, "tok-stdin\nmore", "tok-stdin", ""},
		{"no token flag", []string{"nexus", "--source-url", "https://n.example", "--repo", "a", "--source-token", "x"}, "", "", "flag provided but not defined"},
		{"userinfo url", []string{"nexus", "--source-url", "https://u:p@n.example", "--repo", "a"}, "", "", "must not embed credentials"},
		{"plain http credentials", []string{"nexus", "--source-url", "http://n.example", "--repo", "a"}, "", "", "plain HTTP"},
		{"unknown type", []string{"harbor", "--source-url", "https://n.example", "--repo", "a"}, "", "", "unknown source type"},
		{"no repo", []string{"nexus", "--source-url", "https://n.example"}, "", "", "--repo is required"},
		{"bad concurrency", []string{"nexus", "--source-url", "https://n.example", "--repo", "a", "--concurrency", "0"}, "", "", "--concurrency"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseImportFlags(tc.args, env, strings.NewReader(tc.stdin))

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}

				return
			}

			if err != nil || cfg.SourceToken != tc.wantToken {
				t.Fatalf("token = %q, err = %v, want %q", cfg.SourceToken, err, tc.wantToken)
			}
		})
	}
}

func TestParseRepoMappings(t *testing.T) {
	cfg := parseCfg(t, "nexus", "--source-url", "https://n.example", "--repo", "a:b", "--repo", "solo")

	want := []repoMapping{{"a", "b"}, {"solo", "solo"}}
	if len(cfg.Repos) != 2 || cfg.Repos[0] != want[0] || cfg.Repos[1] != want[1] {
		t.Fatalf("repos = %+v", cfg.Repos)
	}
}

func TestImportableFilters(t *testing.T) {
	cases := []struct {
		f    importFormat
		path string
		want bool
	}{
		{formatMaven, "a/b/1/b-1.jar", true},
		{formatMaven, "a/b/1/b-1.jar.sha512", false},
		{formatMaven, "a/b/maven-metadata.xml", false},
		{formatMaven, "a/b/1/b-1.pom.asc", true},
		{formatMaven, "b-1.jar", false},
		{formatMaven, ".index/x/y/z/q", false},
		{formatDeb, "pool/x/y_1_all.deb", true},
		{formatDeb, "dists/jammy/Release", false},
		{formatRPM, "x86_64/a.rpm", true},
		{formatRPM, "x86_64/repodata/repomd.xml", false},
		{formatRaw, "any/thing.bin", true},
		{formatRaw, ".hidden/x", false},
	}

	for _, tc := range cases {
		if got := importable(tc.f, tc.path); got != tc.want {
			t.Errorf("importable(%s, %q) = %v, want %v", tc.f, tc.path, got, tc.want)
		}
	}
}

func TestImportPathPrefixLimitsAssets(t *testing.T) {
	files := append(mavenRepoFiles(), fakeFile{path: "org/other/lib/2.0/lib-2.0.jar", data: []byte("other"), created: nexusStamp})
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: files})
	oa := newFakeOA(t, map[string]*fakeRepo{"mvn": {format: "maven", repoType: "hosted"}})
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn", "--path-prefix", "org/other/")

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 1 || oa.find("mvn", "org/other/lib/2.0/lib-2.0.jar") == nil || len(oa.repos["mvn"].items) != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// noSHA256Source wraps a source and hides the SHA-256 (and SHA-1) from its
// listing, like old Nexus/Artifactory assets.
type noSHA256Source struct{ importSource }

func (s noSHA256Source) Assets(ctx context.Context, repo sourceRepo) ([]sourceAsset, error) {
	assets, err := s.importSource.Assets(ctx, repo)
	for i := range assets {
		assets[i].SHA256, assets[i].SHA1 = "", ""
	}

	return assets, err
}

func TestImportResumesWithoutSourceSHA256(t *testing.T) {
	nexus := newFakeNexus(t, fakeSourceRepo{name: "rel", format: "maven2", kind: "hosted", files: mavenRepoFiles()})
	oa := newFakeOA(t, map[string]*fakeRepo{"mvn": {format: "maven", repoType: "hosted"}})
	state := filepath.Join(t.TempDir(), "state.jsonl")
	cfg := parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "rel:mvn", "--state-file", state)

	run := func() *importReport {
		imp, err := newImporter(cfg, noSHA256Source{newImportSource(cfg, nexus.Client())}, &importTarget{c: oa.client()}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}

		defer imp.state.close()

		return imp.run(context.Background())
	}

	if rep := run(); rep.Uploaded != 2 {
		t.Fatalf("first run uploaded %d (failures %+v)", rep.Uploaded, rep.Failures)
	}

	oa.repos["mvn"].items = nil
	writes := oa.writes

	rep := run()
	if rep.Resumed != 2 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}
}
