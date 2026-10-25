// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRemotePath(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		opts    ciUploadOptions
		single  bool
		want    string
		wantErr bool
	}{
		{name: "keeps the relative path", file: "dist/app.tgz", want: "dist/app.tgz", single: true},
		{name: "cleans ./", file: "./dist/app.tgz", want: "dist/app.tgz"},
		{name: "flat", file: "dist/app.tgz", opts: ciUploadOptions{flat: true}, want: "app.tgz"},
		{name: "directory target", file: "dist/app.tgz", opts: ciUploadOptions{targetPath: "rel/1.0/"}, want: "rel/1.0/dist/app.tgz", single: true},
		{name: "directory target flat", file: "dist/app.tgz", opts: ciUploadOptions{targetPath: "rel/1.0/", flat: true}, want: "rel/1.0/app.tgz"},
		{name: "exact target for one file", file: "dist/app.tgz", opts: ciUploadOptions{targetPath: "rel/app-1.0.tgz"}, single: true, want: "rel/app-1.0.tgz"},
		{name: "target without slash is a directory for many", file: "a.txt", opts: ciUploadOptions{targetPath: "rel", flat: true}, want: "rel/a.txt"},
		{name: "leading slash dropped", file: "a.txt", opts: ciUploadOptions{targetPath: "/rel/"}, want: "rel/a.txt"},
		{name: "absolute needs flat", file: "/tmp/a.txt", wantErr: true},
		{name: "absolute flat", file: "/tmp/a.txt", opts: ciUploadOptions{flat: true}, want: "a.txt"},
		{name: "outside the tree", file: "../a.txt", wantErr: true},
		{name: "dotdot in target", file: "a.txt", opts: ciUploadOptions{targetPath: "x/../y/"}, wantErr: true},
		{name: "double slash in target", file: "a.txt", opts: ciUploadOptions{targetPath: "x//y/"}, wantErr: true},
		{name: "backslash in target", file: "a.txt", opts: ciUploadOptions{targetPath: "x\\y/"}, wantErr: true},
		{name: "dotdot-prefixed name is fine", file: "..a.txt", want: "..a.txt"},
		{name: "dotdot only", file: "..", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remotePath(tc.file, tc.opts, tc.single)
			if tc.wantErr {
				if !errors.Is(err, errUploadPath) {
					t.Fatalf("err = %v, want errUploadPath", err)
				}

				return
			}

			if err != nil || got != tc.want {
				t.Fatalf("remotePath = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestExpandUploadFiles(t *testing.T) {
	dir := t.TempDir()

	for _, f := range []string{"a.jar", "b.jar", "b.pom", "sub/c.jar", "sub/deep/d.jar", "sub/deep/e.txt"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rel := func(files []string) []string {
		out := make([]string, len(files))
		for i, f := range files {
			out[i], _ = filepath.Rel(dir, f)
		}

		return out
	}

	tests := []struct {
		patterns []string
		want     []string
	}{
		{[]string{dir + "/*.jar"}, []string{"a.jar", "b.jar"}},
		{[]string{dir + "/**/*.jar"}, []string{"a.jar", "b.jar", "sub/c.jar", "sub/deep/d.jar"}},
		{[]string{dir + "/sub/**"}, []string{"sub/c.jar", "sub/deep/d.jar", "sub/deep/e.txt"}},
		{[]string{dir + "/?.pom", dir + "/a.jar", dir + "/*.jar"}, []string{"a.jar", "b.jar", "b.pom"}},
		{[]string{dir + "/[ab].jar"}, []string{"a.jar", "b.jar"}},
		{[]string{dir + "/[!a].jar"}, []string{"b.jar"}},
		{[]string{dir + "/b.pom"}, []string{"b.pom"}},
	}

	for _, tc := range tests {
		got, err := expandUploadFiles(tc.patterns)
		if err != nil {
			t.Fatalf("%v: %v", tc.patterns, err)
		}

		if r := rel(got); !reflect.DeepEqual(r, tc.want) {
			t.Errorf("%v = %v, want %v", tc.patterns, r, tc.want)
		}
	}

	if _, err := expandUploadFiles([]string{dir + "/*.zip"}); !errors.Is(err, errUploadNotFound) {
		t.Errorf("no match: err = %v", err)
	}

	if _, err := expandUploadFiles([]string{dir + "/sub"}); err == nil {
		t.Error("a directory without glob must be refused")
	}

	if _, err := expandUploadFiles([]string{dir + "/missing.jar"}); err == nil {
		t.Error("a missing file must be refused")
	}
}

func TestParseCIUploadArgs(t *testing.T) {
	o, err := parseCIUploadArgs([]string{
		"repo", "a.jar", "--flat", "--property", "k=v", "b/*.jar", "--property=x=y=z", "--build-name", "job", "--build-number", "7",
		"--build-url", "http://ci/7", "--vcs-revision", "abc", "--target-path", "t/",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	if o.repo != "repo" || !slices.Equal(o.patterns, []string{"a.jar", "b/*.jar"}) || !o.flat || o.targetPath != "t/" {
		t.Fatalf("options = %+v", o)
	}

	want := map[string]string{
		"build.name": "job", "build.number": "7", "build.url": "http://ci/7", "vcs.revision": "abc", "k": "v", "x": "y=z",
	}
	if got := o.props(); !reflect.DeepEqual(got, want) {
		t.Errorf("props = %v, want %v", got, want)
	}

	o, err = parseCIUploadArgs([]string{"repo", "f", "--property", "build.name=custom", "--build-name", "job"}, io.Discard)
	if err != nil || o.props()["build.name"] != "custom" {
		t.Errorf("a custom property wins over the build flag: %v %v", o.props(), err)
	}

	for _, bad := range [][]string{{}, {"repo"}, {"repo", "f", "--property", "novalue"}, {"repo", "f", "--property", "=v"}} {
		if _, err := parseCIUploadArgs(bad, io.Discard); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}

func TestParseBundleAddArgs(t *testing.T) {
	o, err := parseBundleAddArgs([]string{"app", "1.0", "--repo", "r", "--path", "a/b", "--path", "c"})
	if err != nil || o.name != "app" || o.version != "1.0" || len(o.paths) != 2 || o.repos[0] != "r" {
		t.Fatalf("path mode: %+v %v", o, err)
	}

	o, err = parseBundleAddArgs([]string{"--from-build", "my/job/12", "app", "1.0", "--repo", "a", "--repo", "b"})
	if err != nil || o.fromBuild != "my/job/12" || len(o.repos) != 2 {
		t.Fatalf("build mode: %+v %v", o, err)
	}

	for _, bad := range [][]string{
		{"app", "1.0"},
		{"app", "1.0", "--path", "a"},
		{"app", "1.0", "--repo", "a", "--repo", "b", "--path", "p"},
		{"app", "1.0", "--from-build", "nonumber"},
		{"app", "1.0", "--from-build", "job/"},
		{"app", "1.0", "--from-build", "j/1", "--repo", "r", "--path", "p"},
		{"app", "--repo", "r", "--path", "p"},
	} {
		if _, err := parseBundleAddArgs(bad); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}

func TestListedMatcher(t *testing.T) {
	q, match, err := listedMatcher(formatMaven, "com/acme/lib/1.0/lib-1.0.jar")
	if err != nil || q != "1.0" || !match(ciPackage{Filename: "com/acme/lib/1.0/lib-1.0.jar"}) || match(ciPackage{Filename: "other"}) {
		t.Errorf("maven: %q %v", q, err)
	}

	q, match, err = listedMatcher(formatRaw, "x/y.bin")
	if err != nil || q != "x/y.bin" || !match(ciPackage{Filename: "x/y.bin"}) {
		t.Errorf("raw: %q %v", q, err)
	}

	_, match, err = listedMatcher(formatDeb, "pool/app_1.0_amd64.deb")
	if err != nil || !match(ciPackage{Filename: "app_1.0_amd64.deb"}) {
		t.Errorf("deb: %v", err)
	}

	if _, _, err := listedMatcher(formatSBT, "too/short"); !errors.Is(err, errBundlePath) {
		t.Errorf("sbt short path: %v", err)
	}

	if _, _, err := listedMatcher(formatNPM, "x"); !errors.Is(err, errBundlePath) {
		t.Errorf("npm: %v", err)
	}
}

func TestDedupeSources(t *testing.T) {
	a, b := ciRepo{ID: "1"}, ciRepo{ID: "2"}
	in := []bundleSource{
		{repo: a, pkg: ciPackage{SHA256: "x"}, path: "p1"},
		{repo: a, pkg: ciPackage{SHA256: "x"}, path: "p2"},
		{repo: b, pkg: ciPackage{SHA256: "x"}, path: "p3"},
		{repo: a, pkg: ciPackage{SHA256: "y"}, path: "p4"},
	}

	got := dedupeSources(in)
	if len(got) != 3 || got[0].path != "p1" || got[1].path != "p3" || got[2].path != "p4" {
		t.Fatalf("dedupe = %+v", got)
	}
}

func TestVersionJSON(t *testing.T) {
	runCases(t, []contractCase{{
		name: "version json", args: []string{"--format", "json", "version"}, out: `"version"`,
	}})

	_, srv := newContractServer(t, nil)
	res := runOA(t, srv, "", "--format", "json", "version")

	var v versionInfo
	if err := json.Unmarshal([]byte(res.stdout), &v); err != nil || v.Version == "" || v.OS == "" || v.Arch == "" {
		t.Fatalf("version json = %q: %v", res.stdout, err)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

const bundleJSON = `{"id":"b1","name":"app","version":"1.0","description":"","state":"draft","pipeline_id":null,"current_stage":null,"created_by":"ci","sealed_at":null,"gpg_key_id":null}`

func TestContractCIUpload(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.bin")

	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	digest := sha("payload")

	rawRepo := ok(`{"id":"r1","name":"rawr","format":"raw","repo_type":"hosted"}`)
	props := m{"build.name": "job", "build.number": "7", "build.url": "http://ci/7", "vcs.revision": "abc", "team": "core"}

	runCases(t, []contractCase{
		{
			name: "raw upload sets properties and reports the artifact",
			args: []string{
				"--format", "json", "upload", "rawr", file, "--flat", "--target-path", "rel/", "--property", "team=core",
				"--build-name", "job", "--build-number", "7", "--build-url", "http://ci/7", "--vcs-revision", "abc",
			},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}": rawRepo,
				"PUT /repos/raw/{name}/*":       {status: 201, body: `{"id":"a1","path":"rel/app.bin","sha256":"` + digest + `","size":7}`},
				"PATCH /api/v1/repositories/{name}/artifacts/{artifact_id}/properties": ok(`{}`),
			},
			calls: []call{
				{method: "GET", pattern: "/api/v1/repository/{name}"},
				{method: "PUT", pattern: "/repos/raw/{name}/*"},
				{method: "PATCH", pattern: "/api/v1/repositories/{name}/artifacts/{artifact_id}/properties", body: props},
			},
			out: `"path": "rel/app.bin"`,
		},
		{
			name: "no properties means no properties call",
			args: []string{"upload", "rawr", file, "--flat"},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}": rawRepo,
				"PUT /repos/raw/{name}/*":       {status: 201, body: `{"id":"a1","sha256":"` + digest + `"}`},
			},
			calls: []call{{method: "GET", pattern: "/api/v1/repository/{name}"}, {method: "PUT", pattern: "/repos/raw/{name}/*"}},
			out:   "Uploaded rawr/app.bin",
		},
		{
			name: "digest mismatch fails",
			args: []string{"upload", "rawr", file, "--flat"},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}": rawRepo,
				"PUT /repos/raw/{name}/*":       {status: 201, body: `{"id":"a1","sha256":"` + sha("other") + `"}`},
			},
			calls: []call{{method: "GET", pattern: "/api/v1/repository/{name}"}, {method: "PUT", pattern: "/repos/raw/{name}/*"}},
			fail:  "different content",
		},
		{
			name: "maven verifies through the checksum sidecar",
			args: []string{"--format", "json", "upload", "mvn", file, "--flat", "--target-path", "com/acme/lib/1.0/lib-1.0.jar", "--build-name", "j"},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}":                                        ok(`{"id":"r2","name":"mvn","format":"maven","repo_type":"hosted"}`),
				"PUT /repos/maven/{name}/*":                                            {status: 201, headers: map[string]string{"X-OA-Artifact-Id": "m1"}},
				"GET /repos/maven/{name}/*":                                            ok(digest + "  lib-1.0.jar\n"),
				"PATCH /api/v1/repositories/{name}/artifacts/{artifact_id}/properties": ok(`{}`),
			},
			calls: []call{
				{method: "GET", pattern: "/api/v1/repository/{name}"},
				{method: "PUT", pattern: "/repos/maven/{name}/*"},
				{method: "GET", pattern: "/repos/maven/{name}/*"},
				{method: "PATCH", pattern: "/api/v1/repositories/{name}/artifacts/{artifact_id}/properties", body: m{"build.name": "j"}},
			},
			out: `"path": "com/acme/lib/1.0/lib-1.0.jar"`,
		},
		{
			name:    "other formats are refused with a hint",
			args:    []string{"upload", "npmr", file, "--flat"},
			replies: map[string]reply{"GET /api/v1/repository/{name}": ok(`{"id":"r3","name":"npmr","format":"npm","repo_type":"hosted"}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repository/{name}"}},
			fail:    "use the native client",
		},
		{
			name:    "proxy repositories are refused",
			args:    []string{"upload", "rawp", file, "--flat"},
			replies: map[string]reply{"GET /api/v1/repository/{name}": ok(`{"id":"r4","name":"rawp","format":"raw","repo_type":"proxy"}`)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repository/{name}"}},
			fail:    "not a hosted one",
		},
		{
			name:    "server refusal fails with its message and json carries the partial result",
			args:    []string{"--format", "json", "upload", "rawr", file, "--flat"},
			replies: map[string]reply{"GET /api/v1/repository/{name}": rawRepo, "PUT /repos/raw/{name}/*": {status: 403, body: `{"error":"forbidden","message":"no write access"}`}},
			calls:   []call{{method: "GET", pattern: "/api/v1/repository/{name}"}, {method: "PUT", pattern: "/repos/raw/{name}/*"}},
			out:     `"uploaded": []`,
			fail:    "no write access",
		},
		{
			name: "unmatched glob fails before any upload",
			args: []string{"upload", "rawr", filepath.Join(dir, "*.zip")},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}": rawRepo,
			},
			calls: []call{{method: "GET", pattern: "/api/v1/repository/{name}"}},
			fail:  "no file matches",
		},
	})
}

func TestContractCIBundle(t *testing.T) {
	const sealed = `{"id":"b1","name":"app","version":"1.0","description":"","state":"sealed","pipeline_id":"p1","current_stage":"dev","created_by":"ci","sealed_at":"2026-01-01T00:00:00Z","gpg_key_id":"K1"}`

	runCases(t, []contractCase{
		{
			name:    "create with pipeline by name",
			args:    []string{"--format", "json", "bundle", "create", "app", "1.0", "--description", "d", "--pipeline", "release"},
			replies: map[string]reply{"POST /api/v1/bundles": {status: 201, body: bundleJSON}},
			calls:   []call{{method: "POST", pattern: "/api/v1/bundles", body: m{"name": "app", "version": "1.0", "description": "d", "pipeline": "release"}}},
			out:     `"state": "draft"`,
		},
		{
			name:    "create without pipeline omits the field",
			args:    []string{"bundle", "create", "app", "1.0"},
			replies: map[string]reply{"POST /api/v1/bundles": {status: 201, body: bundleJSON}},
			calls:   []call{{method: "POST", pattern: "/api/v1/bundles", body: m{"name": "app", "version": "1.0", "description": ""}}},
			out:     "Bundle app 1.0: draft",
		},
		{
			name: "add by path",
			args: []string{"--format", "json", "bundle", "add", "app", "1.0", "--repo", "rawr", "--path", "rel/app.bin"},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}":               ok(`{"id":"r1","name":"rawr","format":"raw","repo_type":"hosted"}`),
				"GET /api/v1/repositories/{name}/packages":    ok(`{"total":2,"packages":[{"id":"a0","filename":"other","sha256":"` + sha("o") + `","size":1},{"id":"a1","filename":"rel/app.bin","sha256":"` + sha("p") + `","size":7}]}`),
				"POST /api/v1/bundles/{name}/{version}/items": {status: 201, body: `{"items":[]}`},
				"GET /api/v1/bundles/{name}/{version}":        ok(bundleJSON),
			},
			calls: []call{
				{method: "GET", pattern: "/api/v1/repository/{name}"},
				{method: "GET", pattern: "/api/v1/repositories/{name}/packages", query: map[string]string{"q": "rel/app.bin"}},
				{method: "POST", pattern: "/api/v1/bundles/{name}/{version}/items", body: m{"items": []any{
					m{"source_repo_id": "r1", "sha256": sha("p"), "artifact_path": "rel/app.bin", "format": "raw", "size_bytes": float64(7)},
				}}},
				{method: "GET", pattern: "/api/v1/bundles/{name}/{version}"},
			},
			out: `"added": 1`,
		},
		{
			name: "add from build searches the readable hosted repositories",
			args: []string{"--format", "json", "bundle", "add", "app", "1.0", "--from-build", "my/job/7"},
			replies: map[string]reply{
				"GET /api/v1/repositories": ok(`{"total":3,"repositories":[` +
					`{"id":"r1","name":"rawr","format":"raw","repo_type":"hosted"},` +
					`{"id":"r2","name":"px","format":"npm","repo_type":"proxy"},` +
					`{"id":"r3","name":"empty","format":"raw","repo_type":"hosted"}]}`),
				"GET /api/v1/repositories/{name}/artifacts/search": ok(`{"artifact_ids":["a1"]}`),
				"GET /api/v1/repositories/{name}/packages":         ok(`{"total":2,"packages":[{"id":"a0","filename":"x","sha256":"` + sha("o") + `","size":1},{"id":"a1","filename":"rel/app.bin","sha256":"` + sha("p") + `","size":7}]}`),
				"POST /api/v1/bundles/{name}/{version}/items":      {status: 201, body: `{"items":[]}`},
				"GET /api/v1/bundles/{name}/{version}":             ok(bundleJSON),
			},
			calls: []call{
				{method: "GET", pattern: "/api/v1/repositories"},
				{method: "GET", pattern: "/api/v1/repositories/{name}/artifacts/search", query: map[string]string{"key": "build.name", "value": "my/job"}},
				{method: "GET", pattern: "/api/v1/repositories/{name}/artifacts/search", query: map[string]string{"key": "build.number", "value": "7"}},
				{method: "GET", pattern: "/api/v1/repositories/{name}/packages"},
				{method: "GET", pattern: "/api/v1/repositories/{name}/artifacts/search", query: map[string]string{"key": "build.name", "value": "my/job"}},
				{method: "GET", pattern: "/api/v1/repositories/{name}/artifacts/search", query: map[string]string{"key": "build.number", "value": "7"}},
				{method: "GET", pattern: "/api/v1/repositories/{name}/packages"},
				{method: "POST", pattern: "/api/v1/bundles/{name}/{version}/items"},
				{method: "GET", pattern: "/api/v1/bundles/{name}/{version}"},
			},
			out: `"path": "rel/app.bin"`,
		},
		{
			name: "add from build fails when nothing carries the properties",
			args: []string{"bundle", "add", "app", "1.0", "--from-build", "my/job/7", "--repo", "rawr"},
			replies: map[string]reply{
				"GET /api/v1/repository/{name}":                    ok(`{"id":"r1","name":"rawr","format":"raw","repo_type":"hosted"}`),
				"GET /api/v1/repositories/{name}/artifacts/search": ok(`{"artifact_ids":[]}`),
			},
			calls: []call{
				{method: "GET", pattern: "/api/v1/repository/{name}"},
				{method: "GET", pattern: "/api/v1/repositories/{name}/artifacts/search"},
			},
			fail: "no artifact carries build.name=my/job and build.number=7",
		},
		{
			name:    "seal with a key",
			args:    []string{"--format", "json", "bundle", "seal", "app", "1.0", "--gpg-key", "K1"},
			replies: map[string]reply{"POST /api/v1/bundles/{name}/{version}/seal": ok(sealed)},
			calls:   []call{{method: "POST", pattern: "/api/v1/bundles/{name}/{version}/seal", body: m{"gpg_key_id": "K1"}}},
			out:     `"gpg_key_id": "K1"`,
		},
		{
			name:    "seal without a key sends no body",
			args:    []string{"bundle", "seal", "app", "1.0"},
			replies: map[string]reply{"POST /api/v1/bundles/{name}/{version}/seal": ok(sealed)},
			calls:   []call{{method: "POST", pattern: "/api/v1/bundles/{name}/{version}/seal"}},
			out:     "Bundle app 1.0: sealed, stage dev",
		},
		{
			name: "show resolves repository names",
			args: []string{"--format", "json", "bundle", "show", "app", "1.0"},
			replies: map[string]reply{
				"GET /api/v1/bundles/{name}/{version}":       ok(sealed),
				"GET /api/v1/bundles/{name}/{version}/items": ok(`{"items":[{"source_repo_id":"r1","sha256":"` + sha("p") + `","artifact_path":"rel/app.bin","format":"raw","size_bytes":7}]}`),
				"GET /api/v1/repositories":                   ok(`{"total":1,"repositories":[{"id":"r1","name":"rawr","format":"raw","repo_type":"hosted"}]}`),
			},
			calls: []call{
				{method: "GET", pattern: "/api/v1/bundles/{name}/{version}"},
				{method: "GET", pattern: "/api/v1/bundles/{name}/{version}/items"},
				{method: "GET", pattern: "/api/v1/repositories"},
			},
			out: `"repo": "rawr"`,
		},
		{
			name: "promote reports the target and counts",
			args: []string{"--format", "json", "promote", "app", "1.0", "--stage", "prod", "--target-repo", "prodr", "--notes", "go"},
			replies: map[string]reply{
				"POST /api/v1/bundles/{name}/{version}/promote": ok(`{"promotion":{"to_stage":"prod","target_repo_id":"r9","item_count":2},"copied":1,"existing":1,"replaced":0,"items":[]}`),
				"GET /api/v1/bundles/{name}/{version}":          ok(strings.Replace(sealed, `"dev"`, `"prod"`, 1)),
			},
			calls: []call{
				{method: "POST", pattern: "/api/v1/bundles/{name}/{version}/promote", body: m{"to_stage": "prod", "target_repo": "prodr", "notes": "go"}},
				{method: "GET", pattern: "/api/v1/bundles/{name}/{version}"},
			},
			out: `"target_repo": "prodr"`,
		},
		{
			name: "promote without target resolves the name the stage fixed",
			args: []string{"--format", "json", "promote", "app", "1.0", "--stage", "prod"},
			replies: map[string]reply{
				"POST /api/v1/bundles/{name}/{version}/promote": ok(`{"promotion":{"to_stage":"prod","target_repo_id":"r9","item_count":2},"copied":2,"existing":0,"replaced":0,"items":[]}`),
				"GET /api/v1/bundles/{name}/{version}":          ok(sealed),
				"GET /api/v1/repositories":                      ok(`{"total":1,"repositories":[{"id":"r9","name":"prodr","format":"raw","repo_type":"hosted"}]}`),
			},
			calls: []call{
				{method: "POST", pattern: "/api/v1/bundles/{name}/{version}/promote"},
				{method: "GET", pattern: "/api/v1/bundles/{name}/{version}"},
				{method: "GET", pattern: "/api/v1/repositories"},
			},
			out: `"target_repo": "prodr"`,
		},
		{
			name: "promote refusal fails with the server message",
			args: []string{"promote", "app", "1.0", "--stage", "prod"},
			replies: map[string]reply{
				"POST /api/v1/bundles/{name}/{version}/promote": {status: 403, body: `{"error":"forbidden","message":"write access to the target repository is required"}`},
			},
			calls: []call{{method: "POST", pattern: "/api/v1/bundles/{name}/{version}/promote"}},
			fail:  "write access to the target repository is required",
		},
		{name: "promote needs a stage", args: []string{"promote", "app", "1.0"}, fail: "Usage: oa promote"},
		{name: "unknown bundle subcommand", args: []string{"bundle", "frob"}, fail: "unknown bundle subcommand"},
	})
}
