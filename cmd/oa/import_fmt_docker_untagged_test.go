// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// referrersFixture is an image with the artifacts a signing and scanning
// pipeline attaches to it: a signature, an SBOM, and an attestation of the
// SBOM. They are stored by digest only; the attestation is listed first, so the
// importer meets an artifact before the manifest it is attached to.
type referrersFixture struct {
	fx                  *dockerFixture
	app, sig, sbom, att dockerFakeImage
}

func newReferrersFixture() referrersFixture {
	fx := newDockerFixture()
	app := fx.image("config-app", "app layer")
	fx.tag("team/app", "1.0", app)

	sig := fx.referrer(app, dockerFakeSigType, "signature of app")
	sbom := fx.referrer(app, "application/spdx+json", "sbom of app")
	att := fx.referrer(sbom, "application/vnd.in-toto+json", "attestation of the sbom")

	fx.untagged("team/app", att)
	fx.untagged("team/app", sig)
	fx.untagged("team/app", sbom)

	return referrersFixture{fx: fx, app: app, sig: sig, sbom: sbom, att: att}
}

// referrers asks the destination which manifests are attached to subject.
func (e *dockerEnv) referrers(image, subject string) []string {
	e.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		e.oa.srv.URL+"/v2/"+dockerDst+"/"+image+"/referrers/"+subject, nil)
	if err != nil {
		e.t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+e.oa.token)

	resp, err := e.oa.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	var index struct {
		Manifests []ociDescriptor `json:"manifests"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		e.t.Fatal(err)
	}

	var out []string
	for _, m := range index.Manifests {
		out = append(out, m.Digest)
	}

	return out
}

// requireUntagged checks that the manifest and its blobs are in the destination
// with the source's bytes, and that no tag or listing row stands for it.
func (e *dockerEnv) requireUntagged(image string, img dockerFakeImage) {
	e.t.Helper()

	held := e.fk.images[dockerDst+"/"+image]
	if held == nil {
		e.t.Fatalf("image %s was not created", image)
	}

	if !bytes.Equal(held.manifests[img.digest], e.fx.manifests[img.digest]) {
		e.t.Fatalf("%s: manifest %s is missing or differs from the source", image, img.digest)
	}

	for _, b := range img.blobs {
		if !bytes.Equal(held.blobs[b], e.fx.blobs[b]) {
			e.t.Fatalf("%s: blob %s differs from the source", image, b)
		}
	}

	for tag, d := range held.tags {
		if d == img.digest {
			e.t.Fatalf("%s: tag %s points at the untagged manifest", image, tag)
		}
	}

	for _, it := range e.oa.repos[dockerDst].items {
		if it.path == img.digest {
			e.t.Fatalf("%s: the untagged manifest is listed: %+v", image, it)
		}
	}
}

// requirePushedOnce checks that no manifest was sent twice by digest.
func (e *dockerEnv) requirePushedOnce() {
	e.t.Helper()

	seen := map[string]int{}

	for _, p := range e.fk.puts {
		if strings.Contains(p.ref, ":") {
			seen[p.name+"@"+p.ref]++
		}
	}

	for k, n := range seen {
		if n != 1 {
			e.t.Errorf("manifest %s was pushed %d times", k, n)
		}
	}
}

func TestImportDockerImportsUntaggedManifestsByDigest(t *testing.T) {
	fx := newDockerFixture()
	current := fx.image("config-2", "base layer", "2.0 layer")
	old := fx.image("config-1", "base layer", "1.0 layer")
	fx.tag("team/app", "2.0", current)
	fx.untagged("team/app", old)

	env := newDockerNexusEnv(t, fx, nil, "--concurrency", "4")

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 2 {
		t.Fatalf("uploaded=%d, want the tag and the untagged manifest", rep.Uploaded)
	}

	// Every other file is a blob, or a manifest the tag brought along.
	if want := len(env.files) - 2; rep.Ignored != want {
		t.Fatalf("ignored=%d, want %d", rep.Ignored, want)
	}

	env.requireAllImported()
	env.requireUntagged("team/app", old)
	env.requirePushedOnce()

	var puts int

	for _, p := range env.fk.puts {
		if p.ref != old.digest {
			continue
		}

		puts++

		if p.created != nexusWant || !isOCIManifestType(p.contentType) {
			t.Errorf("untagged manifest pushed with created-at %q and type %q", p.created, p.contentType)
		}
	}

	if puts != 1 {
		t.Fatalf("the untagged manifest was pushed %d times by digest", puts)
	}

	// The base layer is in the image once, however many manifests use it.
	for key, n := range env.fk.blobPut {
		if n != 1 {
			t.Errorf("%s was uploaded %d times", key, n)
		}
	}
}

func TestImportDockerAttachesReferrersToTheirSubject(t *testing.T) {
	rf := newReferrersFixture()
	env := newDockerNexusEnv(t, rf.fx, nil, "--concurrency", "1")

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 4 {
		t.Fatalf("uploaded=%d, want the tag and three artifacts", rep.Uploaded)
	}

	env.requireAllImported()

	for _, a := range []dockerFakeImage{rf.sig, rf.sbom, rf.att} {
		env.requireUntagged("team/app", a)
	}

	// The destination's own referrers listing shows the links.
	want := []string{rf.sig.digest, rf.sbom.digest}
	slices.Sort(want)

	if got := env.referrers("team/app", rf.app.digest); !slices.Equal(got, want) {
		t.Fatalf("referrers of the image = %v, want %v", got, want)
	}

	if got := env.referrers("team/app", rf.sbom.digest); !slices.Equal(got, []string{rf.att.digest}) {
		t.Fatalf("referrers of the SBOM = %v, want [%s]", got, rf.att.digest)
	}

	// An artifact follows its subject, though the source listed it first.
	pos := func(ref string) int {
		return slices.Index(env.fk.events, "manifest reg/team/app "+ref)
	}

	if pos("1.0") < 0 || pos("1.0") > pos(rf.sbom.digest) || pos(rf.sbom.digest) > pos(rf.att.digest) {
		t.Fatalf("manifest order = %v: want the image, then the SBOM, then its attestation", env.fk.events)
	}

	env.requirePushedOnce()
}

func TestImportDockerCopiesTheReferrersTagOfARegistryWithoutTheAPI(t *testing.T) {
	fx := newDockerFixture()
	app := fx.image("config-app", "app layer")
	sig := fx.referrer(app, dockerFakeSigType, "signature of app")
	sbom := fx.referrer(app, "application/spdx+json", "sbom of app")

	fallback := fx.referrersIndex(sig, sbom)
	fx.tag("team/app", "1.0", app)
	fx.tag("team/app", "sha256-"+strings.TrimPrefix(app.digest, ociDigestPrefix), fallback)

	env := newDockerNexusEnv(t, fx, nil)

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 2 {
		t.Fatalf("uploaded=%d, want the image and its referrers tag", rep.Uploaded)
	}

	env.requireAllImported()

	want := []string{sig.digest, sbom.digest}
	slices.Sort(want)

	if got := env.referrers("team/app", app.digest); !slices.Equal(got, want) {
		t.Fatalf("referrers of the image = %v, want %v", got, want)
	}

	env.requirePushedOnce()
}

func TestImportDockerCopiesAttestationManifestsOfAnIndexOnce(t *testing.T) {
	fx := newDockerFixture()
	amd, arm := fx.image("config-amd64", "amd64 layer"), fx.image("config-arm64", "arm64 layer")

	attestation := fx.attestation(amd)

	idx := fx.index(amd, arm, attestation)
	fx.tag("tool", "latest", idx)

	env := newDockerNexusEnv(t, fx, nil, "--concurrency", "4")

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 1 {
		t.Fatalf("uploaded=%d, want only the tag: the attestation manifest is its child", rep.Uploaded)
	}

	env.requireAllImported()
	env.requirePushedOnce()

	if _, ok := env.fk.images["reg/tool"].manifests[attestation.digest]; !ok {
		t.Fatal("the attestation manifest of the index was not copied")
	}
}

func TestImportDockerUntaggedManifestsAreIdempotent(t *testing.T) {
	rf := newReferrersFixture()
	env := newDockerNexusEnv(t, rf.fx, nil)

	env.requireClean(env.run())

	writes := env.oa.writes
	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 0 || rep.Present != 1 {
		t.Fatalf("second run uploaded=%d present=%d, want 0 and the tag", rep.Uploaded, rep.Present)
	}

	if env.oa.writes != writes {
		t.Fatalf("second run wrote %d times", env.oa.writes-writes)
	}
}

func TestImportDockerUntaggedManifestsResumeFromStateFile(t *testing.T) {
	fx := newDockerFixture()
	fx.untagged("team/app", fx.image("config-old", "old layer"))

	state := filepath.Join(t.TempDir(), "state.jsonl")
	env := newDockerNexusEnv(t, fx, nil, "--state-file", state)

	if rep := env.run(); rep.Uploaded != 1 {
		t.Fatalf("first run uploaded %d, failures %+v", rep.Uploaded, rep.Failures)
	}

	// Wipe the destination: only the state file knows the manifest was done.
	env.fk.images = map[string]*fakeDockerImage{}
	writes := env.oa.writes

	rep := env.run()
	env.requireClean(rep)

	if rep.Resumed != 1 || rep.Uploaded != 0 || env.oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, env.oa.writes-writes)
	}
}

func TestImportDockerDryRunPlansUntaggedManifestsOnce(t *testing.T) {
	rf := newReferrersFixture()
	env := newDockerNexusEnv(t, rf.fx, nil, "--dry-run", "--concurrency", "4")

	rep := env.run()
	env.requireClean(rep)

	if env.oa.writes != 0 || len(env.fk.images) != 0 {
		t.Fatalf("a dry run wrote: writes=%d images=%d", env.oa.writes, len(env.fk.images))
	}

	if rep.Uploaded != 4 {
		t.Fatalf("planned %d transfers, want the tag and three artifacts", rep.Uploaded)
	}

	// The image's own manifest is planned with its tag, not again as a loose
	// one, and the empty config the artifacts share counts once.
	if want := rf.fx.plannedBlobBytes(); rep.Bytes != want {
		t.Fatalf("planned bytes = %d, want %d", rep.Bytes, want)
	}
}

func TestImportDockerReportsReferrersTheDestinationDidNotLink(t *testing.T) {
	rf := newReferrersFixture()
	env := newDockerNexusEnv(t, rf.fx, nil, "--concurrency", "1")
	env.fk.ignoreSubject = true

	rep := env.run()

	if rep.Uploaded != 1 || len(rep.Failures) != 3 {
		t.Fatalf("uploaded=%d failures=%+v, want the tag and three failed artifacts", rep.Uploaded, rep.Failures)
	}

	for _, f := range rep.Failures {
		if !strings.HasPrefix(f.Path, "team/app@sha256:") || !strings.Contains(f.Error, "without linking it to its subject") {
			t.Errorf("failure %+v does not name the artifact and the missing link", f)
		}
	}
}

func TestImportDockerArtifactoryImportsUntaggedManifests(t *testing.T) {
	rf := newReferrersFixture()
	env := newDockerArtifactoryEnv(t, rf.fx, nil, "--concurrency", "4")

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 4 {
		t.Fatalf("uploaded=%d, want the tag and three artifacts", rep.Uploaded)
	}

	env.requireAllImported()

	for _, a := range []dockerFakeImage{rf.sig, rf.sbom, rf.att} {
		env.requireUntagged("team/app", a)
	}

	for _, p := range env.fk.puts {
		if p.created != artiWant {
			t.Errorf("manifest %s %s: created-at %q, want %q", p.name, p.ref, p.created, artiWant)
		}
	}

	env.requirePushedOnce()
}

func TestImportDockerSkipsListedFilesThatAreNotServedAsManifests(t *testing.T) {
	fx := newDockerFixture()
	fx.tag("team/app", "1.0", fx.image("config-app", "app layer"))

	ghost := fx.image("config-ghost", "ghost layer")
	fx.untagged("team/app", ghost)

	env := newDockerArtifactoryEnv(t, fx, nil)
	delete(fx.manifests, ghost.digest)

	rep := env.run()
	env.requireClean(rep)

	if rep.Uploaded != 1 {
		t.Fatalf("uploaded=%d, want the tag only", rep.Uploaded)
	}

	if _, ok := env.fk.images["reg/team/app"].manifests[ghost.digest]; ok {
		t.Fatal("a manifest the source does not serve was copied")
	}
}

func TestImportDockerParseDigestPath(t *testing.T) {
	d := ociDigestPrefix + strings.Repeat("a", 64)
	folder := "sha256__" + strings.Repeat("a", 64)

	cases := []struct {
		path  string
		image string
		match bool
	}{
		{"v2/team/app/manifests/" + d, "team/app", true},
		{"v2/app/manifests/" + d, "app", true},
		{"v2/-/blobs/" + d, "", false},
		{"v2/app/manifests/latest", "", false},
		{"v2/app/manifests/sha256:short", "", false},
		{"v2/app/manifests/sha256:" + strings.Repeat("A", 64), "", false},
		{"v2/manifests/" + d, "", false},
		{"team/app/" + folder + "/manifest.json", "team/app", true},
		{"team/app/" + folder + "/list.manifest.json", "team/app", true},
		{"team/app/1.0/" + folder, "", false},
		{"team/app/" + folder, "", false},
		{folder + "/manifest.json", "", false},
		{"team/app/sha256__short/manifest.json", "", false},
		{"team/app/1.0/manifest.json", "", false},
	}

	for _, c := range cases {
		id, ok := dockerParseDigestPath(c.path)
		if ok != c.match || id.Image != c.image || (ok && id.Digest != d) {
			t.Errorf("dockerParseDigestPath(%q) = %+v, %v; want %s, %v", c.path, id, ok, c.image, c.match)
		}

		if want := c.match || strings.HasSuffix(c.path, "1.0/manifest.json") || strings.HasSuffix(c.path, "latest"); dockerImportable(c.path) != want {
			t.Errorf("dockerImportable(%q) = %v, want %v", c.path, !want, want)
		}
	}
}

func TestImportDockerUnitsPutDigestManifestsAfterTheTagsOfTheirImage(t *testing.T) {
	d1, d2 := ociDigestPrefix+strings.Repeat("1", 64), ociDigestPrefix+strings.Repeat("2", 64)

	assets := []sourceAsset{
		{Path: "v2/app/manifests/" + d1},
		{Path: "v2/app/manifests/1.0"},
		{Path: "v2/other/manifests/" + d2},
		{Path: "v2/app/manifests/2.0"},
		{Path: "v2/lone/manifests/latest"},
		{Path: "v2/app/manifests/" + d2},
	}

	var got [][]string

	for _, u := range dockerUnits(assets) {
		var paths []string
		for _, a := range u {
			paths = append(paths, a.Path)
		}

		got = append(got, paths)
	}

	want := [][]string{
		{"v2/app/manifests/1.0"},
		{"v2/app/manifests/2.0"},
		{"v2/lone/manifests/latest"},
		{"v2/app/manifests/" + d1, "v2/app/manifests/" + d2, "v2/app/manifests/1.0", "v2/app/manifests/2.0"},
		{"v2/other/manifests/" + d2},
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
