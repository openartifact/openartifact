// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// catalogDB serves a fixed, name-ordered catalog and a set of trashed blobs on
// top of the registry fake.
type catalogDB struct {
	*regFakeDB

	entries []*models.DockerCatalogEntry
	trashed map[string]bool // repoID|digest
	calls   int
}

func (d *catalogDB) ListDockerCatalog(_ context.Context, after string, limit int) ([]*models.DockerCatalogEntry, error) {
	d.calls++

	var out []*models.DockerCatalogEntry

	for _, e := range d.entries {
		if e.Name > after && len(out) < limit {
			out = append(out, e)
		}
	}

	return out, nil
}

func (d *catalogDB) DockerBlobInTrash(_ context.Context, repoID, digest string) (bool, error) {
	return d.trashed[scoped(repoID, digest)], nil
}

// readAuthz lets a principal read what allow admits, like a permission target
// would, and records nothing else.
type readAuthz struct {
	fakeAuthz

	allow func(repoName, path string) bool
}

func (a *readAuthz) PathReadFilter(context.Context, auth.Principal) (func(*models.Repository, string) (bool, error), error) {
	return func(repo *models.Repository, path string) (bool, error) { return a.allow(repo.Name, path), nil }, nil
}

func repoNamed(name string) *models.Repository {
	return &models.Repository{ID: "id-" + name, Name: name, Format: "docker"}
}

// hostedEntry is an image of a hosted repository.
func hostedEntry(repo, image string) *models.DockerCatalogEntry {
	r := repoNamed(repo)
	name := repo

	if image != "" {
		name = repo + "/" + image
	}

	return &models.DockerCatalogEntry{Name: name, Repository: r, Image: image, Sources: []*models.Repository{r}}
}

func newCatalogRegistry(t *testing.T, entries []*models.DockerCatalogEntry, allow func(repoName, path string) bool) (*registry, *catalogDB) {
	t.Helper()

	db := &catalogDB{regFakeDB: newRegFakeDB(), entries: entries, trashed: map[string]bool{}}
	g := newRegistry(t, db)

	if allow != nil {
		g.h.SetAuthorizer(&readAuthz{allow: allow})
	}

	return g, db
}

func decodeCatalog(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Repositories []string `json:"repositories"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v; body=%s", err, rec.Body.String())
	}

	if body.Repositories == nil {
		t.Fatalf("repositories must be an array, got null; body=%s", rec.Body.String())
	}

	return body.Repositories
}

func TestCatalog_ListsReadableImagesOnly(t *testing.T) {
	entries := []*models.DockerCatalogEntry{
		hostedEntry("alpha", ""),
		hostedEntry("alpha", "team/api"),
		hostedEntry("beta", "web"),
		hostedEntry("gamma", "db"),
	}

	// beta is readable entirely, alpha only for its team/ images, gamma not.
	g, _ := newCatalogRegistry(t, entries, func(repo, path string) bool {
		return repo == "beta" || (repo == "alpha" && strings.HasPrefix(path, "team/"))
	})

	got := decodeCatalog(t, g.do(http.MethodGet, "/v2/_catalog", ""))
	if want := []string{"alpha/team/api", "beta/web"}; !slices.Equal(got, want) {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
}

func TestCatalog_DefaultImageIsAuthorizedAsTheRepositoryTagList(t *testing.T) {
	g, _ := newCatalogRegistry(t, []*models.DockerCatalogEntry{hostedEntry("alpha", "")}, func(repo, path string) bool {
		return repo == "alpha" && path == "tags/list"
	})

	if got := decodeCatalog(t, g.do(http.MethodGet, "/v2/_catalog", "")); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("catalog = %v, want [alpha]", got)
	}
}

func TestCatalog_GroupNeedsAReadableMember(t *testing.T) {
	group, member, other := repoNamed("grp"), repoNamed("m1"), repoNamed("m2")
	entry := &models.DockerCatalogEntry{Name: "grp/app", Repository: group, Image: "app", Sources: []*models.Repository{member, other}}

	tests := []struct {
		name  string
		allow func(repo, path string) bool
		want  []string
	}{
		{"group and a member readable", func(repo, _ string) bool { return repo == "grp" || repo == "m2" }, []string{"grp/app"}},
		{"group readable, no member", func(repo, _ string) bool { return repo == "grp" }, []string{}},
		{"member readable, not the group", func(repo, _ string) bool { return repo == "m1" }, []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := newCatalogRegistry(t, []*models.DockerCatalogEntry{entry}, tc.allow)

			if got := decodeCatalog(t, g.do(http.MethodGet, "/v2/_catalog", "")); !slices.Equal(got, tc.want) {
				t.Fatalf("catalog = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCatalog_PaginationFollowsTheLinkHeader(t *testing.T) {
	var entries []*models.DockerCatalogEntry
	for i := range 5 {
		entries = append(entries, hostedEntry(fmt.Sprintf("r%d", i), "img"))
	}

	// r1 is unreadable: pages hold readable names only and never end early.
	g, _ := newCatalogRegistry(t, entries, func(repo, _ string) bool { return repo != "r1" })

	var all []string

	target := "/v2/_catalog?n=2"

	for page := 0; target != ""; page++ {
		if page > 5 {
			t.Fatal("pagination does not terminate")
		}

		rec := g.do(http.MethodGet, target, "")
		names := decodeCatalog(t, rec)

		all = append(all, names...)
		target = ""

		if link := rec.Header().Get("Link"); link != "" {
			if !strings.HasSuffix(link, `>; rel="next"`) || !strings.HasPrefix(link, "</v2/_catalog?") {
				t.Fatalf("Link = %q, want </v2/_catalog?...>; rel=\"next\"", link)
			}

			target = strings.TrimSuffix(strings.TrimPrefix(link, "<"), `>; rel="next"`)

			q, err := url.Parse(target)
			if err != nil || q.Query().Get("n") != "2" || q.Query().Get("last") != names[len(names)-1] {
				t.Fatalf("Link %q must carry n=2 and last=%q (err %v)", link, names[len(names)-1], err)
			}
		}
	}

	if want := []string{"r0/img", "r2/img", "r3/img", "r4/img"}; !slices.Equal(all, want) {
		t.Fatalf("paged catalog = %v, want %v", all, want)
	}
}

func TestCatalog_LastResumesAfterTheGivenName(t *testing.T) {
	g, _ := newCatalogRegistry(t, []*models.DockerCatalogEntry{hostedEntry("a", "x"), hostedEntry("b", "x"), hostedEntry("c", "x")},
		func(string, string) bool { return true })

	rec := g.do(http.MethodGet, "/v2/_catalog?last=a/x", "")
	if got := decodeCatalog(t, rec); !slices.Equal(got, []string{"b/x", "c/x"}) {
		t.Fatalf("catalog after a/x = %v, want [b/x c/x]", got)
	}

	if rec.Header().Get("Link") != "" {
		t.Fatalf("last page must carry no Link, got %q", rec.Header().Get("Link"))
	}
}

func TestCatalog_ScansPastLongUnreadableRuns(t *testing.T) {
	// More unreadable names than one database batch precede the readable one.
	var entries []*models.DockerCatalogEntry
	for i := range catalogBatch*2 + 10 {
		entries = append(entries, hostedEntry(fmt.Sprintf("hidden%05d", i), "img"))
	}

	entries = append(entries, hostedEntry("zz", "visible"))

	g, db := newCatalogRegistry(t, entries, func(repo, _ string) bool { return repo == "zz" })

	if got := decodeCatalog(t, g.do(http.MethodGet, "/v2/_catalog?n=1", "")); !slices.Equal(got, []string{"zz/visible"}) {
		t.Fatalf("catalog = %v, want [zz/visible]", got)
	}

	if db.calls != 3 {
		t.Fatalf("database round trips = %d, want 3 (one per batch)", db.calls)
	}
}

func TestCatalog_PageSizeValidation(t *testing.T) {
	g, db := newCatalogRegistry(t, []*models.DockerCatalogEntry{hostedEntry("a", "x")}, func(string, string) bool { return true })

	for _, n := range []string{"abc", "-1"} {
		rec := g.do(http.MethodGet, "/v2/_catalog?n="+n, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "PAGINATION_NUMBER_INVALID") {
			t.Fatalf("n=%s: status %d body %s, want 400 PAGINATION_NUMBER_INVALID", n, rec.Code, rec.Body.String())
		}
	}

	if got := decodeCatalog(t, g.do(http.MethodGet, "/v2/_catalog?n=0", "")); len(got) != 0 {
		t.Fatalf("n=0 catalog = %v, want empty", got)
	}

	if db.calls != 0 {
		t.Fatalf("n=0 and invalid n must not query the catalog, got %d calls", db.calls)
	}
}

func TestCatalog_FailsClosedWithoutACatalogAuthorizer(t *testing.T) {
	g, _ := newCatalogRegistry(t, []*models.DockerCatalogEntry{hostedEntry("a", "x")}, nil)

	if rec := g.do(http.MethodGet, "/v2/_catalog", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("without authorizer: status = %d, want 501; body=%s", rec.Code, rec.Body.String())
	}

	g.h.SetAuthorizer(&fakeAuthz{allow: true}) // Evaluate only: cannot filter a listing

	if rec := g.do(http.MethodGet, "/v2/_catalog", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("with a non-filtering authorizer: status = %d, want 501", rec.Code)
	}
}

// TestBlobTrash covers GET and HEAD by digest: a blob only trashed manifests
// hold is unknown until restored, one a live manifest shares is served.
func TestBlobTrash(t *testing.T) {
	layer := []byte("a layer")
	digest := digestOf(layer)

	db := &catalogDB{regFakeDB: newRegFakeDB("reg"), trashed: map[string]bool{}}
	g := newRegistry(t, db)

	if rec := g.pushBlob("reg/app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("push blob = %d; body=%s", rec.Code, rec.Body.String())
	}

	path := "/v2/reg/app/blobs/" + digest
	status := func(method string) int { return g.do(method, path, "").Code }

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if got := status(m); got != http.StatusOK {
			t.Fatalf("%s live blob = %d, want 200", m, got)
		}
	}

	db.trashed[scoped("id-reg", digest)] = true

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if got := status(m); got != http.StatusNotFound {
			t.Fatalf("%s trashed blob = %d, want 404", m, got)
		}
	}

	if body := g.do(http.MethodGet, path, "").Body.String(); !strings.Contains(body, "BLOB_UNKNOWN") {
		t.Fatalf("trashed blob body = %s, want BLOB_UNKNOWN", body)
	}

	delete(db.trashed, scoped("id-reg", digest)) // restored, or shared with a live manifest

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if got := status(m); got != http.StatusOK {
			t.Fatalf("%s restored blob = %d, want 200", m, got)
		}
	}
}

func TestBlobTrash_TrashedBlobIsNotMountedElsewhere(t *testing.T) {
	layer := []byte("mountable layer")
	digest := digestOf(layer)

	db := &catalogDB{regFakeDB: newRegFakeDB("dst", "src"), trashed: map[string]bool{scoped("id-src", digest): true}}
	db.blobs[scoped("id-src", digest)] = &models.DockerBlob{
		ID: "b1", RepositoryID: "id-src", Digest: digest, Size: int64(len(layer)), BlobSHA256: strings.TrimPrefix(digest, "sha256:"),
	}

	g := newRegistry(t, db)
	g.h.SetAuthorizer(&fakeAuthz{allow: true})

	rec := g.do(http.MethodPost, "/v2/dst/app/blobs/uploads/?mount="+digest+"&from=src/app", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("mount of a trashed source blob = %d, want 202 (upload fallback)", rec.Code)
	}

	if db.blobs[scoped("id-dst", digest)] != nil {
		t.Fatal("trashed blob must not be linked into the target repository")
	}

	delete(db.trashed, scoped("id-src", digest))

	if rec := g.do(http.MethodPost, "/v2/dst/app/blobs/uploads/?mount="+digest+"&from=src/app", ""); rec.Code != http.StatusCreated {
		t.Fatalf("mount after restore = %d, want 201", rec.Code)
	}
}
