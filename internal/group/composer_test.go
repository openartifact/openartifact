// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

const composerBase = "http://oa.test"

// composerDoc is a member's p2 document of acme/lib: one version per
// {version, reference} pair, each dist pointing at the member's own root.
func composerDoc(t *testing.T, member string, versions ...[2]string) string {
	t.Helper()

	vs := make([]pkgcomposer.Version, 0, len(versions))

	for _, v := range versions {
		vs = append(vs, pkgcomposer.Version{
			"name": "acme/lib", "version": v[0], "version_normalized": v[0] + ".0",
			"source": map[string]any{"type": "git", "url": "https://example.invalid/" + member + ".git", "reference": v[1]},
			"dist": map[string]any{
				"type": "zip", "reference": v[1], "shasum": "",
				"url": composerBase + "/repos/" + member + "/" + pkgcomposer.DistPath("acme/lib", v[0], v[1]),
			},
		})
	}

	body, err := pkgcomposer.EncodeDocument(map[string][]pkgcomposer.Version{"acme/lib": vs}, true)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

// composerGroup is a composer group of a hosted and a proxy member.
func composerGroup(t *testing.T, merge bool) *fixture {
	t.Helper()

	f := newFixture(t, "composer", member("hosted", "composer", "hosted"), member("mirror", "composer", "proxy"))
	f.grp.MergeProxyVersions = merge
	f.h.SetBaseURL(composerBase + "/")

	return f
}

// composerVersions decodes a served group p2 into version -> dist URL.
func composerVersions(t *testing.T, body []byte) map[string]string {
	t.Helper()

	pkgs, err := pkgcomposer.Decode(body)
	if err != nil {
		t.Fatalf("served p2 is not a document: %v\n%s", err, body)
	}

	out := map[string]string{}

	for _, v := range pkgs["acme/lib"] {
		d, _ := v.ZipDist()
		out[v.VersionString()] = d.URL
	}

	return out
}

func TestComposerGroup_VersionsAreUnitedFirstMemberWinsAndLinkedToTheGroup(t *testing.T) {
	f := composerGroup(t, true)
	f.set("hosted", "p2/acme/lib.json", 200, composerDoc(t, "hosted", [2]string{"1.1.0", "h110"}, [2]string{"1.0.0", "h100"}))
	f.set("mirror", "p2/acme/lib.json", 200, composerDoc(t, "mirror", [2]string{"2.0.0", "m200"}, [2]string{"1.1.0", "m110"}))

	w := f.get(http.MethodGet, "p2/acme/lib.json")
	if w.Code != http.StatusOK {
		t.Fatalf("p2 = %d %s", w.Code, w.Body.String())
	}

	got := composerVersions(t, w.Body.Bytes())
	group := composerBase + "/repos/all/dists/acme/lib/"

	want := map[string]string{
		"1.0.0": group + "1.0.0/h100.zip",
		"1.1.0": group + "1.1.0/h110.zip", // the hosted member's, not the mirror's
		"2.0.0": group + "2.0.0/m200.zip",
	}

	for v, u := range want {
		if got[v] != u {
			t.Errorf("dist of %s = %q, want %q", v, got[v], u)
		}
	}

	if len(got) != len(want) || strings.Contains(w.Body.String(), "/repos/hosted/") || strings.Contains(w.Body.String(), "/repos/mirror/") {
		t.Errorf("versions = %v; member links leaked: %s", got, w.Body.String())
	}

	if !strings.Contains(w.Body.String(), "example.invalid/mirror.git") {
		t.Errorf("source blocks of the mirror were lost: %s", w.Body.String())
	}
}

func TestComposerGroup_HostedWinsHidesTheProxyVersionsOfAPackageItHolds(t *testing.T) {
	f := composerGroup(t, false)
	f.set("hosted", "p2/acme/lib.json", 200, composerDoc(t, "hosted", [2]string{"1.0.0", "h100"}))
	f.set("mirror", "p2/acme/lib.json", 200, composerDoc(t, "mirror", [2]string{"9.9.9", "m999"}))

	w := f.get(http.MethodGet, "p2/acme/lib.json")
	if got := composerVersions(t, w.Body.Bytes()); len(got) != 1 || got["1.0.0"] == "" {
		t.Errorf("versions = %v, want the hosted member's only", got)
	}

	if slices.Contains(f.asked, "mirror") {
		t.Errorf("members asked = %v, the proxy must not be asked about a hosted package", f.asked)
	}
}

func TestComposerGroup_HostedHoldingOnlyDevVersionsStillWins(t *testing.T) {
	f := composerGroup(t, false)
	f.set("hosted", "p2/acme/lib~dev.json", 200, composerDoc(t, "hosted", [2]string{"dev-main", "hdev"}))
	f.set("mirror", "p2/acme/lib.json", 200, composerDoc(t, "mirror", [2]string{"9.9.9", "m999"}))

	if w := f.get(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusNotFound {
		t.Errorf("stable p2 = %d, want 404 (the hosted package hides the proxy's)", w.Code)
	}
}

func TestComposerGroup_AMemberErrorGivesAPartialUncachedAnswerOr502(t *testing.T) {
	f := composerGroup(t, true)
	f.set("hosted", "p2/acme/lib.json", 200, composerDoc(t, "hosted", [2]string{"1.0.0", "h100"}))
	f.set("mirror", "p2/acme/lib.json", 500, "boom")

	w := f.get(http.MethodGet, "p2/acme/lib.json")
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("ETag") != "" {
		t.Errorf("partial = %d cache-control %q etag %q, want 200 no-store without validator",
			w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
	}

	f = composerGroup(t, true)
	f.set("mirror", "p2/acme/lib.json", 500, "boom")

	if w := f.get(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusBadGateway {
		t.Errorf("only a failing member = %d, want 502", w.Code)
	}

	if w := composerGroup(t, true).get(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusNotFound {
		t.Errorf("no member has it = %d, want 404", w.Code)
	}
}

func TestComposerGroup_PackagesJSONIsTheGroups(t *testing.T) {
	f := composerGroup(t, true)
	f.set("hosted", "packages.json", 200, `{"packages":[],"metadata-url":"x","available-packages":["a/b"]}`)
	f.set("mirror", "packages.json", 200, `{"packages":[],"metadata-url":"y"}`)

	w := f.get(http.MethodGet, "packages.json")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"metadata-url":"`+composerBase+`/repos/all/p2/%package%.json"`) {
		t.Fatalf("packages.json = %d %s", w.Code, w.Body.String())
	}

	if strings.Contains(w.Body.String(), "available-packages") {
		t.Errorf("a proxy member makes the package list unknowable: %s", w.Body.String())
	}
}

func TestComposerGroup_AvailablePackagesOnlyWhenEveryMemberIsHosted(t *testing.T) {
	f := newFixture(t, "composer", member("one", "composer", "hosted"), member("two", "composer", "hosted"))
	f.h.SetBaseURL(composerBase)
	f.set("one", "packages.json", 200, `{"available-packages":["z/z","a/b"]}`)
	f.set("two", "packages.json", 200, `{"available-packages":["a/b","c/d"]}`)

	var doc struct {
		Available []string `json:"available-packages"` //nolint:tagliatelle // Composer wire format
	}

	w := f.get(http.MethodGet, "packages.json")
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || !slices.Equal(doc.Available, []string{"a/b", "c/d", "z/z"}) {
		t.Errorf("available-packages = %v (%v), want the sorted union", doc.Available, err)
	}

	// One member that cannot list its packages makes the union unknown.
	f.set("two", "packages.json", 500, "boom")

	if w := f.get(http.MethodGet, "packages.json"); strings.Contains(w.Body.String(), "available-packages") {
		t.Errorf("incomplete union published: %s", w.Body.String())
	}
}

func TestComposerGroup_DistsAreFirstHitAndListSearchAreNotServed(t *testing.T) {
	f := composerGroup(t, true)
	dist := pkgcomposer.DistPath("acme/lib", "2.0.0", "m200")
	f.set("mirror", dist, 200, "zip-from-mirror")

	if w := f.get(http.MethodGet, dist); w.Code != http.StatusOK || w.Body.String() != "zip-from-mirror" {
		t.Errorf("dist = %d %q, want the member that has it", w.Code, w.Body.String())
	}

	for _, p := range []string{"packages/list.json", "search.json", "api/packages/acme/lib"} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}
}
