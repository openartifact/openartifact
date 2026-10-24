// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const nugetBase = "http://oa.test"

func nugetGroup(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "nuget", member("hosted", "nuget", "hosted"), member("mirror", "nuget", "proxy"))
	f.h.SetBaseURL(nugetBase + "/")

	return f
}

// nugetItem is a registration item as member serves it.
func nugetItem(member, id, version string) string {
	v3 := nugetBase + "/repos/" + member + "/v3/"

	return fmt.Sprintf(`{"@id":"%[1]sregistration/%[2]s/%[3]s.json","catalogEntry":{"@id":"%[1]sregistration/%[2]s/%[3]s.json","version":%[3]q,"marker":%[4]q},`+
		`"packageContent":"%[1]spackage/%[2]s/%[3]s/%[2]s.%[3]s.nupkg","registration":"%[1]sregistration/%[2]s/index.json"}`, v3, id, version, member)
}

// nugetInlineIndex is an index with one inline page holding the versions.
func nugetInlineIndex(member, id string, versions ...string) string {
	idx := nugetBase + "/repos/" + member + "/v3/registration/" + id + "/index.json"

	items := make([]string, len(versions))
	for i, v := range versions {
		items[i] = nugetItem(member, id, v)
	}

	return fmt.Sprintf(`{"@id":%[1]q,"count":1,"items":[{"@id":"%[1]s#page/%[2]s/%[3]s","count":%[4]d,"lower":%[2]q,"upper":%[3]q,"parent":%[1]q,"items":[%[5]s]}]}`,
		idx, versions[0], versions[len(versions)-1], len(versions), strings.Join(items, ","))
}

func nugetVersionsOf(t *testing.T, body []byte) []string {
	t.Helper()

	var v struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("not a version list: %v\n%s", err, body)
	}

	return v.Versions
}

func TestNuGetGroup_ServiceIndexIsTheGroups(t *testing.T) {
	f := nugetGroup(t)

	w := f.get(http.MethodGet, "v3/index.json")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	var doc struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, r := range doc.Resources {
		got[r.Type] = r.ID
	}

	reg, pkg := nugetBase+"/repos/all/v3/registration/", nugetBase+"/repos/all/v3/package/"
	if got["RegistrationsBaseUrl/3.6.0"] != reg || got["RegistrationsBaseUrl"] != reg || got["PackageBaseAddress/3.0.0"] != pkg || len(got) != 4 {
		t.Errorf("resources = %v", got)
	}

	if len(f.asked) != 0 {
		t.Errorf("the service index asked members: %v", f.asked)
	}

	for _, p := range []string{"v3/query", "v3/autocomplete"} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (no search on groups)", p, w.Code)
		}
	}
}

func TestNuGetGroup_VersionListsAreUnited(t *testing.T) {
	f := nugetGroup(t)
	f.set("hosted", "v3/package/lib/index.json", 200, `{"versions":["1.0.0","1.1.0"]}`)
	f.set("mirror", "v3/package/lib/index.json", 200, `{"versions":["1.0.0","2.0.0-beta.1","2.0.0"]}`)

	w := f.get(http.MethodGet, "v3/package/lib/index.json")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if got := strings.Join(nugetVersionsOf(t, w.Body.Bytes()), " "); got != "1.0.0 1.1.0 2.0.0-beta.1 2.0.0" {
		t.Errorf("versions = %s", got)
	}

	// Only one member has the package.
	f.set("hosted", "v3/package/solo/index.json", 404, "")
	f.set("mirror", "v3/package/solo/index.json", 200, `{"versions":["3.0.0"]}`)

	if w := f.get(http.MethodGet, "v3/package/solo/index.json"); w.Code != http.StatusOK || len(nugetVersionsOf(t, w.Body.Bytes())) != 1 {
		t.Errorf("solo = %d %s", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "v3/package/none/index.json"); w.Code != http.StatusNotFound {
		t.Errorf("unknown package = %d, want 404", w.Code)
	}

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
}

func TestNuGetGroup_UnreadableVersionListLeavesTheMemberOut(t *testing.T) {
	f := nugetGroup(t)
	f.set("hosted", "v3/package/lib/index.json", 200, `{"versions":["1.0.0"]}`)
	// JSON, but not a version list.
	f.set("mirror", "v3/package/lib/index.json", 200, `{"versions":[1]}`)

	w := f.get(http.MethodGet, "v3/package/lib/index.json")
	if w.Code != http.StatusOK || strings.Join(nugetVersionsOf(t, w.Body.Bytes()), " ") != "1.0.0" {
		t.Fatalf("answer %d %s, want the hosted member's versions", w.Code, w.Body.String())
	}

	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store for an answer that lacks a member", cc)
	}

	// Nothing readable at all.
	f.set("hosted", "v3/package/lib/index.json", 404, "")

	if w := f.get(http.MethodGet, "v3/package/lib/index.json"); w.Code != http.StatusBadGateway {
		t.Errorf("only an unreadable list = %d, want 502", w.Code)
	}
}

func TestNuGetGroup_RegistrationsAreMergedAndRewritten(t *testing.T) {
	f := nugetGroup(t)
	f.set("hosted", "v3/registration/lib/index.json", 200, nugetInlineIndex("hosted", "lib", "1.0.0", "1.1.0"))

	// The mirror's registration is paged and not inline, as nuget.org's often is.
	m3 := nugetBase + "/repos/mirror/v3/registration/lib/"
	f.set("mirror", "v3/registration/lib/index.json", 200, fmt.Sprintf(
		`{"@id":"%[1]sindex.json","count":2,"items":[`+
			`{"@id":"%[1]spage/1.0.0/2.0.0.json","count":2,"lower":"1.0.0","upper":"2.0.0","parent":"%[1]sindex.json"},`+
			`{"@id":"%[1]spage/3.0.0/3.0.0.json","count":1,"lower":"3.0.0","upper":"3.0.0","parent":"%[1]sindex.json"}]}`, m3))
	f.set("mirror", "v3/registration/lib/page/1.0.0/2.0.0.json", 200, fmt.Sprintf(
		`{"@id":"%spage/1.0.0/2.0.0.json","count":2,"items":[%s,%s]}`, m3, nugetItem("mirror", "lib", "1.0.0"), nugetItem("mirror", "lib", "2.0.0")))
	f.set("mirror", "v3/registration/lib/page/3.0.0/3.0.0.json", 200, fmt.Sprintf(
		`{"@id":"%spage/3.0.0/3.0.0.json","count":1,"items":[%s]}`, m3, nugetItem("mirror", "lib", "3.0.0")))

	w := f.get(http.MethodGet, "v3/registration/Lib/index.json")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if strings.Contains(w.Body.String(), "/repos/hosted/") || strings.Contains(w.Body.String(), "/repos/mirror/") {
		t.Errorf("member URLs leaked: %s", w.Body.String())
	}

	var doc struct {
		ID    string `json:"@id"`
		Count int    `json:"count"`
		Items []struct {
			ID    string `json:"@id"`
			Lower string `json:"lower"`
			Upper string `json:"upper"`
			Items []struct {
				PackageContent string `json:"packageContent"` //nolint:tagliatelle // NuGet wire format
				CatalogEntry   struct {
					Version string `json:"version"`
					Marker  string `json:"marker"`
				} `json:"catalogEntry"` //nolint:tagliatelle // NuGet wire format
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	idx := nugetBase + "/repos/all/v3/registration/lib/index.json"
	if doc.ID != idx || doc.Count != 1 || len(doc.Items) != 1 || doc.Items[0].ID != idx+"#page/1.0.0/3.0.0" || doc.Items[0].Lower != "1.0.0" || doc.Items[0].Upper != "3.0.0" {
		t.Fatalf("index = %+v", doc)
	}

	want := []struct{ version, marker string }{{"1.0.0", "hosted"}, {"1.1.0", "hosted"}, {"2.0.0", "mirror"}, {"3.0.0", "mirror"}}

	items := doc.Items[0].Items
	if len(items) != len(want) {
		t.Fatalf("items = %+v", items)
	}

	for i, wv := range want {
		it := items[i]
		if it.CatalogEntry.Version != wv.version || it.CatalogEntry.Marker != wv.marker {
			t.Errorf("item %d = %s from %s, want %s from %s", i, it.CatalogEntry.Version, it.CatalogEntry.Marker, wv.version, wv.marker)
		}

		if wantURL := fmt.Sprintf("%s/repos/all/v3/package/lib/%[2]s/lib.%[2]s.nupkg", nugetBase, wv.version); it.PackageContent != wantURL {
			t.Errorf("item %d packageContent = %s, want %s", i, it.PackageContent, wantURL)
		}
	}
}

func TestNuGetGroup_RegistrationMemberFailures(t *testing.T) {
	f := nugetGroup(t)
	f.set("hosted", "v3/registration/lib/index.json", 200, nugetInlineIndex("hosted", "lib", "1.0.0"))

	// The mirror lists a page it then fails to deliver: it is left out whole.
	m3 := nugetBase + "/repos/mirror/v3/registration/lib/"
	f.set("mirror", "v3/registration/lib/index.json", 200, fmt.Sprintf(
		`{"@id":"%[1]sindex.json","count":1,"items":[{"@id":"%[1]spage/2.0.0/2.0.0.json","count":1,"lower":"2.0.0","upper":"2.0.0"}]}`, m3))
	f.set("mirror", "v3/registration/lib/page/2.0.0/2.0.0.json", 500, "boom")

	w := f.get(http.MethodGet, "v3/registration/lib/index.json")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"2.0.0"`) || !strings.Contains(w.Body.String(), `"1.0.0"`) {
		t.Errorf("= %d %s; want the hosted member's registration only", w.Code, w.Body.String())
	}

	// Nobody delivers and someone failed: 502, not 404.
	g := nugetGroup(t)
	g.set("hosted", "v3/registration/x/index.json", 404, "")
	g.set("mirror", "v3/registration/x/index.json", 500, "boom")

	if w := g.get(http.MethodGet, "v3/registration/x/index.json"); w.Code != http.StatusBadGateway {
		t.Errorf("failed member = %d, want 502", w.Code)
	}

	g.set("mirror", "v3/registration/x/index.json", 200, "<html>not json</html>")

	if w := g.get(http.MethodGet, "v3/registration/x/index.json"); w.Code != http.StatusBadGateway {
		t.Errorf("garbage member = %d, want 502", w.Code)
	}

	// A page path traversing out of the registration is not followed.
	h := nugetGroup(t)
	h.set("mirror", "v3/registration/lib/index.json", 200, fmt.Sprintf(
		`{"@id":"%[1]sindex.json","count":1,"items":[{"@id":"%[1]spage/x.json","count":1,"lower":"../../secret","upper":"2.0.0"}]}`, m3))

	if w := h.get(http.MethodGet, "v3/registration/lib/index.json"); w.Code == http.StatusOK {
		t.Errorf("traversing page bounds = %d, want an error", w.Code)
	}
}

func TestNuGetGroup_LeafAndPackageFilesComeFromTheFirstMemberThatHasThem(t *testing.T) {
	f := nugetGroup(t)
	f.set("mirror", "v3/registration/lib/1.0.0.json", 200, `{"@id":"`+nugetBase+`/repos/mirror/v3/registration/lib/1.0.0.json",`+
		`"packageContent":"`+nugetBase+`/repos/mirror/v3/package/lib/1.0.0/lib.1.0.0.nupkg"}`)

	w := f.get(http.MethodGet, "v3/registration/lib/1.0.0.json")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), nugetBase+"/repos/all/v3/package/lib/1.0.0/lib.1.0.0.nupkg") || strings.Contains(w.Body.String(), "/mirror/") {
		t.Errorf("leaf = %d %s", w.Code, w.Body.String())
	}

	f.set("mirror", "v3/package/lib/1.0.0/lib.1.0.0.nupkg", 200, "PK-bytes")

	if w := f.get(http.MethodGet, "v3/package/lib/1.0.0/lib.1.0.0.nupkg"); w.Code != http.StatusOK || w.Body.String() != "PK-bytes" {
		t.Errorf("nupkg = %d %q", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "v3/registration/lib/9.9.9.json"); w.Code != http.StatusNotFound {
		t.Errorf("unknown leaf = %d, want 404", w.Code)
	}
}
