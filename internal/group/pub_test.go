// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const pubBase = "http://oa.test"

// pubMemberDoc is the package document "demo" a member answers: every release
// carries the member's own archive URL, the way its server writes it.
func pubMemberDoc(member string, versions ...string) string {
	return pubNamedDoc("demo", member, versions...)
}

// pubNamedDoc is pubMemberDoc for the package name.
func pubNamedDoc(name, member string, versions ...string) string {
	entries := make([]map[string]any, len(versions))
	for i, v := range versions {
		entries[i] = map[string]any{
			"version":        v,
			"archive_url":    pubBase + "/repos/pub/" + member + "/api/archives/" + name + "-" + v + ".tar.gz",
			"archive_sha256": strings.Repeat("a", 63) + string(rune('a'+i)),
			"pubspec":        map[string]any{"name": name, "version": v, "description": "from " + member},
		}
	}

	b, _ := json.Marshal(map[string]any{"name": name, "latest": entries[len(entries)-1], "versions": entries})

	return string(b)
}

func pubGroup(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "pub", member("hosted", "pub", "hosted"), member("mirror", "pub", "proxy"))
	f.grp.MergeProxyVersions = true
	f.h.SetBaseURL(pubBase + "/")

	return f
}

type pubRelease struct {
	Version     string `json:"version"`
	ArchiveURL  string `json:"archive_url"`
	Description string
}

func pubReleases(t *testing.T, body []byte) ([]pubRelease, string) {
	t.Helper()

	var doc struct {
		Versions []struct {
			Version    string `json:"version"`
			ArchiveURL string `json:"archive_url"`
			Pubspec    struct {
				Description string `json:"description"`
			} `json:"pubspec"`
		} `json:"versions"`
		Latest struct {
			Version string `json:"version"`
		} `json:"latest"`
	}

	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("not a package document: %v: %s", err, body)
	}

	out := make([]pubRelease, len(doc.Versions))
	for i, v := range doc.Versions {
		out[i] = pubRelease{Version: v.Version, ArchiveURL: v.ArchiveURL, Description: v.Pubspec.Description}
	}

	return out, doc.Latest.Version
}

func TestPubGroup_DocumentIsTheUnionFirstMemberWinsAndLinksAreTheGroups(t *testing.T) {
	f := pubGroup(t)
	f.set("hosted", "api/packages/demo", 200, pubMemberDoc("hosted", "1.0.0"))
	f.set("mirror", "api/packages/demo", 200, pubMemberDoc("mirror", "1.0.0", "2.0.0", "1.5.0"))

	w := f.get(http.MethodGet, "api/packages/demo")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/vnd.pub.v2+json" {
		t.Fatalf("status %d %s: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}

	releases, latest := pubReleases(t, w.Body.Bytes())

	var got []string
	for _, r := range releases {
		got = append(got, r.Version+":"+r.Description)

		if want := pubBase + "/repos/pub/all/api/archives/demo-" + r.Version + ".tar.gz"; r.ArchiveURL != want {
			t.Errorf("archive_url of %s = %s, want %s", r.Version, r.ArchiveURL, want)
		}
	}

	if want := "1.0.0:from hosted 1.5.0:from mirror 2.0.0:from mirror"; strings.Join(got, " ") != want || latest != "2.0.0" {
		t.Errorf("releases = %v, latest %s; want %s, 2.0.0", got, latest, want)
	}

	if etag := w.Header().Get("ETag"); etag == "" {
		t.Error("merged document has no ETag")
	}

	// The version document is that release's entry alone.
	w = f.get(http.MethodGet, "api/packages/demo/versions/1.5.0")

	var one map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || w.Code != http.StatusOK ||
		one["version"] != "1.5.0" || one["archive_url"] != pubBase+"/repos/pub/all/api/archives/demo-1.5.0.tar.gz" {
		t.Errorf("version = %d: %s", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "api/packages/demo/versions/9.9.9"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted version = %d, want 404", w.Code)
	}
}

func TestPubGroup_MemberWithoutThePackageIsSkipped(t *testing.T) {
	f := pubGroup(t)
	f.set("mirror", "api/packages/demo", 200, pubMemberDoc("mirror", "1.0.0"))

	releases, _ := pubReleases(t, f.get(http.MethodGet, "api/packages/demo").Body.Bytes())
	if len(releases) != 1 || releases[0].Description != "from mirror" {
		t.Errorf("releases = %+v", releases)
	}

	if w := f.get(http.MethodGet, "api/packages/other"); w.Code != http.StatusNotFound {
		t.Errorf("unknown package = %d, want 404", w.Code)
	}
}

func TestPubGroup_FailingMemberDoesNotHideTheOthersButAloneIs502(t *testing.T) {
	f := pubGroup(t)
	f.set("hosted", "api/packages/demo", 500, "boom")
	f.set("mirror", "api/packages/demo", 200, pubMemberDoc("mirror", "1.0.0"))

	if w := f.get(http.MethodGet, "api/packages/demo"); w.Code != http.StatusOK {
		t.Errorf("one failing member = %d, want the others' merge", w.Code)
	}

	f.set("mirror", "api/packages/demo", 502, "boom")

	if w := f.get(http.MethodGet, "api/packages/demo"); w.Code != http.StatusBadGateway {
		t.Errorf("every member failing = %d, want 502", w.Code)
	}
}

func TestPubGroup_NotADocumentIsNotServed(t *testing.T) {
	f := pubGroup(t)
	f.set("mirror", "api/packages/demo", 200, "<html>portal</html>")

	if w := f.get(http.MethodGet, "api/packages/demo"); w.Code == http.StatusOK {
		t.Errorf("an HTML answer was merged: %s", w.Body.String())
	}
}

func TestPubGroup_ArchiveComesFromFirstMemberThatHasIt(t *testing.T) {
	f := pubGroup(t)
	const p = "api/archives/demo-1.0.0.tar.gz"
	f.set("mirror", p, 200, "mirror-archive")

	if w := f.get(http.MethodGet, p); w.Body.String() != "mirror-archive" {
		t.Errorf("= %d %q", w.Code, w.Body.String())
	}

	f.set("hosted", p, 200, "hosted-archive")

	if w := f.get(http.MethodGet, p); w.Body.String() != "hosted-archive" {
		t.Errorf("= %q, want the first member", w.Body.String())
	}
}

func TestPubGroup_PublishingAndOtherPathsAre404WithoutAskingMembers(t *testing.T) {
	f := pubGroup(t)

	for _, p := range []string{
		"api/packages/versions/new", "api/packages/versions/newUpload", "api/packages/versions/newUploadFinish",
		"api/packages/Demo", "api/archives/demo-1.0.0.zip", "foo",
	} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked: %v", f.asked)
	}
}
