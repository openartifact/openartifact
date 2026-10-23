// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// npmReplaceFile swaps the content of one file of a fixture.
func npmReplaceFile(files []fakeFile, path, data string) []fakeFile {
	out := slices.Clone(files)

	for i := range out {
		if out[i].path == path {
			out[i].data = []byte(data)
		}
	}

	return out
}

// npmWantTags is what the default Nexus fixture ends with: the tags of the
// packuments plus the latest derived from the versions.
var (
	npmWantLeftPadTags = map[string]string{"latest": "1.1.0", "beta": "2.0.0-beta.1", "legacy": "1.0.0"} //nolint:gochecknoglobals // read-only expectations shared by the tests
	npmWantWidgetTags  = map[string]string{"latest": "0.2.0-rc.2", "rc": "0.2.0-rc.1"}                   //nolint:gochecknoglobals // read-only expectations shared by the tests
)

func TestImportNPMTagsNexusImportsEveryDistTag(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.failed() {
		t.Fatalf("report = %+v", rep)
	}

	n := npmState(t, oa)

	if got := n.tags["reg/left-pad"]; !maps.Equal(got, npmWantLeftPadTags) {
		t.Errorf("left-pad tags = %v, want %v", got, npmWantLeftPadTags)
	}

	// A scoped name travels as one escaped segment (the fake refuses anything else).
	if got := n.tags["reg/@acme/widget"]; !maps.Equal(got, npmWantWidgetTags) {
		t.Errorf("@acme/widget tags = %v, want %v", got, npmWantWidgetTags)
	}

	// "latest" comes with the publish; the three others are separate requests.
	if n.tagPuts != 3 {
		t.Errorf("%d dist-tag requests, want 3", n.tagPuts)
	}
}

func TestImportNPMTagsSourceLatestWins(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing string // "latest" the destination already holds, if any
	}{
		{"on an empty destination", ""},
		{"over a higher release the destination has", "3.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, _ := npmNexusFixtures(t)
			files = npmReplaceFile(files, "left-pad", `{"dist-tags":{"latest":"1.0.0","next":"2.0.0-beta.1"}}`)
			nexus := npmNexusServer(t, files)
			oa := npmNewOA(t)
			n := npmState(t, oa)

			if tc.existing != "" {
				n.seed("reg", "left-pad", tc.existing, []byte("newer"), "latest")
			}

			if rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa); rep.failed() {
				t.Fatalf("report = %+v", rep)
			}

			// The versions alone would have made 1.1.0 the latest.
			if got, want := n.tags["reg/left-pad"], (map[string]string{"latest": "1.0.0", "next": "2.0.0-beta.1"}); !maps.Equal(got, want) {
				t.Fatalf("left-pad tags = %v, want %v", got, want)
			}
		})
	}
}

func TestImportNPMTagsArtifactory(t *testing.T) {
	for _, mode := range []string{"aql", "storage"} {
		t.Run(mode, func(t *testing.T) {
			var files []fakeFile

			for _, s := range []struct{ name, file, version string }{
				{"left-pad", "left-pad/-/left-pad-1.0.0.tgz", "1.0.0"},
				{"left-pad", "left-pad/-/left-pad-1.1.0.tgz", "1.1.0"},
				{"@acme/widget", "@acme/widget/-/@acme/widget-1.0.0.tgz", "1.0.0"},
			} {
				files = append(files, fakeFile{path: s.file, data: npmTestTarball(t, s.name, s.version, nil), created: artiStamp})
			}

			// Artifactory keeps the packuments in a dot directory the payload listing skips.
			files = append(files,
				fakeFile{path: ".npm/left-pad/package.json", data: []byte(`{"name":"left-pad","dist-tags":{"latest":"1.0.0","stable":"1.1.0"}}`), created: artiStamp},
				fakeFile{path: ".npm/@acme/widget/package.json", data: []byte(`{"name":"@acme/widget","dist-tags":{"latest":"1.0.0","canary":"1.0.0"}}`), created: artiStamp},
			)

			arti := newFakeArtifactory(t, fakeSourceRepo{name: "npm-local", format: "npm", kind: "local", files: files})
			oa := npmNewOA(t)

			cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc", "--repo", "npm-local:reg", "--artifactory-listing", mode)
			cfg.SourceToken = "secret"

			rep := runImportTest(t, cfg, arti, oa)
			if rep.failed() || rep.Uploaded != 3 {
				t.Fatalf("report = %+v", rep)
			}

			n := npmState(t, oa)

			if got, want := n.tags["reg/left-pad"], (map[string]string{"latest": "1.0.0", "stable": "1.1.0"}); !maps.Equal(got, want) {
				t.Errorf("left-pad tags = %v, want %v", got, want)
			}

			if got, want := n.tags["reg/@acme/widget"], (map[string]string{"latest": "1.0.0", "canary": "1.0.0"}); !maps.Equal(got, want) {
				t.Errorf("@acme/widget tags = %v, want %v", got, want)
			}
		})
	}
}

func TestImportNPMTagsAreSkippedAndReportedWhenTheyCannotBeApplied(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	files = npmReplaceFile(files, "left-pad",
		`{"dist-tags":{"ghost":"9.9.9","beta":"2.0.0-beta.1","legacy":"1.0.0","3.0.0":"1.0.0"}}`)

	for i := range files {
		// The prerelease never makes it: its copy fails verification and is removed.
		if files[i].path == "left-pad/-/left-pad-2.0.0-beta.1.tgz" {
			files[i].sha256 = sum256([]byte("what the source claimed"))
		}
	}

	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.Uploaded != 4 || rep.Failed != 4 {
		t.Fatalf("report = %+v", rep)
	}

	errs := map[string]string{}

	for _, f := range rep.Failures {
		errs[f.Path] += f.Error + "\n"
	}

	for _, want := range []string{
		"dist-tag ghost -> 9.9.9: the version is not in the destination",
		"dist-tag beta -> 2.0.0-beta.1: the version is not in the destination",
		"dist-tag 3.0.0 -> 1.0.0: ",
	} {
		if !strings.Contains(errs["left-pad"], want) {
			t.Errorf("no failure line %q for the packument; failures: %v", want, errs)
		}
	}

	if !strings.Contains(errs["left-pad/-/left-pad-2.0.0-beta.1.tgz"], "checksum mismatch") {
		t.Errorf("failures = %v, want the version's own failure too", errs)
	}

	n := npmState(t, oa)

	// The tags that could be applied were: a failing one does not stop the others.
	if got, want := n.tags["reg/left-pad"], (map[string]string{"latest": "1.1.0", "legacy": "1.0.0"}); !maps.Equal(got, want) {
		t.Errorf("left-pad tags = %v, want %v", got, want)
	}

	if got := n.tags["reg/@acme/widget"]; !maps.Equal(got, npmWantWidgetTags) {
		t.Errorf("@acme/widget tags = %v, want %v", got, npmWantWidgetTags)
	}
}

func TestImportNPMTagsAreIdempotent(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)
	cfg := npmNexusCfg(t, nexus)

	runImportTest(t, cfg, nexus, oa)

	n := npmState(t, oa)
	writes, puts := oa.writes, n.tagPuts

	rep := runImportTest(t, cfg, nexus, oa)
	if rep.failed() || rep.Present != 5 || oa.writes != writes || n.tagPuts != puts {
		t.Fatalf("second run: %+v, writes+=%d, tag requests+=%d, want nothing written", rep, oa.writes-writes, n.tagPuts-puts)
	}

	if got := n.tags["reg/left-pad"]; !maps.Equal(got, npmWantLeftPadTags) {
		t.Errorf("left-pad tags = %v, want %v", got, npmWantLeftPadTags)
	}
}

func TestImportNPMTagsMovedAtTheSourceFollowOnTheNextRun(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)

	moved := npmNexusServer(t, npmReplaceFile(files, "left-pad", `{"dist-tags":{"beta":"1.1.0","legacy":"1.0.0"}}`))

	if rep := runImportTest(t, npmNexusCfg(t, moved), moved, oa); rep.failed() {
		t.Fatalf("report = %+v", rep)
	}

	if got := npmState(t, oa).tags["reg/left-pad"]["beta"]; got != "1.1.0" {
		t.Fatalf("beta = %q, want it moved to 1.1.0", got)
	}
}

func TestImportNPMTagsDryRunWritesNothing(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	var progress bytes.Buffer

	imp, err := newImporter(npmNexusCfg(t, nexus, "--dry-run"), newImportSource(npmNexusCfg(t, nexus), nexus.Client()), &importTarget{c: oa.client()}, &progress)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(imp.state.close)

	if rep := imp.run(context.Background()); rep.failed() {
		t.Fatalf("report = %+v", rep)
	}

	if n := npmState(t, oa); oa.writes != 0 || n.tagPuts != 0 || len(n.tags) != 0 {
		t.Fatalf("dry run wrote: writes=%d tag requests=%d tags=%v", oa.writes, n.tagPuts, n.tags)
	}

	for _, want := range []string{"would tag left-pad beta -> 2.0.0-beta.1", "would tag left-pad legacy -> 1.0.0", "would tag @acme/widget rc -> 0.2.0-rc.1"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress lacks %q:\n%s", want, progress.String())
		}
	}
}

func TestImportNPMTagsAreAppliedWhenAResumedRunFindsEveryVersionDone(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)
	n := npmState(t, oa)
	state := filepath.Join(t.TempDir(), "state.jsonl")
	cfg := npmNexusCfg(t, nexus, "--state-file", state)

	n.rejectTags = "dist-tags are frozen"

	rep := runImportTest(t, cfg, nexus, oa)
	if rep.Uploaded != 5 || rep.Failed != 3 || !strings.Contains(rep.Failures[0].Error, "dist-tags are frozen") {
		t.Fatalf("first run: %+v", rep)
	}

	n.rejectTags = ""
	writes := oa.writes

	rep = runImportTest(t, cfg, nexus, oa)
	if rep.failed() || rep.Resumed != 5 || rep.Uploaded != 0 {
		t.Fatalf("second run: %+v", rep)
	}

	if got := n.tags["reg/left-pad"]; !maps.Equal(got, npmWantLeftPadTags) {
		t.Errorf("left-pad tags = %v, want %v", got, npmWantLeftPadTags)
	}

	// Only the three tags the first run could not set.
	if oa.writes != writes+3 {
		t.Errorf("second run wrote %d times, want just the 3 tags", oa.writes-writes)
	}
}

func TestImportNPMTagsWithoutPackumentAreNotAnError(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, npmTarballs(files))
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.failed() {
		t.Fatalf("report = %+v", rep)
	}

	// Without a packument only the latest derived from the versions is set.
	n := npmState(t, oa)
	if n.tagPuts != 0 || !maps.Equal(n.tags["reg/left-pad"], map[string]string{"latest": "1.1.0"}) {
		t.Fatalf("tags = %v after %d tag requests", n.tags, n.tagPuts)
	}
}

func TestImportNPMTagsUnreadablePackumentIsReported(t *testing.T) {
	files, wants := npmNexusFixtures(t)
	nexus := npmNexusServer(t, npmReplaceFile(files, "left-pad", "<html>not a packument</html>"))
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.Uploaded != 5 || rep.Failed != 1 || rep.Failures[0].Path != "left-pad" || !strings.Contains(rep.Failures[0].Error, "decoding the packument") {
		t.Fatalf("report = %+v", rep)
	}

	npmCheckPublished(t, oa, wants, nexusWant)

	if got := npmState(t, oa).tags["reg/@acme/widget"]; !maps.Equal(got, npmWantWidgetTags) {
		t.Errorf("the other package's tags = %v, want %v", got, npmWantWidgetTags)
	}
}

func TestImportNPMPackumentLocation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		tarball    sourceAsset
		pkg        string
		wantWhere  string
		wantURL    string
		wantFailed bool
	}{
		{
			name: "nexus", kind: "nexus", pkg: "left-pad",
			tarball:   sourceAsset{Path: "left-pad/-/left-pad-1.0.0.tgz", URL: "http://nx/repository/npm/left-pad/-/left-pad-1.0.0.tgz"},
			wantWhere: "left-pad", wantURL: "http://nx/repository/npm/left-pad",
		},
		{
			name: "nexus scoped", kind: "nexus", pkg: "@acme/widget",
			tarball:   sourceAsset{Path: "@acme/widget/-/widget-1.0.0.tgz", URL: "http://nx/repository/npm/@acme/widget/-/widget-1.0.0.tgz"},
			wantWhere: "@acme/widget", wantURL: "http://nx/repository/npm/@acme/widget",
		},
		{
			name: "artifactory", kind: "artifactory", pkg: "left-pad",
			tarball:   sourceAsset{Path: "left-pad/-/left-pad-1.0.0.tgz", URL: "http://af/artifactory/npm-local/left-pad/-/left-pad-1.0.0.tgz"},
			wantWhere: ".npm/left-pad/package.json", wantURL: "http://af/artifactory/npm-local/.npm/left-pad/package.json",
		},
		{
			name: "artifactory scoped", kind: "artifactory", pkg: "@acme/widget",
			tarball:   sourceAsset{Path: "@acme/widget/-/@acme/widget-1.0.0.tgz", URL: "http://af/artifactory/npm-local/@acme/widget/-/@acme/widget-1.0.0.tgz"},
			wantWhere: ".npm/@acme/widget/package.json", wantURL: "http://af/artifactory/npm-local/.npm/@acme/widget/package.json",
		},
		{
			name: "a url that is not the listed path", kind: "artifactory", pkg: "left-pad", wantFailed: true,
			tarball: sourceAsset{Path: "left-pad/-/left-pad-1.0.0.tgz", URL: "http://af/elsewhere"},
		},
		{
			name: "a nexus url without the tarball directory", kind: "nexus", pkg: "left-pad", wantFailed: true,
			tarball: sourceAsset{Path: "left-pad/-/left-pad-1.0.0.tgz", URL: "http://nx/left-pad.tgz"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			where, rawURL, err := npmPackumentLocation(tc.kind, tc.tarball, tc.pkg)
			if tc.wantFailed {
				if err == nil {
					t.Fatalf("got %q %q, want an error", where, rawURL)
				}

				return
			}

			if err != nil || where != tc.wantWhere || rawURL != tc.wantURL {
				t.Fatalf("got %q %q, %v; want %q %q", where, rawURL, err, tc.wantWhere, tc.wantURL)
			}
		})
	}
}
