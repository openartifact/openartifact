// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

const ansibleBase = "http://oa.test"

// ansibleFake is a member that answers like a Galaxy API v3 server: the
// collections it holds (FQCN -> versions), a page size it never exceeds, and
// the links of its own host, which the group must not pass on.
type ansibleFake struct {
	collections map[string][]string
	pageSize    int
	fail        bool
}

func (a *ansibleFake) host(name string) string { return "https://" + name + ".invalid/api/v3" }

func (a *ansibleFake) serve(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
	parsed, ok := pkgansible.ParsePath(p)

	switch {
	case a.fail:
		http.Error(w, "boom", http.StatusInternalServerError)
	case !ok:
		http.NotFound(w, r)
	case parsed.Kind == pkgansible.KindIndex:
		a.serveIndex(w, r, m)
	default:
		a.serveCollection(w, r, m, parsed)
	}
}

func (a *ansibleFake) serveCollection(w http.ResponseWriter, r *http.Request, m *models.Repository, p pkgansible.Path) {
	versions := a.collections[pkgansible.FQCN(p.Namespace, p.Name)]
	needsVersion := p.Kind == pkgansible.KindVersion || p.Kind == pkgansible.KindDownload
	if len(versions) == 0 || (needsVersion && !slices.Contains(versions, p.Version)) {
		http.NotFound(w, r)
		return
	}

	root := pkgansible.Links{Root: a.host(m.Name)}

	switch p.Kind {
	case pkgansible.KindCollection:
		writeTestJSON(w, pkgansible.CollectionDoc{Href: root.Collection(p.Namespace, p.Name), Namespace: p.Namespace, Name: p.Name})
	case pkgansible.KindVersions:
		refs := make([]pkgansible.VersionRef, len(versions))
		for i, v := range versions {
			refs[i] = pkgansible.VersionRef{Version: v, Href: root.Version(p.Namespace, p.Name, v), Marks: []string{}}
		}

		a.writePage(w, r, refs)
	case pkgansible.KindVersion:
		sum := sha256.Sum256([]byte(m.Name + p.Version))
		doc := pkgansible.VersionDoc{
			Version: p.Version,
			Href:    root.Version(p.Namespace, p.Name, p.Version),
			Artifact: pkgansible.ArtifactDoc{
				Filename: pkgansible.FileName(p.Namespace, p.Name, p.Version),
				SHA256:   hex.EncodeToString(sum[:]),
			},
			Collection:  pkgansible.CollectionRefDoc{Name: p.Name, Href: root.Collection(p.Namespace, p.Name)},
			DownloadURL: root.Download(pkgansible.FileName(p.Namespace, p.Name, p.Version)),
			Name:        p.Name,
			Namespace:   pkgansible.NamespaceDoc{Name: p.Namespace},
			Metadata:    map[string]any{"origin": m.Name},
		}
		writeTestJSON(w, doc)
	case pkgansible.KindDownload:
		_, _ = w.Write([]byte("tarball-" + m.Name))
	case pkgansible.KindRoot, pkgansible.KindV3, pkgansible.KindIndex:
		http.NotFound(w, r)
	}
}

func (a *ansibleFake) serveIndex(w http.ResponseWriter, r *http.Request, m *models.Repository) {
	names := make([]string, 0, len(a.collections))
	for fq := range a.collections {
		names = append(names, fq)
	}

	slices.Sort(names)

	root := pkgansible.Links{Root: a.host(m.Name)}
	docs := make([]pkgansible.CollectionDoc, len(names))

	for i, fq := range names {
		ns, name, _ := pkgansible.SplitFQCN(fq)
		docs[i] = pkgansible.CollectionDoc{Href: root.Collection(ns, name), Namespace: ns, Name: name, VersionsURL: root.Versions(ns, name)}
	}

	a.writePage(w, r, docs)
}

// writePage answers the window of items the request asks for, in pages of at
// most the member's page size, with a link to the next one.
func (a *ansibleFake) writePage(w http.ResponseWriter, r *http.Request, items any) {
	raw, _ := json.Marshal(items)

	var all []json.RawMessage

	_ = json.Unmarshal(raw, &all)

	limit, offset, err := pkgansible.PageParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if a.pageSize > 0 {
		limit = min(limit, a.pageSize)
	}

	var next any
	if offset+limit < len(all) {
		next = "https://upstream.invalid/next?offset=" + strconv.Itoa(offset+limit)
	}

	writeTestJSON(w, map[string]any{
		"meta":  map[string]int{"count": len(all)},
		"links": map[string]any{"next": next},
		"data":  pkgansible.Window(all, limit, offset),
	})
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ansibleFixture is an ansible group over the members named in fakes.
type ansibleFixture struct {
	f     *fixture
	fakes map[string]*ansibleFake
}

func newAnsibleFixture(t *testing.T, merge bool, members ...*models.Repository) *ansibleFixture {
	t.Helper()

	f := newFixture(t, "ansible", members...)
	f.grp.MergeProxyVersions = merge
	f.h.SetBaseURL(ansibleBase + "/")

	return &ansibleFixture{f: f, fakes: map[string]*ansibleFake{}}
}

func (a *ansibleFixture) add(name string, fake *ansibleFake) { a.fakes[name] = fake }

func (a *ansibleFixture) get(method, path, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/?"+query, nil)
	w := httptest.NewRecorder()
	a.f.h.Serve(w, req, a.f.grp, path, a.backend())

	return w
}

// backend answers the members from the fakes.
func (a *ansibleFixture) backend() Backend {
	return Backend{
		Authorize: func(_ *http.Request, m *models.Repository, _ string) bool { return !a.f.denied[m.Name] },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
			a.f.asked = append(a.f.asked, m.Name)

			fake, ok := a.fakes[m.Name]
			if !ok {
				http.NotFound(w, r)
				return
			}

			fake.serve(w, r, m, p)
		},
	}
}

type ansibleVersionsBody struct {
	Meta  map[string]int `json:"meta"`
	Links map[string]any `json:"links"`
	Data  []pkgansible.VersionRef
}

func (a *ansibleFixture) versions(t *testing.T, query string) ([]string, ansibleVersionsBody) {
	t.Helper()

	w := a.get(http.MethodGet, "api/v3/collections/acme/lib/versions/", query)
	if w.Code != http.StatusOK {
		t.Fatalf("versions = %d: %s", w.Code, w.Body.String())
	}

	var body ansibleVersionsBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	got := make([]string, len(body.Data))
	for i, v := range body.Data {
		got[i] = v.Version
	}

	return got, body
}

func ansibleMembers() []*models.Repository {
	return []*models.Repository{member("hosted", "ansible", "hosted"), member("mirror", "ansible", "proxy")}
}

func TestAnsibleGroup_VersionsAreMergedDedupedNewestFirstAndLinkedToTheGroup(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0", "2.0.0"}}, pageSize: 1})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"acme.lib": {"2.0.0", "3.0.0", "10.0.0"}}, pageSize: 2})

	got, body := a.versions(t, "")
	if want := []string{"10.0.0", "3.0.0", "2.0.0", "1.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("versions = %v, want %v (paged members read to the end, duplicates once)", got, want)
	}

	if body.Meta["count"] != 4 {
		t.Errorf("count = %d, want 4", body.Meta["count"])
	}

	for _, v := range body.Data {
		if want := ansibleBase + "/repos/all/api/v3/collections/acme/lib/versions/" + v.Version + "/"; v.Href != want {
			t.Errorf("href = %q, want %q", v.Href, want)
		}
	}
}

func TestAnsibleGroup_PagesPointAtTheGroup(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0", "2.0.0"}}})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"acme.lib": {"3.0.0", "4.0.0", "5.0.0"}}})

	got, body := a.versions(t, "limit=2&offset=2")
	if want := []string{"3.0.0", "2.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("page = %v, want %v", got, want)
	}

	list := ansibleBase + "/repos/all/api/v3/collections/acme/lib/versions/"
	want := map[string]any{
		"first":    list + "?limit=2&offset=0",
		"previous": list + "?limit=2&offset=0",
		"next":     list + "?limit=2&offset=4",
		"last":     list + "?limit=2&offset=4",
	}

	for k, v := range want {
		if body.Links[k] != v {
			t.Errorf("links.%s = %v, want %v", k, body.Links[k], v)
		}
	}

	if w := a.get(http.MethodGet, "api/v3/collections/acme/lib/versions/", "limit=0"); w.Code != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", w.Code)
	}
}

func TestAnsibleGroup_CollectionDocIsBuiltFromTheMergedList(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}}})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"acme.lib": {"2.0.0", "3.0.0-rc.1"}}})

	w := a.get(http.MethodGet, "api/v3/collections/acme/lib/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("collection = %d: %s", w.Code, w.Body.String())
	}

	var doc pkgansible.CollectionDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	root := ansibleBase + "/repos/all/api/v3"
	if doc.Href != root+"/collections/acme/lib/" || doc.HighestVersion == nil || doc.HighestVersion.Version != "2.0.0" {
		t.Errorf("doc = %+v, want the group's links and the highest release 2.0.0", doc)
	}

	if w := a.get(http.MethodGet, "api/v3/collections/acme/none/", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown collection = %d, want 404", w.Code)
	}
}

func TestAnsibleGroup_VersionDocComesFromTheFirstMemberAndIsRelinked(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}}})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0", "2.0.0"}}})

	for version, origin := range map[string]string{"1.0.0": "hosted", "2.0.0": "mirror"} {
		w := a.get(http.MethodGet, "api/v3/collections/acme/lib/versions/"+version+"/", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", version, w.Code, w.Body.String())
		}

		var doc pkgansible.VersionDoc
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}

		root := ansibleBase + "/repos/all/api/v3"
		sum := sha256.Sum256([]byte(origin + version))

		switch {
		case doc.Metadata["origin"] != origin:
			t.Errorf("%s comes from %v, want %s", version, doc.Metadata["origin"], origin)
		case doc.Href != root+"/collections/acme/lib/versions/"+version+"/",
			doc.Collection.Href != root+"/collections/acme/lib/",
			doc.DownloadURL != root+"/artifacts/collections/acme-lib-"+version+".tar.gz":
			t.Errorf("links not relinked to the group: %+v", doc)
		case doc.Artifact.SHA256 != hex.EncodeToString(sum[:]):
			t.Errorf("artifact digest changed: %s", doc.Artifact.SHA256)
		}
	}

	if w := a.get(http.MethodGet, "api/v3/collections/acme/lib/versions/9.9.9/", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown version = %d, want 404", w.Code)
	}
}

func TestAnsibleGroup_DownloadIsServedByTheMemberThatHasIt(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}}})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0", "2.0.0"}}})

	for file, want := range map[string]string{"acme-lib-1.0.0.tar.gz": "tarball-hosted", "acme-lib-2.0.0.tar.gz": "tarball-mirror"} {
		if w := a.get(http.MethodGet, "api/v3/artifacts/collections/"+file, ""); w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s = %d %q, want %q", file, w.Code, w.Body.String(), want)
		}
	}
}

func TestAnsibleGroup_IsGeneratedForTheDiscoveryDocuments(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)

	w := a.get(http.MethodGet, "api/", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"v3":"v3/"`) {
		t.Errorf("root = %d %s", w.Code, w.Body.String())
	}

	w = a.get(http.MethodGet, "api/v3/", "")
	if !strings.Contains(w.Body.String(), ansibleBase+"/repos/all/api/v3/collections/") ||
		!strings.Contains(w.Body.String(), ansibleBase+"/repos/all/api/v3/artifacts/collections/") {
		t.Errorf("v3 = %d %s", w.Code, w.Body.String())
	}

	if len(a.f.asked) != 0 {
		t.Errorf("members asked for a generated document: %v", a.f.asked)
	}

	if w := a.get(http.MethodGet, "api/v2/", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", w.Code)
	}
}

func TestAnsibleGroup_IndexIsMergedOverHostedMembersOnly(t *testing.T) {
	a := newAnsibleFixture(t, true,
		member("one", "ansible", "hosted"), member("mirror", "ansible", "proxy"), member("two", "ansible", "hosted"))
	a.add("one", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}, "acme.zeta": {"1.0.0"}}, pageSize: 1})
	a.add("two", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}, "other.tools": {"1.0.0"}}})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"upstream.only": {"1.0.0"}}})

	w := a.get(http.MethodGet, "api/v3/collections/", "limit=2")
	if w.Code != http.StatusOK {
		t.Fatalf("index = %d: %s", w.Code, w.Body.String())
	}

	var body struct {
		Meta  map[string]int `json:"meta"`
		Links map[string]any `json:"links"`
		Data  []pkgansible.CollectionDoc
	}

	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, d := range body.Data {
		got = append(got, pkgansible.FQCN(d.Namespace, d.Name))

		if want := ansibleBase + "/repos/all/api/v3/collections/" + d.Namespace + "/" + d.Name + "/"; d.Href != want {
			t.Errorf("href = %q, want %q", d.Href, want)
		}
	}

	if want := []string{"acme.lib", "acme.zeta"}; !slices.Equal(got, want) || body.Meta["count"] != 3 {
		t.Errorf("index = %v (count %d), want %v of 3", got, body.Meta["count"], want)
	}

	if want := ansibleBase + "/repos/all/api/v3/collections/?limit=2&offset=2"; body.Links["next"] != want {
		t.Errorf("next = %v, want %s", body.Links["next"], want)
	}

	if slices.Contains(a.f.asked, "mirror") {
		t.Errorf("proxy asked for the index: %v", a.f.asked)
	}
}

func ansibleHostedWins(t *testing.T) *ansibleFixture {
	t.Helper()

	a := newAnsibleFixture(t, false, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}}})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0", "9.9.9"}, "pub.tools": {"1.0.0"}}})

	return a
}

func TestAnsibleGroup_HostedWinsHidesTheProxysVersionsAndFiles(t *testing.T) {
	a := ansibleHostedWins(t)

	if got, _ := a.versions(t, ""); !slices.Equal(got, []string{"1.0.0"}) {
		t.Errorf("versions = %v, want the hosted 1.0.0 only", got)
	}

	if slices.Contains(a.f.asked, "mirror") {
		t.Errorf("proxy asked for a collection a hosted member holds: %v", a.f.asked)
	}

	for _, p := range []string{"api/v3/collections/acme/lib/versions/9.9.9/", "api/v3/artifacts/collections/acme-lib-9.9.9.tar.gz"} {
		if w := a.get(http.MethodGet, p, ""); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "mirror") {
			t.Errorf("%s = %d %q, want 404", p, w.Code, w.Body.String())
		}
	}

	if slices.Contains(a.f.asked, "mirror") {
		t.Errorf("proxy asked for a version of a hosted collection: %v", a.f.asked)
	}

	if w := a.get(http.MethodGet, "api/v3/artifacts/collections/acme-lib-1.0.0.tar.gz", ""); w.Body.String() != "tarball-hosted" {
		t.Errorf("hosted download = %d %q", w.Code, w.Body.String())
	}
}

func TestAnsibleGroup_HostedWinsLeavesProxyOnlyCollectionsToTheProxy(t *testing.T) {
	a := ansibleHostedWins(t)

	w := a.get(http.MethodGet, "api/v3/collections/pub/tools/versions/1.0.0/", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"origin":"mirror"`) {
		t.Errorf("proxy-only version = %d %s", w.Code, w.Body.String())
	}

	if w := a.get(http.MethodGet, "api/v3/artifacts/collections/pub-tools-1.0.0.tar.gz", ""); w.Body.String() != "tarball-mirror" {
		t.Errorf("proxy-only download = %d %q", w.Code, w.Body.String())
	}
}

func TestAnsibleGroup_MergeProxyVersionsDisablesHostedWins(t *testing.T) {
	a := ansibleHostedWins(t)
	a.f.grp.MergeProxyVersions = true

	if got, _ := a.versions(t, ""); !slices.Equal(got, []string{"9.9.9", "1.0.0"}) {
		t.Errorf("versions = %v, want the union", got)
	}

	if w := a.get(http.MethodGet, "api/v3/artifacts/collections/acme-lib-9.9.9.tar.gz", ""); w.Body.String() != "tarball-mirror" {
		t.Errorf("proxy download = %d %q, want it served", w.Code, w.Body.String())
	}
}

func TestAnsibleGroup_HostedWinsFailsClosedWhenTheHostedMemberFails(t *testing.T) {
	a := ansibleHostedWins(t)
	a.fakes["hosted"].fail = true

	for _, p := range []string{
		"api/v3/collections/acme/lib/", "api/v3/collections/acme/lib/versions/",
		"api/v3/collections/acme/lib/versions/9.9.9/", "api/v3/artifacts/collections/acme-lib-9.9.9.tar.gz",
	} {
		if w := a.get(http.MethodGet, p, ""); w.Code != http.StatusBadGateway {
			t.Errorf("%s with a failing hosted member = %d, want 502", p, w.Code)
		}
	}
}

func TestAnsibleGroup_UnreadableHostedMemberCountsAsAbsent(t *testing.T) {
	a := ansibleHostedWins(t)
	a.f.denied["hosted"] = true

	if got, _ := a.versions(t, ""); !slices.Equal(got, []string{"9.9.9", "1.0.0"}) {
		t.Errorf("versions = %v, want the proxy's", got)
	}
}

func TestAnsibleGroup_FailingMemberYieldsAnUncacheablePartialListOr502(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}}, fail: true})
	a.add("mirror", &ansibleFake{collections: map[string][]string{"acme.lib": {"2.0.0"}}})

	w := a.get(http.MethodGet, "api/v3/collections/acme/lib/versions/", "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
		t.Errorf("partial list = %d cache-control %q etag %q, want 200 no-store without validator",
			w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
	}

	a.fakes["mirror"].fail = true

	for _, p := range []string{
		"api/v3/collections/acme/lib/versions/", "api/v3/collections/acme/lib/", "api/v3/collections/acme/lib/versions/1.0.0/",
		"api/v3/collections/",
	} {
		if w := a.get(http.MethodGet, p, ""); w.Code != http.StatusBadGateway {
			t.Errorf("%s with every member failing = %d, want 502", p, w.Code)
		}
	}
}

func TestAnsibleGroup_ListsAreRevalidatable(t *testing.T) {
	a := newAnsibleFixture(t, true, ansibleMembers()...)
	a.add("hosted", &ansibleFake{collections: map[string][]string{"acme.lib": {"1.0.0"}}})
	a.add("mirror", &ansibleFake{})

	w := a.get(http.MethodGet, "api/v3/collections/acme/lib/versions/", "")

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	rec := httptest.NewRecorder()
	a.f.h.Serve(rec, req, a.f.grp, "api/v3/collections/acme/lib/versions/", Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
			a.fakes[m.Name].serve(w, r, m, p)
		},
	})

	if rec.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", rec.Code)
	}
}
