// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

const gemYankedProp = "rubygems.yanked"

// gemFile builds a .gem: a tar holding the gzipped YAML spec and the gzipped
// payload archive, which is all the server reads.
func gemFile(t *testing.T, name, version, platform string) []byte {
	t.Helper()

	spec := "--- !ruby/object:Gem::Specification\nname: " + name +
		"\nversion: !ruby/object:Gem::Version\n  version: " + version +
		"\nplatform: " + platform + "\nsummary: test gem\n"

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, e := range [][2]string{
		{"metadata.gz", gemGzip(t, spec)},
		{"data.tar.gz", gemGzip(t, "payload of "+name+version+platform)},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o444, Size: int64(len(e[1]))}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func gemGzip(t *testing.T, s string) string {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

// gemFakeIdentity reads name, version and platform out of metadata.gz the way
// the server does: the push names nothing, the gem decides.
func gemFakeIdentity(data []byte) (string, string, string, bool) {
	tr := tar.NewReader(bytes.NewReader(data))

	for {
		h, err := tr.Next()
		if err != nil {
			return "", "", "", false
		}

		if h.Name != "metadata.gz" {
			continue
		}

		gz, err := gzip.NewReader(tr)
		if err != nil {
			return "", "", "", false
		}

		spec, _ := io.ReadAll(gz)
		lines := strings.Split(string(spec), "\n")

		var name, version, platform string

		for i, line := range lines {
			if v, found := strings.CutPrefix(line, "name: "); found {
				name = v
			}

			if v, found := strings.CutPrefix(line, "platform: "); found {
				platform = v
			}

			if line == "version: !ruby/object:Gem::Version" && i+1 < len(lines) {
				version = strings.TrimSpace(strings.TrimPrefix(lines[i+1], "  version: "))
			}
		}

		return name, version, platform, name != "" && version != "" && platform != ""
	}
}

// gemFakeFilename is how the server spells a gem file: the platform is left
// out for "ruby".
func gemFakeFilename(name, version, platform string) string {
	if platform == "ruby" {
		return name + "-" + version + ".gem"
	}

	return name + "-" + version + "-" + platform + ".gem"
}

func gemFakeYanked(it *fakeItem) bool {
	return it.props[gemYankedProp] == "true"
}

func init() { //nolint:gochecknoinits // the fake destination's route registry is filled by each format's tests
	fakeOARoutes["rubygems"] = gemFakeRoutes
}

// gemFakeRoutes implements the hosted RubyGems endpoints the importer uses:
// POST /api/v1/gems with the raw gem (201 {name,version,message}, 409 on a
// stored name, version and platform, yanked or not) and DELETE
// /api/v1/gems/yank with a form-encoded body, the only removal there is. A
// yanked gem stays in the repository and in the package listing.
func gemFakeRoutes(f *fakeOA, mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/gems", func(w http.ResponseWriter, r *http.Request) {
		f.writes++

		repo := r.Header.Get("X-Fake-Repo")

		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read gem", http.StatusBadRequest)
			return
		}

		name, version, platform, ok := gemFakeIdentity(data)
		if !ok {
			http.Error(w, "invalid gem", http.StatusBadRequest)
			return
		}

		file := gemFakeFilename(name, version, platform)

		for _, it := range f.repos[repo].items {
			if it.path == file {
				http.Error(w, "gem version already exists", http.StatusConflict)
				return
			}
		}

		it := f.store(repo, file, data, r.Header.Get(headerImportCreatedAt))
		it.name, it.version = name, version
		it.sha = f.reported(it)

		f.reply(w, http.StatusCreated, map[string]string{"name": name, "version": version, "message": "Successfully registered gem: " + name})
	})

	mux.HandleFunc("DELETE /api/v1/gems/yank", func(w http.ResponseWriter, r *http.Request) {
		f.writes++

		// `gem yank` sends the parameters as a form-encoded DELETE body.
		body, _ := io.ReadAll(r.Body)

		form, err := url.ParseQuery(string(body))
		if err != nil || form.Get("gem_name") == "" || form.Get("version") == "" {
			http.Error(w, "gem_name and version are required", http.StatusBadRequest)
			return
		}

		var hit []*fakeItem

		for _, it := range f.repos[r.Header.Get("X-Fake-Repo")].items {
			if it.name == form.Get("gem_name") && it.version == form.Get("version") && !gemFakeYanked(it) &&
				(form.Get("platform") == "" || it.path == gemFakeFilename(it.name, it.version, form.Get("platform"))) {
				hit = append(hit, it)
			}
		}

		if len(hit) == 0 {
			http.NotFound(w, r)
			return
		}

		for _, it := range hit {
			if it.props == nil {
				it.props = map[string]string{}
			}

			it.props[gemYankedProp] = "true"
		}

		f.deletes += len(hit)
		f.reply(w, http.StatusOK, map[string]string{"message": "Gem yanked successfully"})
	})
}

func gemOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"gems": {format: "rubygems", repoType: "hosted"}})
}

// gemNexusFiles lists a Nexus rubygems repository: gems under gems/, next to
// the generated indexes.
func gemNexusFiles(t *testing.T) []fakeFile {
	t.Helper()

	return []fakeFile{
		{path: "gems/rake-13.0.6.gem", data: gemFile(t, "rake", "13.0.6", "ruby"), created: nexusStamp, contentType: "application/octet-stream"},
		{path: "gems/nokogiri-1.16.0-x86_64-linux.gem", data: gemFile(t, "nokogiri", "1.16.0", "x86_64-linux"), created: nexusStamp},
		{path: "gems/nokogiri-1.16.0-java.gem", data: gemFile(t, "nokogiri", "1.16.0", "java"), created: nexusStamp},
		{path: "gems/my-gem-2.0.0.gem", data: gemFile(t, "my-gem", "2.0.0", "ruby"), created: nexusStamp},
		{path: "specs.4.8.gz", data: []byte("index"), created: nexusStamp},
		{path: "latest_specs.4.8.gz", data: []byte("index"), created: nexusStamp},
		{path: "quick/Marshal.4.8/rake-13.0.6.gemspec.rz", data: []byte("gemspec"), created: nexusStamp},
	}
}

func gemNexus(t *testing.T, files []fakeFile) (*httptest.Server, importConfig) {
	t.Helper()

	nexus := newFakeNexus(t, fakeSourceRepo{name: "gems-hosted", format: "rubygems", kind: "hosted", files: files})

	return nexus, parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "gems-hosted:gems")
}

func TestImportRubyGemsFromNexus(t *testing.T) {
	files := gemNexusFiles(t)
	nexus, cfg := gemNexus(t, files)
	oa := gemOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 4 || rep.Ignored != 3 {
		t.Fatalf("report = %+v (indexes and gemspecs must be ignored)", rep)
	}

	for i, want := range []struct{ file, name, version string }{
		{"rake-13.0.6.gem", "rake", "13.0.6"},
		{"nokogiri-1.16.0-x86_64-linux.gem", "nokogiri", "1.16.0"},
		{"nokogiri-1.16.0-java.gem", "nokogiri", "1.16.0"},
		{"my-gem-2.0.0.gem", "my-gem", "2.0.0"},
	} {
		it := oa.find("gems", want.file)
		if it == nil || it.name != want.name || it.version != want.version {
			t.Fatalf("gem %s = %+v", want.file, it)
		}

		// Each platform of one version must be verified against its own copy.
		if it.created != nexusWant || it.sha != sum256(files[i].data) || gemFakeYanked(it) {
			t.Fatalf("%s created=%q sha=%s yanked=%v", want.file, it.created, it.sha, gemFakeYanked(it))
		}
	}
}

func TestImportRubyGemsFromArtifactoryCopiesProperties(t *testing.T) {
	gem := gemFile(t, "rake", "13.0.6", "ruby")
	arti := newFakeArtifactory(t, fakeSourceRepo{name: "gems-local", format: "gems", kind: "local", files: []fakeFile{
		{path: "gems/rake-13.0.6.gem", data: gem, created: artiStamp, props: map[string][]string{
			"build.name": {"rake-ci"}, "team": {"secret-team"},
		}},
		{path: "specs.4.8.gz", data: []byte("index"), created: artiStamp},
		{path: "quick/Marshal.4.8/rake-13.0.6.gemspec.rz", data: []byte("gemspec"), created: artiStamp},
		{path: ".gems/rake-13.0.6.gem", data: []byte("index"), created: artiStamp},
	}})
	oa := gemOA(t)

	cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc", "--repo", "gems-local:gems")
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, arti, oa)

	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 3 {
		t.Fatalf("report = %+v", rep)
	}

	it := oa.find("gems", "rake-13.0.6.gem")
	if it == nil || it.created != artiWant || it.sha != sum256(gem) {
		t.Fatalf("item = %+v", it)
	}

	if it.props["build.name"] != "rake-ci" || it.props["team"] != "" {
		t.Fatalf("props = %v, want build.name only", it.props)
	}
}

func TestImportRubyGemsNamesTheGemByItsMetadata(t *testing.T) {
	// The source file name is not the gem's coordinates; the gem's own
	// metadata is, and the copy is verified wherever it lands.
	gem := gemFile(t, "rake", "13.0.6", "ruby")
	nexus, cfg := gemNexus(t, []fakeFile{{path: "gems/renamed.gem", data: gem, created: nexusStamp}})
	oa := gemOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	it := oa.find("gems", "rake-13.0.6.gem")
	if rep.failed() || it == nil || it.sha != sum256(gem) {
		t.Fatalf("report = %+v item = %+v", rep, it)
	}
}

func TestImportRubyGemsChecksumMismatchYanksTheCopyAndSaysSo(t *testing.T) {
	files := []fakeFile{
		{path: "gems/good-1.0.0.gem", data: gemFile(t, "good", "1.0.0", "ruby"), created: nexusStamp},
		{path: "gems/bad-1.0.0.gem", data: gemFile(t, "bad", "1.0.0", "ruby"), created: nexusStamp, sha256: sum256([]byte("original"))},
	}
	nexus, cfg := gemNexus(t, files)
	oa := gemOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Uploaded != 1 || rep.Failed != 1 {
		t.Fatalf("uploaded=%d failed=%d: %+v", rep.Uploaded, rep.Failed, rep.Failures)
	}

	// RubyGems hosting has no delete: the failure must say the copy was only
	// hidden, not claim it was removed.
	msg := rep.Failures[0].Error
	if !strings.Contains(msg, "checksum mismatch") || !strings.Contains(msg, "could not remove the bad copy") ||
		!strings.Contains(msg, "yanked instead") || strings.Contains(msg, "bad copy removed") {
		t.Fatalf("failure = %q", msg)
	}

	bad, good := oa.find("gems", "bad-1.0.0.gem"), oa.find("gems", "good-1.0.0.gem")
	if bad == nil || !gemFakeYanked(bad) || good == nil || gemFakeYanked(good) || oa.deletes != 1 {
		t.Fatalf("bad=%+v good=%+v yanks=%d, want only the bad copy yanked", bad, good, oa.deletes)
	}
}

func TestImportRubyGemsYankedCopyStillBlocksARepeatUpload(t *testing.T) {
	nexus, cfg := gemNexus(t, []fakeFile{
		{path: "gems/bad-1.0.0.gem", data: gemFile(t, "bad", "1.0.0", "ruby"), created: nexusStamp, sha256: sum256([]byte("original"))},
	})
	oa := gemOA(t)

	runImportTest(t, cfg, nexus, oa)

	// The yanked copy is not the source's bytes, so it is not "present", and
	// the version cannot be pushed again: reported, not silently skipped.
	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "already exists with different content") {
		t.Fatalf("report = %+v", rep)
	}

	if len(oa.repos["gems"].items) != 1 || oa.deletes != 1 {
		t.Fatalf("items=%d yanks=%d, want the one yanked copy and no second yank", len(oa.repos["gems"].items), oa.deletes)
	}
}

// A gem yanked on the destination after an earlier import stays yanked: the
// importer neither re-pushes it nor has any way to bring it back.
func TestImportRubyGemsNeverRevivesAYankedGem(t *testing.T) {
	data := gemFile(t, "rake", "13.0.6", "ruby")
	nexus, cfg := gemNexus(t, []fakeFile{{path: "gems/rake-13.0.6.gem", data: data, created: nexusStamp}})
	oa := gemOA(t)

	yanked := oa.store("gems", "rake-13.0.6.gem", data, "")
	yanked.name, yanked.version = "rake", "13.0.6"
	yanked.props = map[string]string{gemYankedProp: "true"}

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Present != 1 || rep.Uploaded != 0 || oa.writes != 0 || !gemFakeYanked(yanked) {
		t.Fatalf("report = %+v writes=%d yanked=%v", rep, oa.writes, gemFakeYanked(yanked))
	}
}

func TestImportRubyGemsServerDigestMismatchYanksTheCopy(t *testing.T) {
	nexus, cfg := gemNexus(t, []fakeFile{{path: "gems/rake-13.0.6.gem", data: gemFile(t, "rake", "13.0.6", "ruby"), created: nexusStamp}})
	oa := gemOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") || !strings.Contains(rep.Failures[0].Error, "yanked instead") {
		t.Fatalf("report = %+v", rep)
	}

	if it := oa.find("gems", "rake-13.0.6.gem"); it == nil || !gemFakeYanked(it) {
		t.Fatalf("corrupt copy not yanked: %+v", it)
	}
}

func TestImportRubyGemsIsIdempotent(t *testing.T) {
	nexus, cfg := gemNexus(t, gemNexusFiles(t))
	oa := gemOA(t)

	runImportTest(t, cfg, nexus, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 4 || oa.writes != writes {
		t.Fatalf("second run uploaded=%d present=%d writes+=%d: %+v", rep.Uploaded, rep.Present, oa.writes-writes, rep.Failures)
	}
}

func TestImportRubyGemsIsIdempotentWithoutSourceSHA256(t *testing.T) {
	nexus, cfg := gemNexus(t, gemNexusFiles(t))
	oa := gemOA(t)

	if rep := runImportWithoutSHA256(t, cfg, nexus, oa); rep.failed() || rep.Uploaded != 4 {
		t.Fatalf("first run: %+v", rep)
	}

	writes := oa.writes
	rep := runImportWithoutSHA256(t, cfg, nexus, oa)

	// Each platform of nokogiri is found under its own file name.
	if rep.failed() || rep.Uploaded != 0 || rep.Present != 4 || oa.writes != writes {
		t.Fatalf("second run uploaded=%d present=%d writes+=%d: %+v", rep.Uploaded, rep.Present, oa.writes-writes, rep.Failures)
	}
}

func TestImportRubyGemsResumesFromStateFile(t *testing.T) {
	nexus, cfg := gemNexus(t, gemNexusFiles(t))
	cfg.StateFile = filepath.Join(t.TempDir(), "state.jsonl")
	oa := gemOA(t)

	if rep := runImportTest(t, cfg, nexus, oa); rep.Uploaded != 4 {
		t.Fatalf("first run: %+v", rep)
	}

	oa.repos["gems"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Resumed != 4 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}
}

func TestImportRubyGemsDryRunWritesNothing(t *testing.T) {
	files := gemNexusFiles(t)
	nexus, cfg := gemNexus(t, files)
	cfg.DryRun = true
	oa := gemOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 4 || !rep.DryRun || oa.writes != 0 || len(oa.repos["gems"].items) != 0 {
		t.Fatalf("report = %+v writes=%d", rep, oa.writes)
	}

	if want := int64(len(files[0].data) + len(files[1].data) + len(files[2].data) + len(files[3].data)); rep.Bytes != want {
		t.Fatalf("planned bytes = %d, want %d", rep.Bytes, want)
	}
}

func TestImportRubyGemsReportsConflictAndKeepsTheStoredGem(t *testing.T) {
	nexus, cfg := gemNexus(t, []fakeFile{{path: "gems/rake-13.0.6.gem", data: gemFile(t, "rake", "13.0.6", "ruby"), created: nexusStamp}})
	oa := gemOA(t)

	stored := oa.store("gems", "rake-13.0.6.gem", []byte("an earlier, different rake"), "")
	stored.name, stored.version = "rake", "13.0.6"
	want := stored.sha

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "already exists with different content") {
		t.Fatalf("report = %+v", rep)
	}

	if oa.deletes != 0 || gemFakeYanked(stored) || oa.repos["gems"].items[0].sha != want {
		t.Fatalf("existing gem touched (yanks=%d)", oa.deletes)
	}
}

func TestImportRubyGemsRejectsAFileThatIsNotAGem(t *testing.T) {
	nexus, cfg := gemNexus(t, []fakeFile{{path: "gems/broken-1.0.0.gem", data: []byte("not a gem"), created: nexusStamp}})
	oa := gemOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "invalid gem") || len(oa.repos["gems"].items) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestRubyGemsRemoveYanksOnlyTheGemItWasAskedAbout(t *testing.T) {
	oa := gemOA(t)

	for _, platform := range []string{"ruby", "java"} {
		it := oa.store("gems", gemFakeFilename("nokogiri", "1.16.0", platform), []byte(platform), "")
		it.name, it.version = "nokogiri", "1.16.0"
	}

	tgt := &importTarget{c: oa.client()}
	java := oa.find("gems", "nokogiri-1.16.0-java.gem")

	err := gemRemove(context.Background(), tgt, "gems", sourceAsset{}, uploadResult{ID: java.id})
	if !errors.Is(err, errGemYanked) {
		t.Fatalf("err = %v, want errGemYanked", err)
	}

	if !gemFakeYanked(java) || gemFakeYanked(oa.find("gems", "nokogiri-1.16.0.gem")) {
		t.Fatal("the other platform of the version must stay live")
	}
}

func TestRubyGemsRemoveRefusesToGuessThePlatform(t *testing.T) {
	oa := gemOA(t)

	odd := oa.store("gems", "odd-name.gem", []byte("x"), "")
	odd.name, odd.version = "nokogiri", "1.16.0"

	err := gemRemove(context.Background(), &importTarget{c: oa.client()}, "gems", sourceAsset{}, uploadResult{ID: odd.id})
	if !errors.Is(err, errImportVerify) || oa.writes != 0 || gemFakeYanked(odd) {
		t.Fatalf("err = %v writes=%d, want a refusal before any yank", err, oa.writes)
	}
}

func TestRemoveOfAnUnlistedPackageFailsForEveryFormat(t *testing.T) {
	oa := newFakeOA(t, map[string]*fakeRepo{"r": {format: "rubygems", repoType: "hosted"}})
	tgt := &importTarget{c: oa.client()}
	res := uploadResult{ID: "no-such-id"}

	for name, remove := range map[string]func(context.Context, *importTarget, string, sourceAsset, uploadResult) error{
		"helm": helmRemove, "nuget": nugetRemove, "rubygems": gemRemove,
	} {
		if err := remove(context.Background(), tgt, "r", sourceAsset{}, res); !errors.Is(err, errNotFound) {
			t.Errorf("%s: err = %v, want errNotFound", name, err)
		}
	}
}

func TestGemPlatform(t *testing.T) {
	for _, tc := range []struct{ file, name, version, want string }{
		{"rake-13.0.6.gem", "rake", "13.0.6", "ruby"},
		{"nokogiri-1.16.0-x86_64-linux.gem", "nokogiri", "1.16.0", "x86_64-linux"},
		{"nokogiri-1.16.0-java.gem", "nokogiri", "1.16.0", "java"},
		{"my-gem-2.0.0.gem", "my-gem", "2.0.0", "ruby"},
		{"gems/ignored-dir.gem", "rake", "13.0.6", ""},
		{"renamed.gem", "rake", "13.0.6", ""},
		{"rake-13.0.61.gem", "rake", "13.0.6", ""},
	} {
		if got := gemPlatform(tc.file, tc.name, tc.version); got != tc.want {
			t.Errorf("gemPlatform(%q, %q, %q) = %q, want %q", tc.file, tc.name, tc.version, got, tc.want)
		}
	}
}

func TestImportableGem(t *testing.T) {
	for p, want := range map[string]bool{
		"gems/rake-13.0.6.gem":                     true,
		"rake-13.0.6.gem":                          true,
		"gems/nokogiri-1.16.0-x86_64-linux.gem":    true,
		"specs.4.8.gz":                             false,
		"latest_specs.4.8.gz":                      false,
		"prerelease_specs.4.8.gz":                  false,
		"quick/Marshal.4.8/rake-13.0.6.gemspec.rz": false,
		"gems/rake-13.0.6.gem.sha1":                false,
		".gems/rake-13.0.6.gem":                    false,
		"rake-13.0.6.gemspec":                      false,
	} {
		if got := importable(formatRubyGems, p); got != want {
			t.Errorf("importable(rubygems, %q) = %v, want %v", p, got, want)
		}
	}
}
