// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// ─── fake npm registry ────────────────────────────────────────────────────────

// npmFake is the npm side of the fake OpenArtifact: the publish, dist-tags and
// unpublish routes with the checks the real server makes, and a record of what
// reached them.
type npmFake struct {
	f           *fakeOA
	tags        map[string]map[string]string // "repo/package" -> tag -> version
	manifests   map[string]map[string]any    // "repo/package@version" -> published manifest
	published   []string                     // "package@version", in arrival order
	unpublished []string
	conflicts   int
	tagPuts     int    // dist-tag PUTs that reached the fake, whatever their outcome
	rejectTags  string // when set, every dist-tag PUT is refused with this message
}

// npmFakes finds the npm fake of a fakeOA: its route set creates it.
var npmFakes sync.Map //nolint:gochecknoglobals // per-server state of the fake, looked up by the test that built the server

func init() { //nolint:gochecknoinits // the fake destination's route registry is filled by each format's tests
	fakeOARoutes["npm"] = func(f *fakeOA, mux *http.ServeMux) {
		n := &npmFake{f: f, tags: map[string]map[string]string{}, manifests: map[string]map[string]any{}}
		npmFakes.Store(f, n)

		mux.HandleFunc("PUT /{pkg...}", n.publish)
		mux.HandleFunc("GET /-/package/{rest...}", n.distTags)
		mux.HandleFunc("PUT /-/package/{rest...}", n.setTag)
		mux.HandleFunc("DELETE /{rest...}", n.unpublish)
	}
}

func npmState(t *testing.T, oa *fakeOA) *npmFake {
	t.Helper()

	n, ok := npmFakes.Load(oa)
	if !ok {
		t.Fatal("no npm routes on this fake")
	}

	return n.(*npmFake)
}

// npmRawPath is the request target below the repository, still escaped: a
// scoped name must travel as the single segment "@scope%2Fname".
func npmRawPath(r *http.Request) string {
	return strings.TrimPrefix(r.RequestURI, "/repos/"+r.Header.Get("X-Fake-Repo")+"/")
}

// npmSegment unescapes one path segment holding a package name.
func npmSegment(raw string) (string, bool) {
	name, err := url.PathUnescape(raw)

	return name, err == nil && name != "" && !strings.Contains(raw, "/")
}

// npmFakeDoc reads a publish request, with the field names of the npm
// registry protocol.
type npmFakeDoc struct {
	ID          string                    `json:"_id"` //nolint:tagliatelle // npm registry protocol
	Name        string                    `json:"name"`
	DistTags    map[string]string         `json:"dist-tags"` //nolint:tagliatelle // npm registry protocol
	Versions    map[string]map[string]any `json:"versions"`
	Attachments map[string]npmAttachment  `json:"_attachments"` //nolint:tagliatelle // npm registry protocol
}

// check validates a publish document the way the server does and returns the
// version and tarball it carries.
func (d npmFakeDoc) check(name string) (string, []byte, string) {
	if d.Name != name || d.ID != name {
		return "", nil, "name does not match the URL"
	}

	if len(d.Versions) != 1 || len(d.Attachments) != 1 {
		return "", nil, "one version and one attachment per publish"
	}

	for version, manifest := range d.Versions {
		if manifest["name"] != name || manifest["version"] != version {
			return "", nil, "manifest name/version do not match its key"
		}

		if _, stray := manifest["dist"]; stray {
			return "", nil, "manifest must not carry dist"
		}

		att, ok := d.Attachments[name+"-"+version+".tgz"]
		if !ok || att.ContentType != "application/octet-stream" {
			return "", nil, "attachment must be <name>-<version>.tgz of type application/octet-stream"
		}

		data, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil || int64(len(data)) != att.Length {
			return "", nil, "attachment data is not base64 of the declared length"
		}

		return version, data, ""
	}

	return "", nil, "unreachable"
}

func (n *npmFake) has(repo, name, version string) bool {
	for _, it := range n.f.repos[repo].items {
		if it.name == name && it.version == version {
			return true
		}
	}

	return false
}

func (n *npmFake) bad(w http.ResponseWriter, code int, msg string) {
	n.f.reply(w, code, map[string]string{"error": msg})
}

func (n *npmFake) publish(w http.ResponseWriter, r *http.Request) {
	n.f.writes++

	repo := r.Header.Get("X-Fake-Repo")

	name, ok := npmSegment(npmRawPath(r))
	if !ok {
		n.bad(w, http.StatusBadRequest, "the package name must be one escaped path segment")
		return
	}

	if r.Header.Get("Content-Type") != "application/json" {
		n.bad(w, http.StatusUnsupportedMediaType, "publish body must be application/json")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil || r.ContentLength != int64(len(body)) {
		n.bad(w, http.StatusBadRequest, "publish body must be sent with its Content-Length")
		return
	}

	var doc npmFakeDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		n.bad(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	version, tarball, problem := doc.check(name)
	if problem != "" {
		n.bad(w, http.StatusBadRequest, problem)
		return
	}

	for tag, target := range doc.DistTags {
		if target != version && !n.has(repo, name, target) {
			n.bad(w, http.StatusBadRequest, "dist-tag "+tag+" points to an unknown version")
			return
		}
	}

	if n.has(repo, name, version) {
		n.conflicts++
		n.bad(w, http.StatusConflict, "version already exists")

		return
	}

	it := &fakeItem{
		id: n.f.nextID(), path: npmFakeFile(name, version), name: name, version: version,
		sha: sum256(tarball), size: len(tarball), created: r.Header.Get(headerImportCreatedAt),
	}
	it.sha = n.f.reported(it)
	n.f.repos[repo].items = append(n.f.repos[repo].items, it)

	key := repo + "/" + name
	if n.tags[key] == nil {
		n.tags[key] = map[string]string{}
	}

	maps.Copy(n.tags[key], doc.DistTags)

	n.manifests[key+"@"+version] = doc.Versions[version]
	n.published = append(n.published, name+"@"+version)

	n.f.reply(w, http.StatusCreated, map[string]any{"ok": true, "id": name, "rev": "1-fake"})
}

// npmFakeFile is the tarball file name of a version: no scope in it.
func npmFakeFile(name, version string) string {
	return name[strings.LastIndex(name, "/")+1:] + "-" + version + ".tgz"
}

func (n *npmFake) distTags(w http.ResponseWriter, r *http.Request) {
	repo := r.Header.Get("X-Fake-Repo")

	raw, ok := strings.CutSuffix(strings.TrimPrefix(npmRawPath(r), "-/package/"), "/dist-tags")
	name, valid := npmSegment(raw)

	if !ok || !valid {
		n.bad(w, http.StatusBadRequest, "bad dist-tags request")
		return
	}

	known := false

	for _, it := range n.f.repos[repo].items {
		known = known || it.name == name
	}

	if !known {
		n.bad(w, http.StatusNotFound, "package not found")
		return
	}

	tags := n.tags[repo+"/"+name]
	if tags == nil {
		tags = map[string]string{}
	}

	n.f.reply(w, http.StatusOK, tags)
}

// setTag is PUT /-/package/<pkg>/dist-tags/<tag> with the JSON string of a
// version as its body, which must be one the package has and the tag must not
// be a version itself, as on the real server.
func (n *npmFake) setTag(w http.ResponseWriter, r *http.Request) {
	n.f.writes++
	n.tagPuts++

	repo := r.Header.Get("X-Fake-Repo")
	rawName, tag, ok := strings.Cut(strings.TrimPrefix(npmRawPath(r), "-/package/"), "/dist-tags/")
	name, valid := npmSegment(rawName)

	if !ok || !valid || tag == "" || strings.Contains(tag, "/") {
		n.bad(w, http.StatusBadRequest, "bad dist-tag request: "+npmRawPath(r))
		return
	}

	var version string
	if r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&version) != nil {
		n.bad(w, http.StatusBadRequest, "invalid body")
		return
	}

	if _, isVersion := npmParseSemver(tag); isVersion {
		n.bad(w, http.StatusBadRequest, "tag name must not be a valid semver version")
		return
	}

	if n.rejectTags != "" {
		n.bad(w, http.StatusBadRequest, n.rejectTags)
		return
	}

	if !n.has(repo, name, version) {
		n.bad(w, http.StatusNotFound, "version not found")
		return
	}

	key := repo + "/" + name
	if n.tags[key] == nil {
		n.tags[key] = map[string]string{}
	}

	n.tags[key][tag] = version
	n.f.reply(w, http.StatusOK, n.tags[key])
}

// unpublish is DELETE /<pkg>/-/<file>.tgz/-rev/<rev>.
func (n *npmFake) unpublish(w http.ResponseWriter, r *http.Request) {
	n.f.writes++
	n.f.deletes++

	repo := r.Header.Get("X-Fake-Repo")
	parts := strings.Split(npmRawPath(r), "/")

	if len(parts) != 5 || parts[1] != "-" || parts[3] != "-rev" {
		n.bad(w, http.StatusBadRequest, "not an unpublish request: "+npmRawPath(r))
		return
	}

	name, okName := npmSegment(parts[0])
	file, okFile := npmSegment(parts[2])

	if !okName || !okFile {
		n.bad(w, http.StatusBadRequest, "bad unpublish request")
		return
	}

	items := n.f.repos[repo].items

	for i, it := range items {
		if it.name != name || it.path != file {
			continue
		}

		n.f.repos[repo].items = slices.Delete(items, i, i+1)
		n.unpublished = append(n.unpublished, name+"@"+it.version)

		for tag, target := range n.tags[repo+"/"+name] {
			if target == it.version {
				delete(n.tags[repo+"/"+name], tag)
			}
		}

		n.f.reply(w, http.StatusOK, map[string]bool{"ok": true})

		return
	}

	n.bad(w, http.StatusNotFound, "version not found")
}

// seed adds a version to the destination as if it had been published.
func (n *npmFake) seed(repo, name, version string, data []byte, tags ...string) *fakeItem {
	it := &fakeItem{
		id: n.f.nextID(), path: npmFakeFile(name, version), name: name, version: version,
		sha: sum256(data), size: len(data),
	}
	n.f.repos[repo].items = append(n.f.repos[repo].items, it)

	key := repo + "/" + name
	if n.tags[key] == nil {
		n.tags[key] = map[string]string{}
	}

	for _, tag := range tags {
		n.tags[key][tag] = version
	}

	return it
}

func (n *npmFake) versions(name string) []string {
	var out []string

	for _, p := range n.published {
		if v, ok := strings.CutPrefix(p, name+"@"); ok {
			out = append(out, v)
		}
	}

	return out
}

// ─── fixtures ─────────────────────────────────────────────────────────────────

// npmTestTarball packs an npm tarball the way `npm pack` does: everything under
// "package/". extra is merged into package.json.
func npmTestTarball(t *testing.T, name, version string, extra map[string]any) []byte {
	t.Helper()

	manifest := map[string]any{"name": name, "version": version, "description": "fixture " + name, "main": "index.js"}
	maps.Copy(manifest, extra)

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	for _, f := range []struct{ name, body string }{
		{"package/package.json", string(raw)},
		{"package/index.js", "module.exports = " + `"` + name + "@" + version + `"`},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

type npmWant struct {
	name, version string
	data          []byte
}

// npmNexusFixtures is the content of a Nexus npm repository: tarballs listed
// out of version order, a prerelease above the last stable version, a
// prerelease-only scoped package and the packuments Nexus keeps next to them.
func npmNexusFixtures(t *testing.T) ([]fakeFile, []npmWant) {
	t.Helper()

	var (
		files []fakeFile
		wants []npmWant
	)

	for _, s := range []struct{ name, version string }{
		{"left-pad", "1.1.0"}, {"left-pad", "2.0.0-beta.1"}, {"left-pad", "1.0.0"},
		{"@acme/widget", "0.2.0-rc.2"}, {"@acme/widget", "0.2.0-rc.1"},
	} {
		var extra map[string]any
		if s.version == "1.0.0" {
			// A dist block packed into package.json would fail the integrity
			// check of the server; it belongs to the registry.
			extra = map[string]any{"dist": map[string]any{"integrity": "sha512-stale"}}
		}

		data := npmTestTarball(t, s.name, s.version, extra)
		files = append(files, fakeFile{
			path: s.name + "/-/" + s.name[strings.LastIndex(s.name, "/")+1:] + "-" + s.version + ".tgz",
			data: data, created: nexusStamp, contentType: "application/octet-stream",
		})
		wants = append(wants, npmWant{s.name, s.version, data})
	}

	files = append(files,
		// The packuments of Nexus carry the dist-tags; these two name no "latest",
		// which is then derived from the versions.
		fakeFile{path: "left-pad", data: []byte(`{"name":"left-pad","dist-tags":{"beta":"2.0.0-beta.1","legacy":"1.0.0"}}`), created: nexusStamp, contentType: "application/json"},
		fakeFile{path: "@acme/widget", data: []byte(`{"name":"@acme/widget","dist-tags":{"rc":"0.2.0-rc.1"}}`), created: nexusStamp, contentType: "application/json"},
	)

	return files, wants
}

func npmNewOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"reg": {format: "npm", repoType: "hosted"}})
}

func npmNexusServer(t *testing.T, files []fakeFile) *httptest.Server {
	t.Helper()

	return newFakeNexus(t, fakeSourceRepo{name: "npmsrc", format: "npm", kind: "hosted", files: files})
}

func npmNexusCfg(t *testing.T, srv *httptest.Server, extra ...string) importConfig {
	t.Helper()

	return parseCfg(t, append([]string{"nexus", "--source-url", srv.URL, "--repo", "npmsrc:reg"}, extra...)...)
}

func npmFind(oa *fakeOA, name, version string) *fakeItem {
	for _, it := range oa.repos["reg"].items {
		if it.name == name && it.version == version {
			return it
		}
	}

	return nil
}

// npmCheckPublished asserts every wanted version is in the destination with
// the bytes and creation time of the source.
func npmCheckPublished(t *testing.T, oa *fakeOA, wants []npmWant, created string) {
	t.Helper()

	if got := len(oa.repos["reg"].items); got != len(wants) {
		t.Fatalf("destination holds %d versions, want %d", got, len(wants))
	}

	for _, w := range wants {
		it := npmFind(oa, w.name, w.version)
		if it == nil {
			t.Fatalf("%s@%s was not published", w.name, w.version)
		}

		if it.sha != sum256(w.data) || it.size != len(w.data) {
			t.Errorf("%s@%s: stored sha %s size %d, want the source bytes", w.name, w.version, it.sha, it.size)
		}

		if it.created != created {
			t.Errorf("%s@%s: created-at header %q, want %q", w.name, w.version, it.created, created)
		}

		if it.path != npmFakeFile(w.name, w.version) {
			t.Errorf("%s@%s: file name %q", w.name, w.version, it.path)
		}
	}
}

// ─── import behavior ──────────────────────────────────────────────────────────

func TestImportNPMNexusPublishesInVersionOrderAndSetsLatest(t *testing.T) {
	files, wants := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.failed() || rep.Uploaded != 5 || rep.Ignored != 2 {
		t.Fatalf("report = %+v", rep)
	}

	npmCheckPublished(t, oa, wants, nexusWant)

	n := npmState(t, oa)

	if got, want := n.versions("left-pad"), []string{"1.0.0", "1.1.0", "2.0.0-beta.1"}; !slices.Equal(got, want) {
		t.Errorf("left-pad published as %v, want %v", got, want)
	}

	if got, want := n.versions("@acme/widget"), []string{"0.2.0-rc.1", "0.2.0-rc.2"}; !slices.Equal(got, want) {
		t.Errorf("@acme/widget published as %v, want %v", got, want)
	}

	// The prerelease sits above the last stable version but must not take "latest".
	if got, want := n.tags["reg/left-pad"], map[string]string{"latest": "1.1.0", "beta": "2.0.0-beta.1", "legacy": "1.0.0"}; !maps.Equal(got, want) {
		t.Errorf("left-pad tags = %v, want %v", got, want)
	}

	// A package with prereleases only ends at its highest one.
	if got, want := n.tags["reg/@acme/widget"], map[string]string{"latest": "0.2.0-rc.2", "rc": "0.2.0-rc.1"}; !maps.Equal(got, want) {
		t.Errorf("@acme/widget tags = %v, want %v", got, want)
	}

	manifest := n.manifests["reg/left-pad@1.0.0"]
	if manifest["description"] != "fixture left-pad" || manifest["main"] != "index.js" {
		t.Errorf("published manifest lost package.json fields: %v", manifest)
	}
}

func TestImportNPMLatestRespectsExistingTag(t *testing.T) {
	for _, tc := range []struct{ name, existing, want string }{
		{"a higher release already holds latest", "3.0.0", "3.0.0"},
		{"a lower release is moved forward", "0.5.0", "1.1.0"},
		{"a prerelease holding latest yields to a release", "9.0.0-rc.1", "1.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, _ := npmNexusFixtures(t)
			nexus := npmNexusServer(t, files)
			oa := npmNewOA(t)
			n := npmState(t, oa)
			n.seed("reg", "left-pad", tc.existing, []byte("older"), "latest")

			if rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa); rep.failed() {
				t.Fatalf("report = %+v", rep)
			}

			if got := n.tags["reg/left-pad"]["latest"]; got != tc.want {
				t.Fatalf("latest = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestImportNPMArtifactory(t *testing.T) {
	for _, mode := range []string{"aql", "storage"} {
		t.Run(mode, func(t *testing.T) {
			scoped := npmTestTarball(t, "@acme/widget", "1.0.0", nil)
			plain := npmTestTarball(t, "left-pad", "1.0.0", nil)
			arti := newFakeArtifactory(t, fakeSourceRepo{name: "npm-local", format: "npm", kind: "local", files: []fakeFile{
				{path: "left-pad/-/left-pad-1.0.0.tgz", data: plain, created: artiStamp, props: map[string][]string{"npm.name": {"left-pad"}, "team": {"x"}}},
				// Artifactory repeats the scope in front of a scoped tarball's file name.
				{path: "@acme/widget/-/@acme/widget-1.0.0.tgz", data: scoped, created: artiStamp, props: map[string][]string{"npm.name": {"@acme/widget"}}},
				{path: ".npm/left-pad/package.json", data: []byte(`{"name":"left-pad"}`), created: artiStamp},
				{path: ".npm/@acme/widget/package.json", data: []byte(`{"name":"@acme/widget"}`), created: artiStamp},
			}})
			oa := npmNewOA(t)

			cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc", "--repo", "npm-local:reg",
				"--artifactory-listing", mode, "--properties", "npm.*")
			cfg.SourceToken = "secret"

			rep := runImportTest(t, cfg, arti, oa)
			if rep.failed() || rep.Uploaded != 2 {
				t.Fatalf("report = %+v", rep)
			}

			npmCheckPublished(t, oa, []npmWant{{"left-pad", "1.0.0", plain}, {"@acme/widget", "1.0.0", scoped}}, artiWant)

			// Properties are attached to the package the publish created.
			if it := npmFind(oa, "@acme/widget", "1.0.0"); it.props["npm.name"] != "@acme/widget" {
				t.Errorf("props = %v", it.props)
			}

			if it := npmFind(oa, "left-pad", "1.0.0"); len(it.props) != 1 || it.props["npm.name"] != "left-pad" {
				t.Errorf("props = %v, want only the allow-listed npm.name", it.props)
			}
		})
	}
}

func TestImportNPMIsIdempotent(t *testing.T) {
	files, wants := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)
	cfg := npmNexusCfg(t, nexus)

	runImportTest(t, cfg, nexus, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 5 || oa.writes != writes {
		t.Fatalf("second run: %+v, writes+=%d, want 5 present and no writes", rep, oa.writes-writes)
	}

	npmCheckPublished(t, oa, wants, nexusWant)
}

func TestImportNPMWithoutSourceDigestsRerunSettlesIdenticalVersions(t *testing.T) {
	files, wants := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)
	cfg := npmNexusCfg(t, nexus)

	run := func() *importReport {
		imp, err := newImporter(cfg, noSHA256Source{newImportSource(cfg, nexus.Client())}, &importTarget{c: oa.client()}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}

		defer imp.state.close()

		return imp.run(context.Background())
	}

	if rep := run(); rep.failed() || rep.Uploaded != 5 {
		t.Fatalf("first run: %+v", rep)
	}

	// Nexus npm repositories report no SHA-256, so nothing can be recognized as
	// present up front: every publish is answered with 409 and must be accepted
	// because the published tarball is the same one.
	rep := run()
	if rep.failed() || rep.Uploaded != 5 {
		t.Fatalf("second run: %+v", rep)
	}

	if n := npmState(t, oa); n.conflicts != 5 || len(n.published) != 5 {
		t.Fatalf("conflicts=%d published=%d, want 5 conflicts and no new version", n.conflicts, len(n.published))
	}

	npmCheckPublished(t, oa, wants, nexusWant)
}

// npmTarballs drops everything but the tarballs from a fixture: no packuments, so
// no dist-tags to apply.
func npmTarballs(files []fakeFile) []fakeFile {
	return slices.DeleteFunc(slices.Clone(files), func(f fakeFile) bool { return f.contentType == "application/json" })
}

func TestImportNPMResumesFromStateFile(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, npmTarballs(files))
	oa := npmNewOA(t)
	state := filepath.Join(t.TempDir(), "state.jsonl")
	cfg := npmNexusCfg(t, nexus, "--state-file", state)

	if rep := runImportTest(t, cfg, nexus, oa); rep.Uploaded != 5 {
		t.Fatalf("first run uploaded %d", rep.Uploaded)
	}

	oa.repos["reg"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, nexus, oa)
	if rep.Resumed != 5 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}

	raw, err := os.ReadFile(state)
	if err != nil || strings.Count(string(raw), "\n") != 5 {
		t.Fatalf("state file = %q, %v", raw, err)
	}
}

func TestImportNPMDryRunWritesNothing(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus, "--dry-run"), nexus, oa)
	if rep.failed() || rep.Uploaded != 5 || !rep.DryRun {
		t.Fatalf("report = %+v", rep)
	}

	if oa.writes != 0 || len(oa.repos["reg"].items) != 0 {
		t.Fatalf("dry run wrote: writes=%d items=%d", oa.writes, len(oa.repos["reg"].items))
	}
}

func TestImportNPMSourceChecksumMismatchRemovesCopy(t *testing.T) {
	files, wants := npmNexusFixtures(t)
	files[0].sha256 = sum256([]byte("what the source claimed")) // left-pad@1.1.0
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.Uploaded != 4 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep)
	}

	if f := rep.Failures[0]; !strings.Contains(f.Error, "checksum mismatch") || !strings.HasSuffix(f.Path, "left-pad-1.1.0.tgz") {
		t.Fatalf("failure = %+v", f)
	}

	if got := npmState(t, oa).unpublished; !slices.Equal(got, []string{"left-pad@1.1.0"}) || npmFind(oa, "left-pad", "1.1.0") != nil {
		t.Fatalf("unpublished %v: the bad copy was not removed", got)
	}

	npmCheckPublished(t, oa, slices.DeleteFunc(slices.Clone(wants), func(w npmWant) bool { return w.version == "1.1.0" }), nexusWant)
}

func TestImportNPMStoredDigestMismatchRemovesCopies(t *testing.T) {
	files, _ := npmNexusFixtures(t)
	nexus := npmNexusServer(t, npmTarballs(files))
	oa := npmNewOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.Failed != 5 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}

	// The scoped package's cleanup also proves the escaped name in the DELETE.
	if n := npmState(t, oa); len(n.unpublished) != 5 || len(oa.repos["reg"].items) != 0 {
		t.Fatalf("unpublished %v, %d versions left", n.unpublished, len(oa.repos["reg"].items))
	}
}

func TestImportNPMConflictWithDifferentContentIsReported(t *testing.T) {
	files, wants := npmNexusFixtures(t)
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)
	seeded := npmState(t, oa).seed("reg", "left-pad", "1.0.0", []byte("someone else's build"), "latest")

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.Uploaded != 4 || rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "different content") {
		t.Fatalf("report = %+v", rep)
	}

	if oa.deletes != 0 || seeded.sha != sum256([]byte("someone else's build")) || npmFind(oa, "left-pad", "1.0.0") != seeded {
		t.Fatalf("the existing version was touched (deletes=%d)", oa.deletes)
	}

	// Everything else was published; the conflicting version is still the seeded one.
	others := slices.DeleteFunc(slices.Clone(wants), func(w npmWant) bool { return w.name == "left-pad" && w.version == "1.0.0" })

	if got := len(oa.repos["reg"].items); got != len(others)+1 {
		t.Fatalf("destination holds %d versions, want %d", got, len(others)+1)
	}

	for _, w := range others {
		if it := npmFind(oa, w.name, w.version); it == nil || it.sha != sum256(w.data) {
			t.Errorf("%s@%s was not published intact", w.name, w.version)
		}
	}
}

func TestImportNPMRejectsTarballThatContradictsItsPath(t *testing.T) {
	files := []fakeFile{{
		path: "left-pad/-/left-pad-1.0.0.tgz", data: npmTestTarball(t, "left-pad", "9.9.9", nil), created: nexusStamp,
		contentType: "application/octet-stream",
	}}
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "declares left-pad@9.9.9") {
		t.Fatalf("report = %+v", rep)
	}

	if oa.writes != 0 {
		t.Fatalf("a tarball under foreign coordinates was published (%d writes)", oa.writes)
	}
}

func TestImportNPMReportsTarballWithoutManifest(t *testing.T) {
	files := []fakeFile{{path: "left-pad/-/left-pad-1.0.0.tgz", data: []byte("not a tarball"), created: nexusStamp}}
	nexus := npmNexusServer(t, files)
	oa := npmNewOA(t)

	rep := runImportTest(t, npmNexusCfg(t, nexus), nexus, oa)
	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "cannot read package.json") || oa.writes != 0 {
		t.Fatalf("report = %+v, writes=%d", rep, oa.writes)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func TestImportNPMCoordinates(t *testing.T) {
	for _, tc := range []struct {
		path, name, version string
		ok                  bool
	}{
		{"left-pad/-/left-pad-1.0.0.tgz", "left-pad", "1.0.0", true},
		{"left-pad/-/left-pad-2.0.0-beta.1+build.5.tgz", "left-pad", "2.0.0-beta.1+build.5", true},
		{"@acme/widget/-/widget-0.2.0.tgz", "@acme/widget", "0.2.0", true},
		{"@acme/widget/-/@acme/widget-0.2.0.tgz", "@acme/widget", "0.2.0", true},
		{"a-b/-/a-b-1.0.0.tgz", "a-b", "1.0.0", true},
		{"left-pad", "", "", false},
		{"@acme/widget", "", "", false},
		{".npm/left-pad/package.json", "", "", false},
		{"left-pad/-/left-pad-1.0.0.tar.gz", "", "", false},
		{"left-pad/-/other-1.0.0.tgz", "", "", false},
		{"left-pad/-/left-pad-.tgz", "", "", false},
		{"left-pad/-/@acme/left-pad-1.0.0.tgz", "", "", false},
		{"@acme/widget/-/@other/widget-1.0.0.tgz", "", "", false},
		{"@/widget/-/widget-1.0.0.tgz", "", "", false},
		{"acme/widget/-/widget-1.0.0.tgz", "", "", false},
		{"/-/x-1.0.0.tgz", "", "", false},
	} {
		name, version, ok := npmCoordinates(tc.path)
		if name != tc.name || version != tc.version || ok != tc.ok {
			t.Errorf("npmCoordinates(%q) = %q %q %v, want %q %q %v", tc.path, name, version, ok, tc.name, tc.version, tc.ok)
		}

		if importableNPM(tc.path) != tc.ok {
			t.Errorf("importableNPM(%q) = %v", tc.path, !tc.ok)
		}
	}
}

func TestImportNPMVersionPrecedence(t *testing.T) {
	ordered := []string{
		"0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11",
		"1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0",
	}

	for i := range ordered {
		for j := range ordered {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}

			if got := npmCompareVersions(ordered[i], ordered[j]); got != want {
				t.Errorf("npmCompareVersions(%q, %q) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}

	if npmCompareVersions("1.0.0+a", "1.0.0+b") == 0 {
		t.Error("versions differing in build metadata must still order deterministically")
	}

	if npmCompareVersions("not-semver", "0.0.1") >= 0 {
		t.Error("text that is no version must sort first")
	}
}

func TestImportNPMUnitsSortVersionsPerPackage(t *testing.T) {
	var in []sourceAsset

	for _, p := range []string{
		"b/-/b-1.10.0.tgz", "a/-/a-2.0.0.tgz", "b/-/b-1.2.0.tgz", "a/-/a-2.0.0-rc.1.tgz", "@s/c/-/c-1.0.0.tgz",
	} {
		in = append(in, sourceAsset{Path: p})
	}

	var got [][]string

	for _, unit := range npmUnits(in) {
		var paths []string
		for _, a := range unit {
			paths = append(paths, a.Path)
		}

		got = append(got, paths)
	}

	want := [][]string{
		{"b/-/b-1.2.0.tgz", "b/-/b-1.10.0.tgz"},
		{"a/-/a-2.0.0-rc.1.tgz", "a/-/a-2.0.0.tgz"},
		{"@s/c/-/c-1.0.0.tgz"},
	}

	if len(got) != len(want) {
		t.Fatalf("units = %v, want %v", got, want)
	}

	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("unit %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestImportNPMReplacesLatest(t *testing.T) {
	for _, tc := range []struct {
		version, current string
		want             bool
	}{
		{"1.0.0", "", true},
		{"1.0.0-rc.1", "", true},
		{"1.1.0", "1.0.0", true},
		{"1.0.0", "1.1.0", false},
		{"1.0.0", "1.0.0", false},
		{"2.0.0-beta.1", "1.1.0", false},
		{"1.0.0", "2.0.0-beta.1", true},
		{"2.0.0-beta.2", "2.0.0-beta.1", true},
		{"2.0.0-beta.1", "2.0.0-beta.2", false},
		{"1.0.0", "garbage", true},
		{"garbage", "1.0.0", false},
	} {
		if got := npmReplacesLatest(tc.version, tc.current); got != tc.want {
			t.Errorf("npmReplacesLatest(%q, %q) = %v, want %v", tc.version, tc.current, got, tc.want)
		}
	}
}

func TestImportNPMReadManifest(t *testing.T) {
	good := npmTestTarball(t, "left-pad", "1.0.0", nil)

	manifest, err := npmReadManifest(bytes.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}

	if name, version, err := npmManifestIdentity(manifest); err != nil || name != "left-pad" || version != "1.0.0" {
		t.Fatalf("identity = %q %q %v", name, version, err)
	}

	pack := func(entries ...[2]string) []byte {
		var buf bytes.Buffer

		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)

		for _, e := range entries {
			if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1])), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}

			_, _ = tw.Write([]byte(e[1]))
		}

		_ = tw.Close()
		_ = zw.Close()

		return buf.Bytes()
	}

	for _, tc := range []struct {
		name string
		data []byte
		want string // substring of the error; empty: success with version "7.0.0"
	}{
		{"another top-level directory name", pack([2]string{"pkg-src/package.json", `{"name":"x","version":"7.0.0"}`}), ""},
		{"byte order mark", pack([2]string{"package/package.json", "\xef\xbb\xbf" + `{"name":"x","version":"7.0.0"}`}), ""},
		{"nested manifests are not the package's", pack([2]string{"package/node_modules/y/package.json", `{"name":"y","version":"1.0.0"}`}), "the archive has none"},
		{"no gzip", []byte("plain"), "cannot read package.json"},
		{"invalid json", pack([2]string{"package/package.json", "{"}), "cannot read package.json"},
		{"first manifest wins", pack(
			[2]string{"package/package.json", `{"name":"x","version":"7.0.0"}`},
			[2]string{"other/package.json", `{"name":"y","version":"1.0.0"}`}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := npmReadManifest(bytes.NewReader(tc.data))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, errNPMManifest) {
					t.Fatalf("err = %v, want %q", err, tc.want)
				}

				return
			}

			if _, version, err := npmManifestIdentity(m); err != nil || version != "7.0.0" {
				t.Fatalf("version = %q, %v", version, err)
			}
		})
	}
}

func TestImportNPMPublishDocument(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 4, 100, 4097} {
		data := bytes.Repeat([]byte{0xfe, 0x00, 'x'}, size)[:size]
		manifest := []byte(`{"name":"@a/b","version":"1.0.0-rc.1","description":"a <b> & \"c\""}`)

		for _, latest := range []bool{true, false} {
			doc, err := newNPMPublish("@a/b", "1.0.0-rc.1", manifest, latest, bytes.NewReader(data), int64(size))
			if err != nil {
				t.Fatal(err)
			}

			body := doc.open()

			raw, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}

			_ = body.Close()

			if int64(len(raw)) != doc.length() {
				t.Fatalf("size %d: body is %d bytes, length() says %d", size, len(raw), doc.length())
			}

			var parsed npmFakeDoc
			if err := json.Unmarshal(raw, &parsed); err != nil {
				t.Fatalf("size %d: %v", size, err)
			}

			version, tarball, problem := parsed.check("@a/b")
			if problem != "" || version != "1.0.0-rc.1" || !bytes.Equal(tarball, data) {
				t.Fatalf("size %d: version %q problem %q, tarball intact: %v", size, version, problem, bytes.Equal(tarball, data))
			}

			if tags := parsed.DistTags; latest != (tags["latest"] == "1.0.0-rc.1") || (!latest && len(tags) != 0) {
				t.Fatalf("dist-tags = %v, latest=%v", tags, latest)
			}
		}
	}
}

func TestImportNPMRefusesTarballBeyondPublishLimit(t *testing.T) {
	// A tarball grows by a third as base64, so the 200 MiB publish limit leaves
	// room for a little under 150 MiB.
	const size = 149 << 20

	fits, err := newNPMPublish("a", "1.0.0", []byte(`{}`), true, nil, size)
	if err != nil {
		t.Fatal(err)
	}

	over, err := newNPMPublish("a", "1.0.0", []byte(`{}`), true, nil, size+(2<<20))
	if err != nil {
		t.Fatal(err)
	}

	if fits.length() > npmMaxPublishBody || over.length() <= npmMaxPublishBody {
		t.Fatalf("lengths %d and %d around the %d byte publish limit", fits.length(), over.length(), npmMaxPublishBody)
	}
}
