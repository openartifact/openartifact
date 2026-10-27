// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	pkgconan "github.com/openartifact/openartifact/pkg/conan"
)

const (
	conanBase     = "http://oa.test"
	conanLatest   = "v2/conans/zlib/1.0/_/_/latest"
	conanRevs     = "v2/conans/zlib/1.0/_/_/revisions"
	conanSearch   = "v2/conans/search"
	conanOld      = `{"revision":"aaaa","time":"2025-01-01T00:00:00.000+0000"}`
	conanNew      = `{"revision":"bbbb","time":"2025-06-01T00:00:00.000+0000"}`
	conanV1Digest = "v1/conans/zlib/1.0/_/_/download_urls"
)

func conanGroup(t *testing.T, mergeProxy bool) *fixture {
	t.Helper()

	f := newFixture(t, "conan", member("hosted", "conan", "hosted"), member("mirror", "conan", "proxy"))
	f.grp.MergeProxyVersions = mergeProxy
	f.h.SetBaseURL(conanBase + "/")

	return f
}

func conanResults(t *testing.T, body string) []string {
	t.Helper()

	var doc struct {
		Results []string `json:"results"`
	}

	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("not a search result: %v: %s", err, body)
	}

	return doc.Results
}

func TestConanGroup_LatestIsTheNewestRevisionOfAllMembers(t *testing.T) {
	f := conanGroup(t, true)
	f.set("hosted", conanLatest, 200, conanOld)
	f.set("mirror", conanLatest, 200, conanNew)

	w := f.get(http.MethodGet, conanLatest)

	rev, err := pkgconan.ParseRevision(w.Body.Bytes())
	if w.Code != http.StatusOK || err != nil || rev.ID != "bbbb" {
		t.Errorf("latest = %d %q (%v), want the newest revision bbbb", w.Code, w.Body.String(), err)
	}
}

func TestConanGroup_RevisionsAreMergedNewestFirstWithoutDuplicates(t *testing.T) {
	f := conanGroup(t, true)
	f.set("hosted", conanRevs, 200, `{"reference":"zlib/1.0","revisions":[`+conanOld+`]}`)
	f.set("mirror", conanRevs, 200, `{"reference":"zlib/1.0","revisions":[`+conanOld+`,`+conanNew+`]}`)

	w := f.get(http.MethodGet, conanRevs)

	revs, err := pkgconan.ParseRevisions(w.Body.Bytes())
	if err != nil || w.Code != http.StatusOK || len(revs) != 2 || revs[0].ID != "bbbb" || revs[1].ID != "aaaa" {
		t.Errorf("revisions = %d %q (%v)", w.Code, w.Body.String(), err)
	}
}

func TestConanGroup_MergedDocumentsRevalidate(t *testing.T) {
	f := conanGroup(t, true)
	f.set("mirror", conanLatest, 200, conanNew)

	first := f.get(http.MethodGet, conanLatest)

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("a merged document has no ETag")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, conanLatest, f.backend())

	if w.Code != http.StatusNotModified {
		t.Errorf("conditional request = %d, want 304", w.Code)
	}
}

func TestConanGroup_NoMemberHasItIs404AndAFailingOneIs502(t *testing.T) {
	f := conanGroup(t, true)

	if w := f.get(http.MethodGet, conanLatest); w.Code != http.StatusNotFound {
		t.Errorf("unknown recipe = %d, want 404", w.Code)
	}

	f.set("hosted", conanLatest, 500, "boom")
	f.set("mirror", conanLatest, 200, "<html>portal</html>")

	w := f.get(http.MethodGet, conanLatest)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "boom") || strings.Contains(w.Body.String(), "portal") {
		t.Errorf("failing members = %d %q, want a generic 502", w.Code, w.Body.String())
	}
}

func TestConanGroup_FilesComeFromTheFirstMemberThatHasThem(t *testing.T) {
	f := conanGroup(t, true)
	file := "v2/conans/zlib/1.0/_/_/revisions/aaaa/files/conanfile.py"
	f.set("hosted", file, 200, "from-hosted")
	f.set("mirror", file, 200, "from-mirror")

	if w := f.get(http.MethodGet, file); w.Code != http.StatusOK || w.Body.String() != "from-hosted" {
		t.Errorf("file = %d %q, want the hosted member's", w.Code, w.Body.String())
	}
}

func TestConanGroup_PackageSearchIsMergedAndTheFirstPackageWins(t *testing.T) {
	f := conanGroup(t, true)
	path := "v2/conans/zlib/1.0/_/_/search"
	f.set("hosted", path, 200, `{"p1":{"settings":{"os":"hosted"}}}`)
	f.set("mirror", path, 200, `{"p1":{"settings":{"os":"mirror"}},"p2":{"settings":{"os":"mirror"}}}`)

	w := f.get(http.MethodGet, path)

	var got map[string]struct {
		Settings map[string]string `json:"settings"`
	}

	if err := json.Unmarshal(
		w.Body.Bytes(),
		&got,
	); err != nil || len(got) != 2 || got["p1"].Settings["os"] != "hosted" ||
		got["p2"].Settings["os"] != "mirror" {
		t.Errorf("package search = %d %q (%v)", w.Code, w.Body.String(), err)
	}
}

func TestConanGroup_SearchHidesProxyNamesAHostedMemberHolds(t *testing.T) {
	f := conanGroup(t, false)
	f.set("hosted", conanSearch, 200, `{"results":["zlib/9.9@_/_"]}`)
	f.set("mirror", conanSearch, 200, `{"results":["zlib/1.3.1@_/_","fmt/10.0@_/_"]}`)

	w := f.get(http.MethodGet, conanSearch)
	got := conanResults(t, w.Body.String())

	slices.Sort(got)

	if w.Code != http.StatusOK || !slices.Equal(got, []string{"fmt/10.0@_/_", "zlib/9.9@_/_"}) {
		t.Errorf("search = %d %v, want the hosted zlib and the proxy's other names", w.Code, got)
	}
}

func TestConanGroup_SearchKeepsProxyNamesWhenProxyVersionsAreMerged(t *testing.T) {
	f := conanGroup(t, true)
	f.set("hosted", conanSearch, 200, `{"results":["zlib/9.9@_/_"]}`)
	f.set("mirror", conanSearch, 200, `{"results":["zlib/1.3.1@_/_"]}`)

	got := conanResults(t, f.get(http.MethodGet, conanSearch).Body.String())
	if len(got) != 2 {
		t.Errorf("search = %v, want both versions", got)
	}
}

func TestConanGroup_SearchFailsClosedWhenAHostedMemberCannotSay(t *testing.T) {
	f := conanGroup(t, false)
	f.set("hosted", conanSearch, 500, "boom")
	f.set("mirror", conanSearch, 200, `{"results":["zlib/1.3.1@_/_"]}`)

	if w := f.get(http.MethodGet, conanSearch); w.Code != http.StatusBadGateway {
		t.Errorf("search with an unreadable hosted member = %d %q, want 502", w.Code, w.Body.String())
	}
}

func TestConanGroup_HostedNameHidesTheProxyRevisions(t *testing.T) {
	f := conanGroup(t, false)
	f.set("hosted", conanSearch, 200, `{"results":["zlib/1.0@_/_"]}`)
	f.set("hosted", conanLatest, 200, conanOld)
	f.set("mirror", conanLatest, 200, conanNew)

	w := f.get(http.MethodGet, conanLatest)

	rev, err := pkgconan.ParseRevision(w.Body.Bytes())
	if err != nil || rev.ID != "aaaa" {
		t.Errorf("latest = %d %q (%v), want the hosted revision aaaa: the proxy's newer one must not win", w.Code, w.Body.String(), err)
	}
}

func TestConanGroup_V1URLsPointAtTheGroup(t *testing.T) {
	f := conanGroup(t, true)
	hosted := conanBase + "/repos/hosted/v1/files/zlib/1.0/_/_/0/export/conanfile.py"
	f.set("hosted", conanV1Digest, 200, `{"conanfile.py":"`+hosted+`"}`)

	w := f.get(http.MethodGet, conanV1Digest)

	var urls map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &urls); err != nil || w.Code != http.StatusOK {
		t.Fatalf("download_urls = %d %q", w.Code, w.Body.String())
	}

	if want := conanBase + "/repos/all/v1/files/zlib/1.0/_/_/0/export/conanfile.py"; urls["conanfile.py"] != want {
		t.Errorf("url = %q, want %q", urls["conanfile.py"], want)
	}
}

func TestConanGroup_V1SnapshotIsTheFirstMembersUnchanged(t *testing.T) {
	f := conanGroup(t, true)
	path := "v1/conans/zlib/1.0/_/_"
	f.set("mirror", path, 200, `{"conanfile.py":"d41d8cd98f00b204e9800998ecf8427e"}`)

	if w := f.get(http.MethodGet, path); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "d41d8cd98f00b204e9800998ecf8427e") {
		t.Errorf("snapshot = %d %q", w.Code, w.Body.String())
	}

	f.set("mirror", path, 200, `["not","a","map"]`)

	if w := f.get(http.MethodGet, path); w.Code != http.StatusBadGateway {
		t.Errorf("malformed snapshot = %d, want 502", w.Code)
	}
}

func TestConanGroup_V1SearchIsMergedLikeV2(t *testing.T) {
	f := conanGroup(t, true)
	f.set("hosted", "v1/conans/search", 200, `{"results":["a/1@_/_"]}`)
	f.set("mirror", "v1/conans/search", 200, `{"results":["b/1@_/_"]}`)

	if got := conanResults(t, f.get(http.MethodGet, "v1/conans/search").Body.String()); len(got) != 2 {
		t.Errorf("v1 search = %v, want both members' results", got)
	}
}
