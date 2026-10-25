// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/logger"
)

// mavenRepoDB is an in-memory hosted maven repository with license scans.
type mavenRepoDB struct {
	databasetest.BaseMockDB

	policies []*models.LicensePolicy
	// freePolicyReads is how many policy reads answer "no policies" first: the
	// policy that blocks a license later found through a parent must not also
	// reject that parent's own upload.
	freePolicyReads int
	arts            map[string]*models.MavenArtifact // by repository id + "|" + repo path
	scans           map[string]*models.LicenseScanResult
	repos           map[string]*models.Repository // by name
	// groupMembers maps a group's id to its members, in priority order.
	groupMembers map[string][]*models.Repository
	// proxyCache maps a proxy repository id + "|" + path to its cached file.
	proxyCache map[string]*models.ProxyCacheEntry
	nextID     int
}

func newMavenRepoDB() *mavenRepoDB {
	d := &mavenRepoDB{
		arts: map[string]*models.MavenArtifact{}, scans: map[string]*models.LicenseScanResult{},
		repos: map[string]*models.Repository{}, groupMembers: map[string][]*models.Repository{},
		proxyCache: map[string]*models.ProxyCacheEntry{},
	}
	d.addRepo("r", "repo-1", "hosted")

	return d
}

func (d *mavenRepoDB) addRepo(name, id, repoType string) *models.Repository {
	r := &models.Repository{ID: id, Name: name, Format: "maven", RepoType: repoType}
	d.repos[name] = r

	return r
}

func (d *mavenRepoDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return d.repos[name], nil
}

func (d *mavenRepoDB) ListGroupsOfMember(_ context.Context, memberID string) ([]*models.Repository, error) {
	var out []*models.Repository

	for _, g := range d.repos {
		for _, m := range d.groupMembers[g.ID] {
			if m.ID == memberID {
				out = append(out, g)
			}
		}
	}

	return out, nil
}

func (d *mavenRepoDB) ListGroupMemberRepositories(_ context.Context, groupID string) ([]*models.Repository, error) {
	return d.groupMembers[groupID], nil
}

func (d *mavenRepoDB) GetProxyCacheEntry(_ context.Context, repoID, key string) (*models.ProxyCacheEntry, error) {
	return d.proxyCache[repoID+"|"+key], nil
}

func (d *mavenRepoDB) GetOrCreateStorageQuota(context.Context, string) (*models.StorageQuota, error) {
	return &models.StorageQuota{}, nil
}

func (d *mavenRepoDB) ReserveStorageUsage(context.Context, string, int64, int) (bool, error) {
	return true, nil
}

func (d *mavenRepoDB) UpsertBlob(_ context.Context, sha256, _ string, _ int64) (string, error) {
	return "blob-" + sha256, nil
}

func (d *mavenRepoDB) GetMavenArtifactByPath(_ context.Context, repoID, path string) (*models.MavenArtifact, error) {
	return d.arts[repoID+"|"+path], nil
}

func (d *mavenRepoDB) GetLatestSnapshotFile(context.Context, string, string, string, string, string, string) (*models.MavenArtifact, error) {
	return nil, nil
}

func (d *mavenRepoDB) CreateMavenArtifact(_ context.Context, a *models.MavenArtifact) error {
	d.nextID++
	a.ID = "art-" + strconv.Itoa(d.nextID)
	d.arts[a.RepositoryID+"|"+a.RepoPath] = a

	return nil
}

func (d *mavenRepoDB) GetMavenArtifactsByGAV(_ context.Context, repoID, g, a, v string) ([]*models.MavenArtifact, error) {
	var out []*models.MavenArtifact

	for _, art := range d.arts {
		if art.RepositoryID == repoID && art.GroupID == g && art.ArtifactCoord == a && art.Version == v {
			out = append(out, art)
		}
	}

	return out, nil
}

func (d *mavenRepoDB) ListMavenArtifactsByRepo(_ context.Context, repoID string) ([]*models.MavenArtifact, error) {
	var out []*models.MavenArtifact

	for _, art := range d.arts {
		if art.RepositoryID == repoID {
			out = append(out, art)
		}
	}

	return out, nil
}

func (d *mavenRepoDB) ListLicensePolicies(context.Context, string) ([]*models.LicensePolicy, error) {
	if d.freePolicyReads > 0 {
		d.freePolicyReads--
		return nil, nil
	}

	return d.policies, nil
}

func (d *mavenRepoDB) GetLicenseScanResult(_ context.Context, _, id string) (*models.LicenseScanResult, error) {
	if r := d.scans[id]; r != nil {
		cp := *r
		return &cp, nil
	}

	return nil, nil
}

func (d *mavenRepoDB) CreateLicenseScanResult(_ context.Context, r *models.LicenseScanResult) error {
	cp := *r
	cp.ID = "scan-" + r.ArtifactID
	d.scans[r.ArtifactID] = &cp

	return nil
}

func (d *mavenRepoDB) ListLicenseScanResults(_ context.Context, repoID string) ([]*models.LicenseScanResult, error) {
	var out []*models.LicenseScanResult

	for _, s := range d.scans {
		if s.RepositoryID == repoID {
			out = append(out, s)
		}
	}

	return out, nil
}

func (d *mavenRepoDB) ListArtifactProperties(context.Context, string, string) (map[string]string, error) {
	return nil, nil
}

func (d *mavenRepoDB) GetRepoComponentBlob(_ context.Context, repoID, id string) (*models.ComponentBlob, error) {
	for _, a := range d.arts {
		if a.ID == id && a.RepositoryID == repoID {
			return &models.ComponentBlob{ID: id, Format: "maven", Filename: a.ArtifactCoord + "-" + a.Version + "." + a.Extension, SHA256: a.SHA256}, nil
		}
	}

	return nil, nil
}

type mavenParentEnv struct {
	t      *testing.T
	db     *mavenRepoDB
	blobs  *formattest.BlobStore
	router chi.Router
}

func newMavenParentEnv(t *testing.T) *mavenParentEnv {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db, blobs := newMavenRepoDB(), formattest.NewBlobStore()
	router := chi.NewRouter()
	NewMavenUploadHandler(log, db, blobs, nil).Register(router)

	return &mavenParentEnv{t: t, db: db, blobs: blobs, router: router}
}

func (e *mavenParentEnv) put(group, artifact, version, body string) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.putIn("r", group, artifact, version, body)
}

func (e *mavenParentEnv) putIn(repo, group, artifact, version, body string) *httptest.ResponseRecorder {
	e.t.Helper()

	path := "/repos/maven/" + repo + "/" + strings.ReplaceAll(group, ".", "/") + "/" + artifact + "/" + version + "/" + artifact + "-" + version + ".pom"
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httptest.NewRequestWithContext(e.t.Context(), http.MethodPut, path, strings.NewReader(body)))

	return rec
}

// scanLicenses is what the on-demand license scan of the pom resolves to.
func (e *mavenParentEnv) scanLicenses(id string) []string {
	e.t.Helper()

	log, _ := logger.New("error", "json", "stderr")
	src := artifactLicenseSource{db: e.db, blobs: e.blobs, log: log, detector: nil}

	got, err := src.fromFile(e.t.Context(), "repo-1", id)
	if err != nil {
		e.t.Fatalf("fromFile: %v", err)
	}

	return got
}

func (e *mavenParentEnv) scanOf(id string) *models.LicenseScanResult {
	return e.db.scans[id]
}

// cacheInProxy stores pom as an already proxied file of the proxy repository.
func (e *mavenParentEnv) cacheInProxy(proxy *models.Repository, group, artifact, version, pom string) {
	path := strings.ReplaceAll(group, ".", "/") + "/" + artifact + "/" + version + "/" + artifact + "-" + version + ".pom"
	sha := "proxy-" + artifact + "-" + version
	e.blobs.Blobs[sha] = []byte(pom)
	e.db.proxyCache[proxy.ID+"|"+path] = &models.ProxyCacheEntry{RepositoryID: proxy.ID, CacheKey: path, BlobKey: sha, StatusCode: http.StatusOK}
}

func pomXML(group, artifact, version, packaging, parent, licenses string) string {
	out := "<project><modelVersion>4.0.0</modelVersion>"
	if parent != "" {
		out += "<parent>" + parent + "</parent>"
	}

	out += "<groupId>" + group + "</groupId><artifactId>" + artifact + "</artifactId><version>" + version + "</version>"
	if packaging != "" {
		out += "<packaging>" + packaging + "</packaging>"
	}

	if licenses != "" {
		out += "<licenses><license><name>" + licenses + "</name></license></licenses>"
	}

	return out + "</project>"
}

func parentRef(g, a, v string) string {
	return "<groupId>" + g + "</groupId><artifactId>" + a + "</artifactId><version>" + v + "</version>"
}

func TestMavenPut_InheritsParentLicense(t *testing.T) {
	e := newMavenParentEnv(t)

	if rec := e.put("com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "Apache-2.0")); rec.Code != http.StatusCreated {
		t.Fatalf("parent = %d: %s", rec.Code, rec.Body.String())
	}

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("child = %d: %s", rec.Code, rec.Body.String())
	}

	id := rec.Header().Get("X-Oa-Artifact-Id")
	if got := e.scanOf(id); got == nil || !slices.Equal(got.Licenses, []string{"Apache-2.0"}) {
		t.Fatalf("recorded scan = %+v, want Apache-2.0", got)
	}

	if got := e.scanLicenses(id); !slices.Equal(got, []string{"Apache-2.0"}) {
		t.Fatalf("scan licenses = %v, want [Apache-2.0]", got)
	}
}

func TestMavenPut_OwnLicenseWinsOverParent(t *testing.T) {
	e := newMavenParentEnv(t)
	e.put("com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "Apache-2.0"))

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), "MIT"))
	if got := e.scanLicenses(rec.Header().Get("X-Oa-Artifact-Id")); !slices.Equal(got, []string{"MIT"}) {
		t.Fatalf("licenses = %v, want [MIT]", got)
	}
}

func TestMavenPut_InheritsThroughGrandparent(t *testing.T) {
	e := newMavenParentEnv(t)
	e.put("com.acme", "root", "1", pomXML("com.acme", "root", "1", "pom", "", "BSD-3-Clause"))
	e.put("com.acme", "mid", "1", pomXML("com.acme", "mid", "1", "pom", parentRef("com.acme", "root", "1"), ""))

	rec := e.put("com.acme", "leaf", "1", pomXML("com.acme", "leaf", "1", "", parentRef("com.acme", "mid", "1"), ""))
	if got := e.scanLicenses(rec.Header().Get("X-Oa-Artifact-Id")); !slices.Equal(got, []string{"BSD-3-Clause"}) {
		t.Fatalf("licenses = %v, want [BSD-3-Clause]", got)
	}
}

func TestMavenPut_ParentCycleTerminates(t *testing.T) {
	e := newMavenParentEnv(t)
	// a -> b -> a: b is uploaded while a is stored, and a is then re-uploaded as a
	// child of b through a distinct version so the chain loops.
	e.put("com.acme", "a", "1", pomXML("com.acme", "a", "1", "pom", parentRef("com.acme", "b", "1"), ""))
	e.put("com.acme", "b", "1", pomXML("com.acme", "b", "1", "pom", parentRef("com.acme", "a", "1"), ""))

	rec := e.put("com.acme", "c", "1", pomXML("com.acme", "c", "1", "", parentRef("com.acme", "a", "1"), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("child of a cycle = %d: %s", rec.Code, rec.Body.String())
	}

	if got := e.scanLicenses(rec.Header().Get("X-Oa-Artifact-Id")); len(got) != 0 {
		t.Fatalf("licenses = %v, want none", got)
	}
}

func TestMavenPut_SelfParentTerminates(t *testing.T) {
	e := newMavenParentEnv(t)

	rec := e.put("com.acme", "loop", "1", pomXML("com.acme", "loop", "1", "pom", parentRef("com.acme", "loop", "1"), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("self parent = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMavenPut_DeepChainIsBounded(t *testing.T) {
	e := newMavenParentEnv(t)
	// A 14 level chain with the license at the root: deeper than the bound.
	e.put("com.acme", "p0", "1", pomXML("com.acme", "p0", "1", "pom", "", "Apache-2.0"))

	for i := 1; i <= 13; i++ {
		prev := "p" + strconv.Itoa(i-1)
		cur := "p" + strconv.Itoa(i)
		e.put("com.acme", cur, "1", pomXML("com.acme", cur, "1", "pom", parentRef("com.acme", prev, "1"), ""))
	}

	rec := e.put("com.acme", "leaf", "1", pomXML("com.acme", "leaf", "1", "", parentRef("com.acme", "p13", "1"), ""))
	if got := e.scanLicenses(rec.Header().Get("X-Oa-Artifact-Id")); len(got) != 0 {
		t.Fatalf("licenses past the depth bound = %v, want none", got)
	}
}

func TestMavenPut_MissingParentThenLaterUpload(t *testing.T) {
	e := newMavenParentEnv(t)

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("child without parent = %d: %s", rec.Code, rec.Body.String())
	}

	id := rec.Header().Get("X-Oa-Artifact-Id")
	if got := e.scanOf(id); got == nil || len(got.Licenses) != 0 {
		t.Fatalf("unresolved scan = %+v, want an empty result", got)
	}

	// A grandchild waiting on the same missing chain.
	grand := e.put("com.acme", "grand", "1", pomXML("com.acme", "grand", "1", "", parentRef("com.acme", "child", "1"), ""))
	gid := grand.Header().Get("X-Oa-Artifact-Id")

	if rec := e.put("com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "EPL-2.0")); rec.Code != http.StatusCreated {
		t.Fatalf("parent = %d: %s", rec.Code, rec.Body.String())
	}

	for _, artID := range []string{id, gid} {
		if got := e.scanOf(artID); got == nil || !slices.Equal(got.Licenses, []string{"EPL-2.0"}) {
			t.Errorf("scan of %s after the parent upload = %+v, want EPL-2.0", artID, got)
		}
	}
}

func TestMavenPut_LaterParentQuarantinesBlockedChild(t *testing.T) {
	e := newMavenParentEnv(t)
	e.db.policies = []*models.LicensePolicy{{ID: "p1", Name: "no-gpl", Action: "block", Enabled: true, DeniedLicenses: []string{"GPL-3.0-only"}}}

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("child = %d: %s", rec.Code, rec.Body.String())
	}

	e.db.freePolicyReads = 1
	e.put("com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "GPL-3.0-only"))

	if got := e.scanOf(rec.Header().Get("X-Oa-Artifact-Id")); got == nil || !got.Quarantined {
		t.Fatalf("child scan = %+v, want quarantined", got)
	}
}

func TestMavenPut_InheritedBlockedLicenseIsRejected(t *testing.T) {
	e := newMavenParentEnv(t)
	// The parent is stored before the policy exists.
	e.put("com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "GPL-3.0-only"))
	e.db.policies = []*models.LicensePolicy{{ID: "p1", Name: "no-gpl", Action: "block", Enabled: true, DeniedLicenses: []string{"GPL-3.0-only"}}}

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("child inheriting a blocked license = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// groupEnv is a group "g" of a hosted repository "r", a second hosted
// repository "other" and a proxy "central", in that priority order.
func newGroupEnv(t *testing.T) (*mavenParentEnv, *models.Repository) {
	t.Helper()

	e := newMavenParentEnv(t)
	other := e.db.addRepo("other", "repo-2", "hosted")
	central := e.db.addRepo("central", "repo-3", "proxy")
	g := e.db.addRepo("g", "repo-4", "group")
	e.db.groupMembers[g.ID] = []*models.Repository{e.db.repos["r"], other, central}

	return e, central
}

func TestMavenPut_ParentFromProxyCacheInGroup(t *testing.T) {
	e, central := newGroupEnv(t)
	e.cacheInProxy(central, "org.spring", "starter-parent", "3.2.0", pomXML("org.spring", "starter-parent", "3.2.0", "pom", "", "Apache-2.0"))

	rec := e.put("com.acme", "app", "1", pomXML("com.acme", "app", "1", "", parentRef("org.spring", "starter-parent", "3.2.0"), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("child = %d: %s", rec.Code, rec.Body.String())
	}

	id := rec.Header().Get("X-Oa-Artifact-Id")
	if got := e.scanOf(id); got == nil || !slices.Equal(got.Licenses, []string{"Apache-2.0"}) {
		t.Fatalf("scan = %+v, want Apache-2.0 from the proxy cache", got)
	}

	if got := e.scanLicenses(id); !slices.Equal(got, []string{"Apache-2.0"}) {
		t.Fatalf("scan path licenses = %v, want [Apache-2.0]", got)
	}
}

func TestMavenPut_ParentFromHostedGroupMember(t *testing.T) {
	e, _ := newGroupEnv(t)
	e.putIn("other", "com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "MIT"))

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	if got := e.scanLicenses(rec.Header().Get("X-Oa-Artifact-Id")); !slices.Equal(got, []string{"MIT"}) {
		t.Fatalf("licenses = %v, want [MIT]", got)
	}
}

func TestMavenPut_ParentNotInAnyGroupIsNotFound(t *testing.T) {
	e := newMavenParentEnv(t)
	other := e.db.addRepo("other", "repo-2", "hosted")
	_ = other
	// "other" shares no group with "r": its pom is not a candidate parent.
	e.putIn("other", "com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "MIT"))

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	if got := e.scanLicenses(rec.Header().Get("X-Oa-Artifact-Id")); len(got) != 0 {
		t.Fatalf("licenses = %v, want none", got)
	}
}

func TestMavenPut_ParentLandsInGroupMemberLater(t *testing.T) {
	e, _ := newGroupEnv(t)

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	id := rec.Header().Get("X-Oa-Artifact-Id")

	if got := e.scanOf(id); got == nil || len(got.Licenses) != 0 {
		t.Fatalf("unresolved scan = %+v, want empty", got)
	}

	e.putIn("other", "com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "EPL-2.0"))

	if got := e.scanOf(id); got == nil || !slices.Equal(got.Licenses, []string{"EPL-2.0"}) {
		t.Fatalf("scan after the parent landed in a group member = %+v, want EPL-2.0", got)
	}
}

func TestMavenPut_ParentCachedByProxyLaterIsFoundByRescan(t *testing.T) {
	e, central := newGroupEnv(t)

	rec := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("org.spring", "starter-parent", "3.2.0"), ""))
	id := rec.Header().Get("X-Oa-Artifact-Id")

	if got := e.scanLicenses(id); len(got) != 0 {
		t.Fatalf("licenses before the proxy cached the parent = %v, want none", got)
	}

	e.cacheInProxy(central, "org.spring", "starter-parent", "3.2.0", pomXML("org.spring", "starter-parent", "3.2.0", "pom", "", "Apache-2.0"))

	if got := e.scanLicenses(id); !slices.Equal(got, []string{"Apache-2.0"}) {
		t.Fatalf("licenses after the proxy cached the parent = %v, want [Apache-2.0]", got)
	}
}
