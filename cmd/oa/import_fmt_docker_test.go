// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const dockerDst = "reg"

// dockerEnv is a source registry, a destination and the importer's config.
type dockerEnv struct {
	t     *testing.T
	fx    *dockerFixture
	oa    *fakeOA
	fk    *fakeDocker
	reg   *dockerRegistry // set when the source is Artifactory
	src   *httptest.Server
	cfg   importConfig
	files []fakeFile
}

func newDockerOA(t *testing.T) (*fakeOA, *fakeDocker) {
	t.Helper()

	oa := newFakeOA(t, map[string]*fakeRepo{dockerDst: {format: "docker", repoType: "hosted"}})

	return oa, fakeDockerOf(oa)
}

// newDockerNexusEnv serves the fixture from a Nexus that, like the real one,
// exposes the registry API as plain files below /repository/<repo>/v2/….
func newDockerNexusEnv(t *testing.T, fx *dockerFixture, mutate func([]fakeFile), args ...string) *dockerEnv {
	t.Helper()

	files := fx.nexusFiles(nexusStamp)
	if mutate != nil {
		mutate(files)
	}

	nexus := newFakeNexus(t, fakeSourceRepo{name: "dock", format: "docker", kind: "hosted", files: files})
	oa, fk := newDockerOA(t)
	cfg := parseCfg(t, append([]string{"nexus", "--source-url", nexus.URL, "--repo", "dock:" + dockerDst}, args...)...)

	return &dockerEnv{t: t, fx: fx, oa: oa, fk: fk, src: nexus, cfg: cfg, files: files}
}

// newDockerArtifactoryEnv serves the fixture from an Artifactory with a real
// registry API (content types, digest headers, Accept handling).
func newDockerArtifactoryEnv(t *testing.T, fx *dockerFixture, props map[string]map[string][]string, args ...string) *dockerEnv {
	t.Helper()

	files := fx.artifactoryFiles(artiStamp, props)
	reg := newDockerRegistry(fx)
	src := newDockerArtifactory(t, reg, fakeSourceRepo{name: "docker-local", format: "docker", kind: "local", files: files})
	oa, fk := newDockerOA(t)

	cfg := parseCfg(t, append([]string{"artifactory", "--source-url", src.URL, "--source-user", "svc", "--repo", "docker-local:" + dockerDst}, args...)...)
	cfg.SourceToken = "secret"

	return &dockerEnv{t: t, fx: fx, oa: oa, fk: fk, reg: reg, src: src, cfg: cfg, files: files}
}

func (e *dockerEnv) run() *importReport {
	e.t.Helper()

	return runImportTest(e.t, e.cfg, e.src, e.oa)
}

// requireClean fails the test unless the run had no failure and the
// destination received no request it does not know.
func (e *dockerEnv) requireClean(rep *importReport) {
	e.t.Helper()

	if rep.failed() {
		e.t.Fatalf("unexpected failures: %+v", rep.Failures)
	}

	if len(e.fk.unexpected) > 0 {
		e.t.Fatalf("the destination got requests it does not serve: %v", e.fk.unexpected)
	}
}

// requireImported checks that the tag is in the destination with the source's
// digests and bytes: the tag, the child manifests and every blob.
func (e *dockerEnv) requireImported(tag dockerFakeTag) {
	e.t.Helper()

	img := e.fk.images[dockerDst+"/"+tag.image]
	if img == nil {
		e.t.Fatalf("image %s was not created", tag.image)
	}

	if got := img.tags[tag.tag]; got != tag.img.digest {
		e.t.Fatalf("%s:%s points at %q, want %s", tag.image, tag.tag, got, tag.img.digest)
	}

	for _, m := range tag.img.allManifests() {
		if !bytes.Equal(img.manifests[m], e.fx.manifests[m]) {
			e.t.Fatalf("%s: manifest %s differs from the source", tag.image, m)
		}
	}

	for _, b := range tag.img.blobs {
		if !bytes.Equal(img.blobs[b], e.fx.blobs[b]) {
			e.t.Fatalf("%s: blob %s differs from the source", tag.image, b)
		}
	}

	var listed *fakeItem

	for _, it := range e.oa.repos[dockerDst].items {
		if it.name == dockerDst+"/"+tag.image && it.version == tag.tag {
			listed = it
		}
	}

	if listed == nil || listed.path != tag.img.digest {
		e.t.Fatalf("%s:%s is not listed at %s: %+v", tag.image, tag.tag, tag.img.digest, listed)
	}
}

func (e *dockerEnv) requireAllImported() {
	e.t.Helper()

	for _, tag := range e.fx.tags {
		e.requireImported(tag)
	}
}

// plannedBlobBytes is what a run sends: every distinct blob of every image once.
func (fx *dockerFixture) plannedBlobBytes() int64 {
	seen := map[string]bool{}

	var n int64

	for _, t := range slices.Concat(fx.tags, fx.loose) {
		for _, b := range t.img.blobs {
			if k := t.image + "@" + b; !seen[k] {
				seen[k] = true
				n += int64(len(fx.blobs[b]))
			}
		}
	}

	return n
}

// dockerSampleFixture is a repository with a shared base layer between two
// tags of one image, an empty layer, and a multi-platform image.
func dockerSampleFixture() *dockerFixture {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config-app-1", "base layer", "app 1.0 layer"))
	fx.tag("team/app", "2.0", fx.image("config-app-2", "base layer", "app 2.0 layer", ""))
	fx.tag("tool", "latest", fx.index(
		fx.image("config-tool-amd64", "tool amd64 layer"),
		fx.image("config-tool-arm64", "tool arm64 layer"),
	))

	return fx
}

func TestImportDockerNexusCopiesTags(t *testing.T) {
	fx := dockerSampleFixture()
	env := newDockerNexusEnv(t, fx, nil)

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 3 || rep.Present != 0 {
		t.Fatalf("uploaded=%d present=%d, want 3 and 0", rep.Uploaded, rep.Present)
	}

	if want := len(env.files) - 3; rep.Ignored != want {
		t.Fatalf("ignored=%d, want %d (digest manifests and blobs are reached through the tags)", rep.Ignored, want)
	}

	env.requireAllImported()

	if want := fx.plannedBlobBytes(); rep.Bytes != want {
		t.Fatalf("bytes=%d, want %d", rep.Bytes, want)
	}

	// The registry never sees a blob twice for one image, however many tags use it.
	for key, n := range env.fk.blobPut {
		if n != 1 {
			t.Errorf("%s was uploaded %d times", key, n)
		}
	}

	// Every manifest carries the creation time of the source.
	if len(env.fk.puts) == 0 {
		t.Fatal("no manifest was pushed")
	}

	for _, p := range env.fk.puts {
		if p.created != nexusWant {
			t.Errorf("manifest %s %s: created-at %q, want %q", p.name, p.ref, p.created, nexusWant)
		}

		if !isOCIManifestType(p.contentType) {
			t.Errorf("manifest %s %s: content type %q", p.name, p.ref, p.contentType)
		}
	}
}

func TestImportDockerPushesChildrenBeforeIndexAndTagLast(t *testing.T) {
	fx := newDockerFixture()
	amd, arm := fx.image("config-amd64", "amd64 layer"), fx.image("config-arm64", "arm64 layer")
	idx := fx.index(amd, arm)
	fx.tag("tool", "latest", idx)

	env := newDockerNexusEnv(t, fx, nil, "--concurrency", "1")
	env.requireClean(env.run())

	pos := func(prefix string) int {
		return slices.IndexFunc(env.fk.events, func(e string) bool { return strings.HasPrefix(e, prefix) })
	}

	firstManifest := pos("manifest ")

	for _, b := range idx.blobs {
		if i := pos("blob reg/tool@" + b); i < 0 || i > firstManifest {
			t.Errorf("blob %s was pushed at %d, after the first manifest at %d", shortDigest(b), i, firstManifest)
		}
	}

	tail := env.fk.events[firstManifest:]
	want := []string{
		"manifest reg/tool " + amd.digest,
		"manifest reg/tool " + arm.digest,
		"manifest reg/tool latest",
	}

	if !slices.Equal(tail, want) {
		t.Fatalf("manifest order = %v, want %v", tail, want)
	}
}

func TestImportDockerArtifactoryCopiesTagsAndProperties(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config-app", "app layer"))
	fx.tag("team/multi", "latest", fx.index(fx.image("config-a", "layer a"), fx.image("config-b", "layer b")))

	props := map[string]map[string][]string{
		"team/app:1.0": {"build.name": {"ci"}, "build.number": {"7"}, "docker.manifest": {"1.0"}},
	}
	env := newDockerArtifactoryEnv(t, fx, props)

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 2 {
		t.Fatalf("uploaded=%d, want 2 (a multi-arch tag is one transfer, not two files)", rep.Uploaded)
	}

	// manifest.json and list.manifest.json of the index, manifest.json of the app.
	if want := len(env.files) - 3; rep.Ignored != want {
		t.Fatalf("ignored=%d, want %d (digest folders, blobs and uploads)", rep.Ignored, want)
	}

	env.requireAllImported()

	for _, p := range env.fk.puts {
		if p.created != artiWant {
			t.Errorf("manifest %s %s: created-at %q, want %q", p.name, p.ref, p.created, artiWant)
		}
	}

	var app *fakeItem

	for _, it := range env.oa.repos[dockerDst].items {
		if it.name == dockerDst+"/team/app" {
			app = it
		}
	}

	if app == nil || len(app.props) != 2 || app.props["build.name"] != "ci" || app.props["build.number"] != "7" {
		t.Fatalf("properties = %+v, want build.name and build.number only", app)
	}

	for _, accept := range env.reg.accept {
		for _, mt := range ociManifestTypes {
			if !strings.Contains(accept, mt) {
				t.Fatalf("manifest request asked for %q, which lacks %s", accept, mt)
			}
		}
	}

	if len(env.reg.accept) == 0 {
		t.Fatal("the source registry was never asked for a manifest")
	}
}

func TestImportDockerIsIdempotent(t *testing.T) {
	env := newDockerNexusEnv(t, dockerSampleFixture(), nil)

	env.requireClean(env.run())

	writes := env.oa.writes
	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 0 || rep.Present != 3 {
		t.Fatalf("second run uploaded=%d present=%d, want 0 and 3", rep.Uploaded, rep.Present)
	}

	if env.oa.writes != writes {
		t.Fatalf("second run wrote %d times", env.oa.writes-writes)
	}
}

func TestImportDockerRecognisesPresentTagsWithoutSourceChecksums(t *testing.T) {
	zero := strings.Repeat("0", 64)
	env := newDockerNexusEnv(t, dockerSampleFixture(), func(files []fakeFile) {
		for i := range files {
			files[i].sha256 = zero
		}
	})

	env.requireClean(env.run())

	writes := env.oa.writes
	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 0 || rep.Present != 3 || env.oa.writes != writes {
		t.Fatalf("uploaded=%d present=%d writes+=%d, want present only", rep.Uploaded, rep.Present, env.oa.writes-writes)
	}
}

func TestImportDockerMovesTagWhenSourceContentDiffers(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config-new", "new layer"))
	env := newDockerNexusEnv(t, fx, nil)

	// The destination has the tag at older content.
	old := fx.image("config-old", "old layer")
	img := env.fk.image(dockerDst + "/team/app")
	img.tags["1.0"] = old.digest
	env.oa.repos[dockerDst].items = append(env.oa.repos[dockerDst].items, &fakeItem{
		id: "old", name: dockerDst + "/team/app", version: "1.0", path: old.digest, sha: strings.TrimPrefix(old.digest, ociDigestPrefix),
	})

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 1 || rep.Present != 0 {
		t.Fatalf("uploaded=%d present=%d, want 1 and 0", rep.Uploaded, rep.Present)
	}

	env.requireAllImported()
}

func TestImportDockerReportsTagConflict(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config-new", "new layer"))
	env := newDockerNexusEnv(t, fx, nil)
	env.fk.immutable = true

	old := fx.image("config-old", "old layer")
	img := env.fk.image(dockerDst + "/team/app")
	img.tags["1.0"] = old.digest
	env.oa.repos[dockerDst].items = append(env.oa.repos[dockerDst].items, &fakeItem{
		id: "old", name: dockerDst + "/team/app", version: "1.0", path: old.digest, sha: strings.TrimPrefix(old.digest, ociDigestPrefix),
	})

	rep := env.run()

	if len(rep.Failures) != 1 || rep.Failures[0].Path != "team/app:1.0" || !strings.Contains(rep.Failures[0].Error, errUploadConflict.Error()) {
		t.Fatalf("failures = %+v, want one conflict for team/app:1.0", rep.Failures)
	}

	if rep.Uploaded != 0 || img.tags["1.0"] != old.digest {
		t.Fatalf("uploaded=%d, tag=%s: a conflicting tag must be left alone", rep.Uploaded, img.tags["1.0"])
	}
}

func TestImportDockerResumesFromStateFile(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.jsonl")
	env := newDockerNexusEnv(t, dockerSampleFixture(), nil, "--state-file", state)

	if rep := env.run(); rep.Uploaded != 3 {
		t.Fatalf("first run uploaded %d, failures %+v", rep.Uploaded, rep.Failures)
	}

	// Forget the tags: only the state file knows they were done.
	env.oa.repos[dockerDst].items = nil
	writes := env.oa.writes

	rep := env.run()
	if rep.Resumed != 3 || rep.Uploaded != 0 || env.oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, env.oa.writes-writes)
	}

	raw, err := os.ReadFile(state)
	if err != nil || strings.Count(string(raw), "\n") != 3 {
		t.Fatalf("state file = %q, %v", raw, err)
	}
}

func TestImportDockerDryRunWritesNothing(t *testing.T) {
	fx := dockerSampleFixture()
	env := newDockerNexusEnv(t, fx, nil, "--dry-run")

	rep := env.run()
	env.requireClean(rep)

	if env.oa.writes != 0 || len(env.fk.images) != 0 || len(env.oa.repos[dockerDst].items) != 0 {
		t.Fatalf("a dry run wrote: writes=%d images=%d items=%d", env.oa.writes, len(env.fk.images), len(env.oa.repos[dockerDst].items))
	}

	if rep.Uploaded != 3 {
		t.Fatalf("planned %d tags, want 3", rep.Uploaded)
	}

	if want := fx.plannedBlobBytes(); rep.Bytes != want {
		t.Fatalf("planned bytes = %d, want %d (a blob shared by two tags counts once)", rep.Bytes, want)
	}
}

func TestImportDockerCorruptBlobFailsOnlyItsTag(t *testing.T) {
	fx := newDockerFixture()
	good := fx.image("config-good", "good layer")
	broken := fx.image("config-broken", "broken layer bytes")
	fx.tag("team/good", "1.0", good)
	fx.tag("team/broken", "1.0", broken)

	brokenLayer := broken.blobs[1]
	env := newDockerNexusEnv(t, fx, func(files []fakeFile) {
		for i, f := range files {
			if strings.HasSuffix(f.path, "/blobs/"+brokenLayer) {
				files[i].data = bytes.Clone(f.data)
				files[i].data[0] ^= 0xff // same length, other content
			}
		}
	})

	rep := env.run()

	if rep.Uploaded != 1 || len(rep.Failures) != 1 {
		t.Fatalf("uploaded=%d failures=%+v, want the good tag only", rep.Uploaded, rep.Failures)
	}

	f := rep.Failures[0]
	if f.Path != "team/broken:1.0" || !strings.Contains(f.Error, "the source sent content of") || !strings.Contains(f.Error, shortDigest(brokenLayer)) {
		t.Fatalf("failure = %+v, want the corrupt blob of team/broken:1.0 named", f)
	}

	env.requireImported(fx.tags[0])

	if img := env.fk.images[dockerDst+"/team/broken"]; img != nil && (len(img.manifests) > 0 || len(img.tags) > 0) {
		t.Fatalf("a manifest was pushed although a blob was corrupt: %+v", img)
	}

	for _, e := range env.fk.events {
		if strings.HasPrefix(e, "manifest reg/team/broken") {
			t.Fatalf("event %q after a corrupt blob", e)
		}
	}

	if len(env.fk.uploads) != 0 {
		t.Fatalf("%d upload session(s) left open", len(env.fk.uploads))
	}
}

func TestImportDockerMountsBlobsHeldByAnotherImage(t *testing.T) {
	fx := newDockerFixture()
	a := fx.image("config-a", "shared base", "a only")
	b := fx.image("config-b", "shared base", "b only")
	fx.tag("team/a", "1", a)
	fx.tag("team/b", "1", b)

	env := newDockerNexusEnv(t, fx, nil, "--concurrency", "1")

	rep := env.run()
	env.requireClean(rep)
	env.requireAllImported()

	shared := a.blobs[1]
	if env.fk.blobPut[dockerDst+"/team/b@"+shared] != 0 {
		t.Fatalf("the shared layer was uploaded again for team/b")
	}

	mount := fmt.Sprintf("mount %s/team/b@%s from %s/team/a", dockerDst, shared, dockerDst)
	if !slices.Contains(env.fk.events, mount) {
		t.Fatalf("no mount of the shared layer; events = %v", env.fk.events)
	}

	// Mounted bytes did not travel.
	if want := fx.plannedBlobBytes() - int64(len(fx.blobs[shared])); rep.Bytes != want {
		t.Fatalf("bytes=%d, want %d", rep.Bytes, want)
	}
}

func TestImportDockerFetchesAndUploadsSharedBlobOnce(t *testing.T) {
	fx := newDockerFixture()

	const tags = 8

	for i := range tags {
		fx.tag("team/app", fmt.Sprintf("v%d", i), fx.image(fmt.Sprintf("config %d", i), "the shared base layer", fmt.Sprintf("layer %d", i)))
	}

	env := newDockerArtifactoryEnv(t, fx, nil, "--concurrency", "8")

	rep := env.run()
	env.requireClean(rep)
	env.requireAllImported()

	if rep.Uploaded != tags {
		t.Fatalf("uploaded=%d, want %d", rep.Uploaded, tags)
	}

	shared := ociDigestOf([]byte("the shared base layer"))

	if n := env.fk.blobPut[dockerDst+"/team/app@"+shared]; n != 1 {
		t.Fatalf("the shared layer was uploaded %d times, want 1", n)
	}

	if n := env.reg.blobGets(shared); n != 1 {
		t.Fatalf("the shared layer was fetched from the source %d times, want 1", n)
	}
}

func TestImportDockerSkipsForeignLayers(t *testing.T) {
	fx := newDockerFixture()
	foreign := ociDescriptor{
		MediaType: "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip",
		Digest:    ociDigestOf([]byte("windows base layer")), Size: 18,
		URLs: []string{"https://mcr.example/layer"},
	}
	img := fx.imageWith("config-win", foreign, fx.blob("own layer"))
	fx.tag("win/app", "1.0", img)

	env := newDockerNexusEnv(t, fx, nil)

	rep := env.run()
	env.requireClean(rep)

	if _, ok := env.fk.images[dockerDst+"/win/app"].blobs[foreign.Digest]; ok {
		t.Fatal("a foreign layer was pushed")
	}

	env.requireAllImported()
}

func TestImportDockerRejectsSchema1Manifests(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/ok", "1.0", fx.image("config", "layer"))

	legacy := []byte(`{"schemaVersion":1,"name":"team/legacy","tag":"1.0","fsLayers":[{"blobSum":"sha256:` + strings.Repeat("a", 64) + `"}]}`)
	digest := ociDigestOf(legacy)
	fx.manifests[digest] = legacy
	fx.tag("team/legacy", "1.0", dockerFakeImage{digest: digest, manifest: legacy})

	env := newDockerNexusEnv(t, fx, nil)

	rep := env.run()

	if rep.Uploaded != 1 || len(rep.Failures) != 1 || rep.Failures[0].Path != "team/legacy:1.0" || !strings.Contains(rep.Failures[0].Error, "schema 1") {
		t.Fatalf("uploaded=%d failures=%+v, want the schema 1 tag refused", rep.Uploaded, rep.Failures)
	}

	env.requireImported(fx.tags[0])
}

func TestImportDockerRemovesManifestStoredWithOtherDigest(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config", "layer"))

	env := newDockerNexusEnv(t, fx, nil)
	env.fk.mangle = true

	rep := env.run()

	if len(rep.Failures) != 1 || !strings.Contains(rep.Failures[0].Error, "was stored as") || !strings.Contains(rep.Failures[0].Error, "bad copy was removed") {
		t.Fatalf("failures = %+v, want a digest mismatch with the copy removed", rep.Failures)
	}

	if len(env.fk.deletedManifest) != 1 || env.oa.deletes != 1 {
		t.Fatalf("deleted = %v (%d deletes), want the stored manifest removed", env.fk.deletedManifest, env.oa.deletes)
	}

	if len(env.oa.repos[dockerDst].items) != 0 {
		t.Fatalf("the bad copy is still listed: %+v", env.oa.repos[dockerDst].items)
	}
}

func TestImportDockerRetriesTransientBlobFailure(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config", "layer"))

	env := newDockerNexusEnv(t, fx, nil)
	env.fk.failBlobPuts = 2

	sleeps := &fakeSleeper{}
	sleeps.apply(env.cfg.retrier)

	rep := env.run()
	env.requireClean(rep)
	env.requireAllImported()

	if rep.Uploaded != 1 || len(sleeps.recorded()) != 2 {
		t.Fatalf("uploaded=%d retries=%d, want 1 and 2", rep.Uploaded, len(sleeps.recorded()))
	}

	if len(env.fk.uploads) != 0 {
		t.Fatalf("%d abandoned upload session(s) were not cancelled", len(env.fk.uploads))
	}
}

func TestImportDockerIgnoresTheHostOfUploadLocations(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config", "layer"))

	env := newDockerNexusEnv(t, fx, nil)
	env.fk.foreignLocation = true

	rep := env.run()
	env.requireClean(rep)
	env.requireAllImported()
}

// dockerPlainSource hides the registry API of the source it wraps.
type dockerPlainSource struct{ importSource }

func TestImportDockerNeedsARegistryAPI(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config", "layer"))

	env := newDockerNexusEnv(t, fx, nil)

	imp, err := newImporter(env.cfg, dockerPlainSource{newImportSource(env.cfg, env.src.Client())}, &importTarget{c: env.oa.client()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(imp.state.close)

	rep := imp.run(context.Background())

	if len(rep.Failures) != 1 || !strings.Contains(rep.Failures[0].Error, errOCINoAPI.Error()) {
		t.Fatalf("failures = %+v, want %q", rep.Failures, errOCINoAPI)
	}
}

func TestImportDockerParsePath(t *testing.T) {
	cases := []struct {
		path      string
		image     string
		tag       string
		wantMatch bool
	}{
		{"v2/library/alpine/manifests/3.20", "library/alpine", "3.20", true},
		{"v2/app/manifests/latest", "app", "latest", true},
		{"v2/a/manifests/b/manifests/1.0", "a/manifests/b", "1.0", true},
		{"v2/smoke/hello/manifests/sha256:" + strings.Repeat("a", 64), "", "", false},
		{"v2/-/blobs/sha256:" + strings.Repeat("a", 64), "", "", false},
		{"v2/manifests/latest", "", "", false},
		{"library/alpine/3.20/manifest.json", "library/alpine", "3.20", true},
		{"library/alpine/3.20/list.manifest.json", "library/alpine", "3.20", true},
		{"v2/odd/1.0/manifest.json", "v2/odd", "1.0", true},
		{"library/alpine/sha256__" + strings.Repeat("a", 64) + "/manifest.json", "", "", false},
		{"library/alpine/3.20/sha256__" + strings.Repeat("a", 64), "", "", false},
		{"library/alpine/_uploads/abc", "", "", false},
		{"manifest.json", "", "", false},
		{"alpine/manifest.json", "", "", false},
	}

	for _, c := range cases {
		id, ok := dockerParsePath(c.path)
		if ok != c.wantMatch || id.Image != c.image || id.Tag != c.tag {
			t.Errorf("dockerParsePath(%q) = %+v, %v; want %s:%s, %v", c.path, id, ok, c.image, c.tag, c.wantMatch)
		}

		// Files of manifests stored by digest are importable, but they are not
		// tags (see TestImportDockerParseDigestPath).
		_, isDigest := dockerParseDigestPath(c.path)
		if want := c.wantMatch || isDigest; dockerImportable(c.path) != want {
			t.Errorf("dockerImportable(%q) = %v, want %v", c.path, !want, want)
		}

		if c.wantMatch && isDigest {
			t.Errorf("%q is both a tag and a digest manifest", c.path)
		}
	}
}

func TestImportDockerIdentityPrefersTheComponent(t *testing.T) {
	a := sourceAsset{Path: "v2/smoke/hello/manifests/1.0", Component: sourceComponent{Name: "smoke/hello", Version: "1.0"}}

	if id, ok := dockerIdentity(a); !ok || id != (dockerTagID{Image: "smoke/hello", Tag: "1.0"}) {
		t.Fatalf("identity = %+v, %v", id, ok)
	}

	a.Component = sourceComponent{}
	if id, ok := dockerIdentity(a); !ok || id != (dockerTagID{Image: "smoke/hello", Tag: "1.0"}) {
		t.Fatalf("identity without a component = %+v, %v", id, ok)
	}

	if _, ok := dockerIdentity(sourceAsset{Path: "v2/-/blobs/sha256:x", Component: sourceComponent{Name: "smoke/hello", Version: "1.0"}}); ok {
		t.Fatal("a blob became a tag through its component")
	}
}

func TestImportDockerUnitsGroupTagFilesAndKeepOrder(t *testing.T) {
	assets := []sourceAsset{
		{Path: "app/1.0/manifest.json"},
		{Path: "multi/latest/manifest.json"},
		{Path: "multi/latest/list.manifest.json"},
		{Path: "app/2.0/manifest.json"},
	}

	units := dockerUnits(assets)

	var got [][]string

	for _, u := range units {
		var paths []string
		for _, a := range u {
			paths = append(paths, a.Path)
		}

		got = append(got, paths)
	}

	want := [][]string{
		{"app/1.0/manifest.json"},
		{"multi/latest/list.manifest.json", "multi/latest/manifest.json"},
		{"app/2.0/manifest.json"},
	}

	if len(got) != len(want) {
		t.Fatalf("units = %v, want %v", got, want)
	}

	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("unit %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestImportDockerBlobPathsAreNeverImported(t *testing.T) {
	// A source that lists only content the tags reach imports nothing by itself.
	fx := dockerSampleFixture()
	env := newDockerNexusEnv(t, fx, nil, "--path-prefix", "v2/team/app/blobs/")

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 0 || env.oa.writes != 0 {
		t.Fatalf("uploaded=%d writes=%d, want nothing: blobs are not tags", rep.Uploaded, env.oa.writes)
	}
}
