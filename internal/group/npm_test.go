// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const npmBase = "http://oa.test"

// packument builds a packument whose versions point at member-specific URLs
// and carry a member-specific integrity, so the merge winner is observable.
func packument(t *testing.T, member, name string, versions map[string]string, tags map[string]string) string {
	t.Helper()

	vs := map[string]any{}
	for v, marker := range versions {
		vs[v] = map[string]any{
			"name": name, "version": v, "marker": marker,
			"dist": map[string]any{
				"tarball":   "https://" + member + ".example/" + name + "/-/pkg-" + v + ".tgz",
				"integrity": "sha512-" + marker,
				"shasum":    marker,
			},
		}
	}

	b, err := json.Marshal(map[string]any{
		"name": name, "dist-tags": tags, "versions": vs,
		"description": "from " + member,
		"time":        map[string]string{"modified": "m-" + member, "created": "c-" + member, "1.0.0": "t-" + member},
	})
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func npmGroup(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "npm", member("hosted", "npm", "hosted"), member("mirror", "npm", "proxy"))
	f.h.SetBaseURL(npmBase + "/")

	return f
}

type merged struct {
	Description string                    `json:"description"`
	DistTags    map[string]string         `json:"dist-tags"` //nolint:tagliatelle // npm registry wire format
	Time        map[string]string         `json:"time"`
	Versions    map[string]map[string]any `json:"versions"`
}

func decodeMerged(t *testing.T, body []byte) merged {
	t.Helper()

	var m merged
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("merged packument is not JSON: %v\n%s", err, body)
	}

	return m
}

func TestNPMGroup_MergePrecedence(t *testing.T) {
	f := npmGroup(t)
	f.set(
		"hosted",
		"lib",
		200,
		packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA", "1.1.0": "AAA1"}, map[string]string{"latest": "1.1.0"}),
	)
	f.set("mirror", "lib", 200, packument(t, "mirror", "lib",
		map[string]string{"1.0.0": "BBBB", "2.0.0": "BBB2"},
		map[string]string{"latest": "2.0.0", "next": "2.0.0"}))

	w := f.get(http.MethodGet, "lib")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	m := decodeMerged(t, w.Body.Bytes())

	if len(m.Versions) != 3 {
		t.Errorf("versions = %v, want union of 1.0.0, 1.1.0, 2.0.0", keys(m.Versions))
	}

	if got := m.Versions["1.0.0"]["marker"]; got != "AAAA" {
		t.Errorf("1.0.0 comes from %v, want the first member (AAAA)", got)
	}

	if got := m.Versions["2.0.0"]["marker"]; got != "BBB2" {
		t.Errorf("2.0.0 marker = %v, want BBB2", got)
	}

	if m.DistTags["latest"] != "1.1.0" {
		t.Errorf("latest = %q, want 1.1.0 (first member that has the tag)", m.DistTags["latest"])
	}

	if m.DistTags["next"] != "2.0.0" {
		t.Errorf("next = %q, want 2.0.0 (only the second member has it)", m.DistTags["next"])
	}

	if m.Description != "from hosted" {
		t.Errorf("description = %q, want the first member's", m.Description)
	}

	if m.Time["modified"] != "m-hosted" || m.Time["1.0.0"] != "t-hosted" {
		t.Errorf("time = %v, want first-member entries", m.Time)
	}

	for v, want := range map[string]string{
		"1.0.0": npmBase + "/repos/all/lib/-/pkg-1.0.0.tgz",
		"1.1.0": npmBase + "/repos/all/lib/-/pkg-1.1.0.tgz",
		"2.0.0": npmBase + "/repos/all/lib/-/pkg-2.0.0.tgz",
	} {
		dist, _ := m.Versions[v]["dist"].(map[string]any)
		if dist["tarball"] != want {
			t.Errorf("%s tarball = %v, want %s (rewritten to the group)", v, dist["tarball"], want)
		}
	}

	// Digests are the winning member's, untouched by the rewrite.
	dist, _ := m.Versions["1.0.0"]["dist"].(map[string]any)
	if dist["integrity"] != "sha512-AAAA" || dist["shasum"] != "AAAA" {
		t.Errorf("1.0.0 dist = %v, want the first member's digests", dist)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

func TestNPMGroup_ScopedPackageIsRequestedEscapedAndRewritten(t *testing.T) {
	f := npmGroup(t)
	f.set(
		"mirror",
		"@s%2fname",
		200,
		packument(t, "mirror", "@s/name", map[string]string{"1.0.0": "BBBB"}, map[string]string{"latest": "1.0.0"}),
	)

	for _, path := range []string{"@s%2fname", "@s/name", "@s%2Fname"} {
		w := f.get(http.MethodGet, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}

		dist, _ := decodeMerged(t, w.Body.Bytes()).Versions["1.0.0"]["dist"].(map[string]any)
		if want := npmBase + "/repos/all/@s/name/-/pkg-1.0.0.tgz"; dist["tarball"] != want {
			t.Errorf("GET %s tarball = %v, want %s", path, dist["tarball"], want)
		}
	}
}

func TestNPMGroup_PackumentRevalidatesWithETag(t *testing.T) {
	f := npmGroup(t)
	f.set("hosted", "lib", 200, packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))

	first := f.get(http.MethodGet, "lib")
	etag := first.Header().Get("ETag")

	if etag == "" {
		t.Fatal("no ETag on the merged packument")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, "lib", f.backend())
	if w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}
}

func TestNPMGroup_VersionManifestUsesGroupTarballURL(t *testing.T) {
	f := npmGroup(t)
	f.set("hosted", "lib", 200, packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))
	f.set("mirror", "lib", 200, packument(t, "mirror", "lib", map[string]string{"2.0.0": "BBB2"}, map[string]string{"beta": "2.0.0"}))

	for path, wantMarker := range map[string]string{"lib/2.0.0": "BBB2", "lib/beta": "BBB2", "lib/latest": "AAAA", "lib/1.0.0": "AAAA"} {
		w := f.get(http.MethodGet, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}

		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}

		dist, _ := v["dist"].(map[string]any)
		tarball, _ := dist["tarball"].(string)
		if v["marker"] != wantMarker || !strings.HasPrefix(tarball, npmBase+"/repos/all/lib/-/") {
			t.Errorf("GET %s = %v", path, v)
		}
	}

	if w := f.get(http.MethodGet, "lib/9.9.9"); w.Code != http.StatusNotFound {
		t.Errorf("unknown version = %d, want 404", w.Code)
	}
}

func TestNPMGroup_TarballIsFirstHit(t *testing.T) {
	f := npmGroup(t)
	f.set("mirror", "lib/-/pkg-2.0.0.tgz", 200, "tarball-bytes")

	w := f.get(http.MethodGet, "lib/-/pkg-2.0.0.tgz")
	if w.Code != http.StatusOK || w.Body.String() != "tarball-bytes" {
		t.Errorf("tarball = %d %q", w.Code, w.Body.String())
	}

	if got := f.asked; len(got) != 2 || got[0] != "hosted" || got[1] != "mirror" {
		t.Errorf("members asked = %v, want hosted then mirror", got)
	}
}

func TestNPMGroup_MemberFailureHandling(t *testing.T) {
	good := func(f *fixture) {
		f.set("hosted", "lib", 200, packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}))
	}

	t.Run("a failing member is ignored while another contributes", func(t *testing.T) {
		f := npmGroup(t)
		good(f)
		f.set("mirror", "lib", 502, "upstream down")

		if w := f.get(http.MethodGet, "lib"); w.Code != http.StatusOK {
			t.Errorf("status %d, want 200", w.Code)
		}
	})

	t.Run("nobody has it", func(t *testing.T) {
		if w := npmGroup(t).get(http.MethodGet, "lib"); w.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", w.Code)
		}
	})

	t.Run("nobody has it and one member failed", func(t *testing.T) {
		f := npmGroup(t)
		f.set("mirror", "lib", 502, "down")

		if w := f.get(http.MethodGet, "lib"); w.Code != http.StatusBadGateway {
			t.Errorf("status %d, want 502", w.Code)
		}
	})

	t.Run("a member the caller cannot read contributes nothing", func(t *testing.T) {
		f := npmGroup(t)
		good(f)
		f.denied["hosted"] = true

		if w := f.get(http.MethodGet, "lib"); w.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", w.Code)
		}
	})

	t.Run("publishing to a group is refused", func(t *testing.T) {
		f := npmGroup(t)

		if w := f.get(http.MethodPut, "lib"); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("PUT = %d, want 405", w.Code)
		}
	})
}
