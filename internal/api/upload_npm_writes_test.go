// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/middleware"
)

const (
	npmTestRepoID  = "repo-1"
	npmOtherRepoID = "repo-2"
)

// npmMemDB is an in-memory npm store over two repositories, "myrepo" and
// "otherrepo", so scoping of package lookups can be asserted.
type npmMemDB struct {
	*formattest.DB

	mu       sync.Mutex
	other    *models.Repository
	pkgs     []*models.NPMPackage
	versions map[string][]*models.NPMVersion // package ID -> live versions
	tags     map[string]map[string]string    // package ID -> tag -> version

	released     []int64 // sizes passed to DecrementStorageUsage
	stale        int
	deletedPkgs  []string
	createVerErr error
	createPkgErr error
	fail         map[string]error // method name -> error to return
}

// ListLicensePolicies is not exercised by the tests.
func (*npmMemDB) ListLicensePolicies(context.Context, string) ([]*models.LicensePolicy, error) {
	return nil, nil
}

// failure returns the error injected for the named database method.
func (d *npmMemDB) failure(method string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.fail[method]
}

func newNPMMemDB() *npmMemDB {
	d := &npmMemDB{
		DB:       formattest.NewDB(),
		other:    &models.Repository{ID: npmOtherRepoID, Name: "otherrepo"},
		versions: map[string][]*models.NPMVersion{},
		tags:     map[string]map[string]string{},
	}
	d.Repo.ID = npmTestRepoID

	return d
}

func (d *npmMemDB) GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error) {
	if name == d.other.Name {
		return d.other, nil
	}

	return d.DB.GetRepositoryByName(ctx, name)
}

// addPackage seeds a package of repoID with versions (age = how long ago they
// were published) and the given dist-tags.
func (d *npmMemDB) addPackage(repoID, name string, age time.Duration, tags map[string]string, versions ...string) *models.NPMPackage {
	d.mu.Lock()
	defer d.mu.Unlock()

	pkg := &models.NPMPackage{ID: fmt.Sprintf("pkg-%d", len(d.pkgs)+1), RepositoryID: repoID, FullName: name, Name: name}
	d.pkgs = append(d.pkgs, pkg)
	d.tags[pkg.ID] = map[string]string{}

	maps.Copy(d.tags[pkg.ID], tags)

	for _, v := range versions {
		d.versions[pkg.ID] = append(d.versions[pkg.ID], &models.NPMVersion{
			ID: pkg.ID + "-" + v, PackageID: pkg.ID, Version: v,
			TarballFilename: strings.TrimPrefix(name[strings.LastIndex(name, "/")+1:], "") + "-" + v + ".tgz",
			BlobSizeBytes:   100, CreatedAt: time.Now().Add(-age),
		})
	}

	return pkg
}

func (d *npmMemDB) GetNPMPackageByName(_ context.Context, repoID, name string) (*models.NPMPackage, error) {
	if err := d.failure("GetNPMPackageByName"); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, p := range d.pkgs {
		if p.RepositoryID == repoID && p.FullName == name {
			return p, nil
		}
	}

	return nil, nil
}

func (d *npmMemDB) CreateNPMPackage(_ context.Context, p *models.NPMPackage) (*models.NPMPackage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.createPkgErr != nil {
		return nil, d.createPkgErr
	}

	p.ID = fmt.Sprintf("pkg-%d", len(d.pkgs)+1)
	d.pkgs = append(d.pkgs, p)
	d.tags[p.ID] = map[string]string{}

	return p, nil
}

func (d *npmMemDB) GetNPMVersion(_ context.Context, pkgID, version string) (*models.NPMVersion, error) {
	if err := d.failure("GetNPMVersion"); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, v := range d.versions[pkgID] {
		if v.Version == version {
			return v, nil
		}
	}

	return nil, nil
}

func (d *npmMemDB) GetNPMVersionsByPackage(_ context.Context, pkgID string) ([]*models.NPMVersion, error) {
	if err := d.failure("GetNPMVersionsByPackage"); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.versions[pkgID]), nil
}

func (d *npmMemDB) CreateNPMVersion(_ context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.createVerErr != nil {
		return nil, d.createVerErr
	}

	d.versions[v.PackageID] = append(d.versions[v.PackageID], v)

	return v, nil
}

func (d *npmMemDB) DeleteNPMVersion(_ context.Context, pkgID, version string) error {
	if err := d.failure("DeleteNPMVersion"); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.versions[pkgID] = slices.DeleteFunc(d.versions[pkgID], func(v *models.NPMVersion) bool { return v.Version == version })

	return nil
}

func (d *npmMemDB) DeleteNPMPackage(_ context.Context, pkgID string) error {
	if err := d.failure("DeleteNPMPackage"); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.deletedPkgs = append(d.deletedPkgs, pkgID)

	return nil
}

func (d *npmMemDB) GetNPMDistTags(_ context.Context, pkgID string) (map[string]string, error) {
	if err := d.failure("GetNPMDistTags"); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	out := map[string]string{}

	maps.Copy(out, d.tags[pkgID])

	return out, nil
}

func (d *npmMemDB) UpsertNPMDistTag(_ context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	if err := d.failure("UpsertNPMDistTag"); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.tags[t.PackageID][t.Tag] = t.Version

	return t, nil
}

func (d *npmMemDB) DeleteNPMDistTag(_ context.Context, pkgID, tag string) error {
	if err := d.failure("DeleteNPMDistTag"); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.tags[pkgID], tag)

	return nil
}

func (d *npmMemDB) MarkPackumentStale(context.Context, string) error {
	if err := d.failure("MarkPackumentStale"); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.stale++

	return nil
}

func (d *npmMemDB) ReserveStorageUsage(context.Context, string, int64, int) (bool, error) {
	return true, nil
}

func (d *npmMemDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.released = append(d.released, size)

	return nil
}

func (d *npmMemDB) UpsertBlob(_ context.Context, sha string, _ string, _ int64) (string, error) {
	return "blob-" + sha, nil
}

func (d *npmMemDB) liveVersions(pkgID string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]string, 0, len(d.versions[pkgID]))
	for _, v := range d.versions[pkgID] {
		out = append(out, v.Version)
	}

	slices.Sort(out)

	return out
}

func (d *npmMemDB) tagsOf(pkgID string) map[string]string {
	tags, _ := d.GetNPMDistTags(context.Background(), pkgID)
	return tags
}

func (d *npmMemDB) staleCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.stale
}

type npmEnv struct {
	db     *npmMemDB
	h      *NPMUploadHandler
	blobs  *formattest.BlobStore
	events *formattest.Publisher
}

func newNPMEnv(t *testing.T) *npmEnv {
	t.Helper()

	db := newNPMMemDB()
	blobs := formattest.NewBlobStore()
	events := &formattest.Publisher{}
	db.fail = map[string]error{}
	h := NewNPMUploadHandler(formattest.NewLogger(t), db, blobs, nil)
	h.SetPublisher(events)

	return &npmEnv{db: db, h: h, blobs: blobs, events: events}
}

// do sends one request through the handler's routes as principal (nil: no
// principal in the context).
func (e *npmEnv) do(principal *auth.Principal, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if principal != nil {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), principal))
	}

	rw := httptest.NewRecorder()
	router := chi.NewRouter()
	e.h.Register(router)
	router.ServeHTTP(rw, req)

	return rw
}

func npmUser() *auth.Principal { return &auth.Principal{Username: "alice"} }

func npmAnonymous() *auth.Principal { return &auth.Principal{IsAnonymous: true} }

func TestNPMDistTagWrites(t *testing.T) {
	const tagURL = "/repos/myrepo/-/package/demo/dist-tags/"

	tests := []struct {
		name      string
		principal *auth.Principal
		method    string
		target    string
		body      string
		want      int
		wantTags  map[string]string // demo's tags afterwards; nil skips the check
		wantStale bool
	}{
		{
			"put without a principal",
			nil,
			http.MethodPut,
			tagURL + "beta",
			`"2.0.0"`,
			http.StatusUnauthorized,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"put as anonymous",
			npmAnonymous(),
			http.MethodPut,
			tagURL + "beta",
			`"2.0.0"`,
			http.StatusUnauthorized,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"put moves a tag",
			npmUser(),
			http.MethodPut,
			tagURL + "beta",
			`"2.0.0"`,
			http.StatusOK,
			map[string]string{"latest": "1.0.0", "beta": "2.0.0", "stable": "1.0.0"},
			true,
		},
		{
			"put may repoint latest",
			npmUser(),
			http.MethodPut,
			tagURL + "latest",
			`"2.0.0"`,
			http.StatusOK,
			map[string]string{"latest": "2.0.0", "stable": "1.0.0"},
			true,
		},
		{
			"put a version-shaped tag",
			npmUser(),
			http.MethodPut,
			tagURL + "1.2.3",
			`"2.0.0"`,
			http.StatusBadRequest,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"put to an unknown version",
			npmUser(),
			http.MethodPut,
			tagURL + "beta",
			`"9.9.9"`,
			http.StatusNotFound,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"put a non-string body",
			npmUser(),
			http.MethodPut,
			tagURL + "beta",
			`{"v":"2.0.0"}`,
			http.StatusBadRequest,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"put an oversized body",
			npmUser(),
			http.MethodPut,
			tagURL + "beta",
			`"` + strings.Repeat("9", 2048) + `"`,
			http.StatusBadRequest,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"put on an unknown package",
			npmUser(),
			http.MethodPut,
			"/repos/myrepo/-/package/ghost/dist-tags/beta",
			`"2.0.0"`,
			http.StatusNotFound,
			nil,
			false,
		},
		{
			"put on an unknown repository",
			npmUser(),
			http.MethodPut,
			"/repos/nope/-/package/demo/dist-tags/beta",
			`"2.0.0"`,
			http.StatusNotFound,
			nil,
			false,
		},
		{
			"put cannot reach another repository's package",
			npmUser(),
			http.MethodPut,
			"/repos/myrepo/-/package/onlyother/dist-tags/beta",
			`"2.0.0"`,
			http.StatusNotFound,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"delete without a principal",
			nil,
			http.MethodDelete,
			tagURL + "stable",
			"",
			http.StatusUnauthorized,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"delete removes a tag",
			npmUser(),
			http.MethodDelete,
			tagURL + "stable",
			"",
			http.StatusOK,
			map[string]string{"latest": "1.0.0"},
			true,
		},
		{
			"delete protects latest",
			npmUser(),
			http.MethodDelete,
			tagURL + "latest",
			"",
			http.StatusBadRequest,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
		{
			"delete on an unknown package",
			npmUser(),
			http.MethodDelete,
			"/repos/myrepo/-/package/ghost/dist-tags/stable",
			"",
			http.StatusNotFound,
			nil,
			false,
		},
		{
			"delete on an unknown repository",
			npmUser(),
			http.MethodDelete,
			"/repos/nope/-/package/demo/dist-tags/stable",
			"",
			http.StatusNotFound,
			nil,
			false,
		},
		{
			"delete cannot reach another repository's package",
			npmUser(),
			http.MethodDelete,
			"/repos/myrepo/-/package/onlyother/dist-tags/stable",
			"",
			http.StatusNotFound,
			map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			demo := e.db.addPackage(
				npmTestRepoID,
				"demo",
				time.Hour,
				map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
				"1.0.0",
				"2.0.0",
			)
			// A package of the other repository: no request to myrepo may touch it.
			other := e.db.addPackage(
				npmOtherRepoID,
				"onlyother",
				time.Hour,
				map[string]string{"latest": "1.0.0", "stable": "1.0.0"},
				"1.0.0",
				"2.0.0",
			)

			rw := e.do(tt.principal, tt.method, tt.target, tt.body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if tt.wantTags != nil {
				if got := e.db.tagsOf(demo.ID); !mapsEqual(got, tt.wantTags) {
					t.Errorf("tags = %v, want %v", got, tt.wantTags)
				}
			}

			if got, want := e.db.tagsOf(other.ID), (map[string]string{"latest": "1.0.0", "stable": "1.0.0"}); !mapsEqual(got, want) {
				t.Errorf("another repository's tags = %v, want them untouched", got)
			}

			if got := e.db.staleCount() > 0; got != tt.wantStale {
				t.Errorf("packument marked stale = %v, want %v", got, tt.wantStale)
			}

			if tt.want == http.StatusOK && !strings.Contains(rw.Body.String(), `"latest"`) {
				t.Errorf("response lacks the tag map: %s", rw.Body.String())
			}
		})
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func TestNPMDistTagScopedPackage(t *testing.T) {
	e := newNPMEnv(t)
	pkg := e.db.addPackage(npmTestRepoID, "@acme/demo", time.Hour, map[string]string{"latest": "1.0.0"}, "1.0.0", "2.0.0")

	if rw := e.do(npmUser(), http.MethodPut, "/repos/myrepo/-/package/@acme%2Fdemo/dist-tags/next", `"2.0.0"`); rw.Code != http.StatusOK {
		t.Fatalf("put status = %d: %s", rw.Code, rw.Body.String())
	}

	if got := e.db.tagsOf(pkg.ID)["next"]; got != "2.0.0" {
		t.Fatalf("next = %q, want 2.0.0", got)
	}

	if rw := e.do(npmUser(), http.MethodDelete, "/repos/myrepo/-/package/@acme%2Fdemo/dist-tags/next", ""); rw.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", rw.Code, rw.Body.String())
	}

	if _, ok := e.db.tagsOf(pkg.ID)["next"]; ok {
		t.Fatal("next survived its delete")
	}
}

func TestNPMUnpublish(t *testing.T) {
	const old = 100 * time.Hour // beyond the 72h unpublish window

	tests := []struct {
		name         string
		principal    *auth.Principal
		target       string
		repo         func(*models.Repository)
		age          time.Duration
		want         int
		wantLive     []string // demo's versions afterwards
		wantReleased []int64
		wantPkgGone  bool
	}{
		{"anonymous", nil, "/repos/myrepo/demo/-rev/1-x", nil, time.Hour, http.StatusUnauthorized, []string{"1.0.0", "2.0.0"}, nil, false},
		{
			"anonymous flag",
			npmAnonymous(),
			"/repos/myrepo/demo/-rev/1-x",
			nil,
			time.Hour,
			http.StatusUnauthorized,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"whole package within the window",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			nil,
			time.Hour,
			http.StatusOK,
			nil,
			[]int64{100, 100},
			true,
		},
		{
			"whole package after 72h is refused",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			nil,
			old,
			http.StatusForbidden,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"whole package after 72h when full unpublish is allowed",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			func(r *models.Repository) { r.AllowFullUnpublish = true },
			old,
			http.StatusOK,
			nil,
			[]int64{100, 100},
			true,
		},
		{
			"immutable release",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			func(r *models.Repository) { r.ImmutableReleases = true },
			time.Hour,
			http.StatusConflict,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"one version by tarball",
			npmUser(),
			"/repos/myrepo/demo/-/demo-1.0.0.tgz/-rev/1-x",
			nil,
			time.Hour,
			http.StatusOK,
			[]string{"2.0.0"},
			[]int64{100},
			false,
		},
		{
			"one version by tarball after 72h is refused",
			npmUser(),
			"/repos/myrepo/demo/-/demo-1.0.0.tgz/-rev/1-x",
			nil,
			old,
			http.StatusForbidden,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"one immutable release by tarball",
			npmUser(),
			"/repos/myrepo/demo/-/demo-1.0.0.tgz/-rev/1-x",
			func(r *models.Repository) { r.ImmutableReleases = true },
			time.Hour,
			http.StatusConflict,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"tarball of a version that is gone",
			npmUser(),
			"/repos/myrepo/demo/-/demo-9.9.9.tgz/-rev/1-x",
			nil,
			time.Hour,
			http.StatusOK,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"unknown package",
			npmUser(),
			"/repos/myrepo/ghost/-rev/1-x",
			nil,
			time.Hour,
			http.StatusNotFound,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"unknown repository",
			npmUser(),
			"/repos/nope/demo/-rev/1-x",
			nil,
			time.Hour,
			http.StatusNotFound,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
		{
			"package of another repository",
			npmUser(),
			"/repos/myrepo/onlyother/-rev/1-x",
			nil,
			time.Hour,
			http.StatusNotFound,
			[]string{"1.0.0", "2.0.0"},
			nil,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			if tt.repo != nil {
				tt.repo(e.db.Repo)
			}

			demo := e.db.addPackage(npmTestRepoID, "demo", tt.age, map[string]string{"latest": "2.0.0", "old": "1.0.0"}, "1.0.0", "2.0.0")
			otherPkg := e.db.addPackage(npmOtherRepoID, "onlyother", time.Hour, map[string]string{"latest": "2.0.0"}, "1.0.0", "2.0.0")

			rw := e.do(tt.principal, http.MethodDelete, tt.target, "")
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if got := e.db.liveVersions(demo.ID); !slices.Equal(got, tt.wantLive) && (len(got) != 0 || len(tt.wantLive) != 0) {
				t.Errorf("versions = %v, want %v", got, tt.wantLive)
			}

			if got := e.db.liveVersions(otherPkg.ID); len(got) != 2 {
				t.Errorf("another repository's versions = %v, want both kept", got)
			}

			if !slices.Equal(e.db.released, tt.wantReleased) {
				t.Errorf("quota released = %v, want %v", e.db.released, tt.wantReleased)
			}

			if gone := slices.Contains(e.db.deletedPkgs, demo.ID); gone != tt.wantPkgGone {
				t.Errorf("package deleted = %v, want %v", gone, tt.wantPkgGone)
			}

			if got := len(e.events.Events()); got != len(tt.wantReleased) {
				t.Errorf("artifact.deleted events = %d, want %d", got, len(tt.wantReleased))
			}
		})
	}
}

// A tarball unpublish removes the dist-tags of the removed version only and
// frees the blob bytes the version held.
func TestNPMUnpublishVersionDropsItsTagsAndBlob(t *testing.T) {
	e := newNPMEnv(t)
	sha := strings.Repeat("a", 64)
	e.blobs.Blobs[sha] = []byte("tarball")

	demo := e.db.addPackage(
		npmTestRepoID,
		"demo",
		time.Hour,
		map[string]string{"latest": "2.0.0", "old": "1.0.0", "beta": "1.0.0"},
		"1.0.0",
		"2.0.0",
	)
	e.db.versions[demo.ID][0].BlobSHA256 = sha
	e.db.versions[demo.ID][0].BlobSizeBytes = 0 // size resolved from the blob store

	if rw := e.do(npmUser(), http.MethodDelete, "/repos/myrepo/demo/-/demo-1.0.0.tgz/-rev/1-x", ""); rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
	}

	if got, want := e.db.tagsOf(demo.ID), map[string]string{"latest": "2.0.0"}; !mapsEqual(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}

	if _, ok := e.blobs.Blobs[sha]; ok {
		t.Error("the unreferenced tarball blob was not deleted")
	}

	if want := []int64{int64(len("tarball"))}; !slices.Equal(e.db.released, want) {
		t.Errorf("quota released = %v, want the blob's size %v", e.db.released, want)
	}

	if e.db.staleCount() == 0 {
		t.Error("the packument was not marked stale")
	}
}

func npmRevBody(name string, tags map[string]string, versions ...string) string {
	vs := make([]string, 0, len(versions))
	for _, v := range versions {
		vs = append(vs, fmt.Sprintf(`"%s":{"name":"%s","version":"%s"}`, v, name, v))
	}

	ts := make([]string, 0, len(tags))
	for k, v := range tags {
		ts = append(ts, fmt.Sprintf(`"%s":"%s"`, k, v))
	}

	return fmt.Sprintf(`{"_id":"%[1]s","name":"%[1]s","versions":{%[2]s},"dist-tags":{%[3]s}}`,
		name, strings.Join(vs, ","), strings.Join(ts, ","))
}

func TestNPMRevPut(t *testing.T) {
	const old = 100 * time.Hour

	tests := []struct {
		name         string
		principal    *auth.Principal
		target       string
		body         string
		age          time.Duration
		immutable    bool
		want         int
		wantLive     []string
		wantTags     map[string]string
		wantPkgGone  bool
		wantReleased int
	}{
		{
			"anonymous",
			nil,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "1.0.0"}, "1.0.0"),
			time.Hour,
			false,
			http.StatusUnauthorized,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
		{
			"removes the missing version and retags",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "1.0.0", "beta": "1.0.0"}, "1.0.0"),
			time.Hour,
			false,
			http.StatusOK,
			[]string{"1.0.0"},
			map[string]string{"latest": "1.0.0", "beta": "1.0.0"},
			false,
			1,
		},
		{
			"no version left removes the package",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", nil),
			time.Hour,
			false,
			http.StatusOK,
			nil,
			nil,
			true,
			2,
		},
		{
			"after 72h",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "1.0.0"}, "1.0.0"),
			old,
			false,
			http.StatusForbidden,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
		{
			"immutable release",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "1.0.0"}, "1.0.0"),
			time.Hour,
			true,
			http.StatusConflict,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
		{
			"tag to an unknown version",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "7.0.0"}, "1.0.0"),
			time.Hour,
			false,
			http.StatusBadRequest,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
		{
			"version-shaped tag",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"3.0.0": "1.0.0"}, "1.0.0"),
			time.Hour,
			false,
			http.StatusBadRequest,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
		{
			"unknown package",
			npmUser(),
			"/repos/myrepo/ghost/-rev/1-x",
			npmRevBody("ghost", nil, "1.0.0"),
			time.Hour,
			false,
			http.StatusNotFound,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
		{
			"name mismatch",
			npmUser(),
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("other", nil, "1.0.0"),
			time.Hour,
			false,
			http.StatusBadRequest,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
		{
			"package of another repository",
			npmUser(),
			"/repos/myrepo/onlyother/-rev/1-x",
			npmRevBody("onlyother", nil, "1.0.0"),
			time.Hour,
			false,
			http.StatusNotFound,
			[]string{"1.0.0", "2.0.0"},
			map[string]string{"latest": "2.0.0"},
			false,
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			e.db.Repo.ImmutableReleases = tt.immutable

			demo := e.db.addPackage(npmTestRepoID, "demo", tt.age, map[string]string{"latest": "2.0.0"}, "1.0.0", "2.0.0")
			e.db.addPackage(npmOtherRepoID, "onlyother", time.Hour, nil, "1.0.0")

			rw := e.do(tt.principal, http.MethodPut, tt.target, tt.body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if got := e.db.liveVersions(demo.ID); !slices.Equal(got, tt.wantLive) && (len(got) != 0 || len(tt.wantLive) != 0) {
				t.Errorf("versions = %v, want %v", got, tt.wantLive)
			}

			if tt.wantTags != nil {
				if got := e.db.tagsOf(demo.ID); !mapsEqual(got, tt.wantTags) {
					t.Errorf("tags = %v, want %v", got, tt.wantTags)
				}
			}

			if gone := slices.Contains(e.db.deletedPkgs, demo.ID); gone != tt.wantPkgGone {
				t.Errorf("package deleted = %v, want %v", gone, tt.wantPkgGone)
			}

			if len(e.db.released) != tt.wantReleased {
				t.Errorf("quota releases = %d, want %d", len(e.db.released), tt.wantReleased)
			}
		})
	}
}

// A unique violation while storing a version (a concurrent publish of the same
// version) is a 409 that gives the reserved quota and the tarball back.
func TestNPMPublishUniqueViolation(t *testing.T) {
	payload := []byte("tarball-bytes")
	body := npmBody("demo", "1.0.0", base64.StdEncoding.EncodeToString(payload), len(payload), "data-middle")

	tests := []struct {
		name         string
		versionErr   error
		packageErr   error
		want         int
		wantReleased int
	}{
		{
			"version already exists",
			errors.New(`ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)`),
			nil,
			http.StatusConflict,
			1,
		},
		{"package already exists", nil, errors.New("package already exists"), http.StatusConflict, 0},
		{"other database failure", errors.New("connection reset"), nil, http.StatusInternalServerError, 1},
		{"other package failure", nil, errors.New("connection reset"), http.StatusInternalServerError, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			e.db.createVerErr = tt.versionErr
			e.db.createPkgErr = tt.packageErr

			rw := e.do(npmUser(), http.MethodPut, "/repos/myrepo/demo", body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), "connection reset") || strings.Contains(rw.Body.String(), "SQLSTATE") {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}

			if len(e.db.released) != tt.wantReleased {
				t.Errorf("quota releases = %d, want %d", len(e.db.released), tt.wantReleased)
			}

			if len(e.blobs.Blobs) != 0 {
				t.Errorf("a failed publish left %d blob(s) behind", len(e.blobs.Blobs))
			}

			if len(e.events.Events()) != 0 {
				t.Error("a failed publish emitted artifact.uploaded")
			}
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("connection reset"), false},
		{errors.New("version already exists"), true},
		{errors.New(`duplicate key value violates unique constraint "npm_versions_pkey"`), true},
		{errors.New("ERROR: duplicate (SQLSTATE 23505)"), true},
	}

	for _, tt := range tests {
		if got := isUniqueViolation(tt.err); got != tt.want {
			t.Errorf("isUniqueViolation(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

// npmTrashDB makes the npm store keep a trash: TrashRows stamps nothing here
// but answers like a repository with retention (soft), or reports the rows as
// no longer live.
type npmTrashDB struct {
	*npmMemDB
	database.TrashOps

	notLive bool
	trashed []string
}

func (d *npmTrashDB) TrashRows(_ context.Context, _ string, _ models.TrashKind, ids []string, _ string) (bool, error) {
	if d.notLive {
		return false, database.ErrTrashNotFound
	}

	d.trashed = append(d.trashed, ids...)

	return true, nil
}

// npmRBACDB adds access tokens, their users and per-user permission targets to
// the npm store so requests go through the real RBAC middleware.
type npmRBACDB struct {
	*npmMemDB

	tokens  map[string]*models.Token
	targets map[string][]*models.PermissionTarget // user ID -> targets
}

// ListProjectGrantsByUser is not exercised by the tests.
func (*npmRBACDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

// UpdateTokenLastUsed is not exercised by the tests.
func (*npmRBACDB) UpdateTokenLastUsed(context.Context, string) error { return errNotImplemented }

// ListGroupsByUser is not exercised by the tests.
func (*npmRBACDB) ListGroupsByUser(context.Context, string) ([]*models.Group, error) {
	return nil, errNotImplemented
}

func (d *npmRBACDB) GetTokenByHash(_ context.Context, hash string) (*models.Token, error) {
	return d.tokens[hash], nil
}

func (d *npmRBACDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return &models.User{ID: id, Username: id}, nil
}

func (d *npmRBACDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	return d.targets[userID], nil
}

// TestNPMWriteRoutesThroughRBAC sends the npm write routes through the real
// RBAC middleware: dist-tag writes need write, tag and version removal need
// delete (the -rev PUT included), and a grant on one repository gives nothing
// on another.
func TestNPMWriteRoutesThroughRBAC(t *testing.T) {
	grant := func(repo string, actions ...string) []*models.PermissionTarget {
		return []*models.PermissionTarget{{ID: "t-" + repo, RepositoryFilter: "^" + repo + "$", PathPattern: ".*", Actions: actions}}
	}

	users := map[string][]*models.PermissionTarget{
		"reader":    grant("myrepo", "read"),
		"writer":    grant("myrepo", "read", "write"),
		"deleter":   grant("myrepo", "read", "write", "delete"),
		"elsewhere": grant("otherrepo", "read", "write", "delete"),
		"no-grant":  nil,
	}

	tests := []struct {
		name   string
		user   string // "" sends no credentials
		method string
		target string
		body   string
		want   int
	}{
		{"anonymous tag put", "", http.MethodPut, "/repos/myrepo/-/package/demo/dist-tags/beta", `"2.0.0"`, http.StatusUnauthorized},
		{"anonymous unpublish", "", http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", "", http.StatusUnauthorized},
		{"read-only tag put", "reader", http.MethodPut, "/repos/myrepo/-/package/demo/dist-tags/beta", `"2.0.0"`, http.StatusForbidden},
		{"read-only tag delete", "reader", http.MethodDelete, "/repos/myrepo/-/package/demo/dist-tags/stable", "", http.StatusForbidden},
		{"read-only unpublish", "reader", http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", "", http.StatusForbidden},
		{"writer tag put", "writer", http.MethodPut, "/repos/myrepo/-/package/demo/dist-tags/beta", `"2.0.0"`, http.StatusOK},
		{
			"writer tag delete needs delete",
			"writer",
			http.MethodDelete,
			"/repos/myrepo/-/package/demo/dist-tags/stable",
			"",
			http.StatusForbidden,
		},
		{"writer unpublish needs delete", "writer", http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", "", http.StatusForbidden},
		{
			"writer rev put needs delete",
			"writer",
			http.MethodPut,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "1.0.0"}, "1.0.0"),
			http.StatusForbidden,
		},
		{"deleter tag delete", "deleter", http.MethodDelete, "/repos/myrepo/-/package/demo/dist-tags/stable", "", http.StatusOK},
		{
			"deleter rev put",
			"deleter",
			http.MethodPut,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "1.0.0"}, "1.0.0"),
			http.StatusOK,
		},
		{"deleter unpublish", "deleter", http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", "", http.StatusOK},
		{
			"grant on another repository: tag put",
			"elsewhere",
			http.MethodPut,
			"/repos/myrepo/-/package/demo/dist-tags/beta",
			`"2.0.0"`,
			http.StatusForbidden,
		},
		{"grant on another repository: unpublish", "elsewhere", http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", "", http.StatusForbidden},
		{"no grant", "no-grant", http.MethodPut, "/repos/myrepo/-/package/demo/dist-tags/beta", `"2.0.0"`, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			demo := e.db.addPackage(
				npmTestRepoID,
				"demo",
				time.Hour,
				map[string]string{"latest": "2.0.0", "stable": "1.0.0"},
				"1.0.0",
				"2.0.0",
			)

			db := &npmRBACDB{npmMemDB: e.db, tokens: map[string]*models.Token{}, targets: users}
			e.h.db = db

			secrets := map[string]string{}

			for user := range users {
				secret, hash, err := auth.NewTokenSecret(auth.TokenKindAccess)
				if err != nil {
					t.Fatal(err)
				}

				db.tokens[hash] = &models.Token{
					ID:        "tok-" + user,
					UserID:    user,
					Kind:      auth.TokenKindAccess,
					TokenHash: hash,
					Scopes:    []string{},
					CreatedAt: time.Now(),
				}
				secrets[user] = secret
			}

			log := formattest.NewLogger(t)
			router := chi.NewRouter()
			router.Use(middleware.RBACMiddleware(middleware.RBACConfig{}, db, auth.NewEvaluator(db, log, false), log))
			e.h.Register(router)

			req := httptest.NewRequestWithContext(context.Background(), tt.method, tt.target, strings.NewReader(tt.body))
			if tt.user != "" {
				req.Header.Set("Authorization", "Bearer "+secrets[tt.user])
			}

			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, req)

			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if tt.want >= 400 {
				if got := e.db.liveVersions(demo.ID); !slices.Equal(got, []string{"1.0.0", "2.0.0"}) {
					t.Errorf("a denied request changed the versions to %v", got)
				}

				if got, want := e.db.tagsOf(demo.ID), (map[string]string{"latest": "2.0.0", "stable": "1.0.0"}); !mapsEqual(got, want) {
					t.Errorf("a denied request changed the tags to %v", got)
				}
			}
		})
	}
}

// A database failure at any step of a tag write or a removal is a plain 500
// that leaks nothing and leaves the other state alone.
func TestNPMWritesDatabaseFailures(t *testing.T) {
	boom := errors.New("connection reset by peer")

	tests := []struct {
		name   string
		fail   string
		method string
		target string
		body   string
		want   int
	}{
		{
			"tag put: package lookup",
			"GetNPMPackageByName",
			http.MethodPut,
			"/repos/myrepo/-/package/demo/dist-tags/beta",
			`"2.0.0"`,
			http.StatusInternalServerError,
		},
		{
			"tag put: version lookup",
			"GetNPMVersion",
			http.MethodPut,
			"/repos/myrepo/-/package/demo/dist-tags/beta",
			`"2.0.0"`,
			http.StatusInternalServerError,
		},
		{
			"tag put: upsert",
			"UpsertNPMDistTag",
			http.MethodPut,
			"/repos/myrepo/-/package/demo/dist-tags/beta",
			`"2.0.0"`,
			http.StatusInternalServerError,
		},
		{
			"tag put: reading the tags back",
			"GetNPMDistTags",
			http.MethodPut,
			"/repos/myrepo/-/package/demo/dist-tags/beta",
			`"2.0.0"`,
			http.StatusInternalServerError,
		},
		{
			"tag put: stale marker failure is not fatal",
			"MarkPackumentStale",
			http.MethodPut,
			"/repos/myrepo/-/package/demo/dist-tags/beta",
			`"2.0.0"`,
			http.StatusOK,
		},
		{
			"tag delete: package lookup",
			"GetNPMPackageByName",
			http.MethodDelete,
			"/repos/myrepo/-/package/demo/dist-tags/stable",
			"",
			http.StatusInternalServerError,
		},
		{
			"tag delete: delete",
			"DeleteNPMDistTag",
			http.MethodDelete,
			"/repos/myrepo/-/package/demo/dist-tags/stable",
			"",
			http.StatusInternalServerError,
		},
		{
			"tag delete: reading the tags back",
			"GetNPMDistTags",
			http.MethodDelete,
			"/repos/myrepo/-/package/demo/dist-tags/stable",
			"",
			http.StatusInternalServerError,
		},
		{
			"unpublish: package lookup",
			"GetNPMPackageByName",
			http.MethodDelete,
			"/repos/myrepo/demo/-rev/1-x",
			"",
			http.StatusInternalServerError,
		},
		{
			"unpublish: version listing",
			"GetNPMVersionsByPackage",
			http.MethodDelete,
			"/repos/myrepo/demo/-rev/1-x",
			"",
			http.StatusInternalServerError,
		},
		{
			"unpublish: version delete",
			"DeleteNPMVersion",
			http.MethodDelete,
			"/repos/myrepo/demo/-rev/1-x",
			"",
			http.StatusInternalServerError,
		},
		{"unpublish: tag listing", "GetNPMDistTags", http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", "", http.StatusInternalServerError},
		{
			"unpublish: package delete",
			"DeleteNPMPackage",
			http.MethodDelete,
			"/repos/myrepo/demo/-rev/1-x",
			"",
			http.StatusInternalServerError,
		},
		{
			"unpublish: stale marker failure is not fatal",
			"MarkPackumentStale",
			http.MethodDelete,
			"/repos/myrepo/demo/-rev/1-x",
			"",
			http.StatusOK,
		},
		{
			"unpublish tarball: version delete",
			"DeleteNPMVersion",
			http.MethodDelete,
			"/repos/myrepo/demo/-/demo-1.0.0.tgz/-rev/1-x",
			"",
			http.StatusInternalServerError,
		},
		{
			"rev put: package lookup",
			"GetNPMPackageByName",
			http.MethodPut,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", nil, "1.0.0"),
			http.StatusInternalServerError,
		},
		{
			"rev put: version listing",
			"GetNPMVersionsByPackage",
			http.MethodPut,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", nil, "1.0.0"),
			http.StatusInternalServerError,
		},
		{
			"rev put: version delete",
			"DeleteNPMVersion",
			http.MethodPut,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", nil, "1.0.0"),
			http.StatusInternalServerError,
		},
		{
			"rev put: tag upsert",
			"UpsertNPMDistTag",
			http.MethodPut,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", map[string]string{"latest": "1.0.0"}, "1.0.0"),
			http.StatusInternalServerError,
		},
		{
			"rev put: package delete",
			"DeleteNPMPackage",
			http.MethodPut,
			"/repos/myrepo/demo/-rev/1-x",
			npmRevBody("demo", nil),
			http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			e.db.addPackage(npmTestRepoID, "demo", time.Hour, map[string]string{"latest": "2.0.0", "stable": "1.0.0"}, "1.0.0", "2.0.0")
			e.db.fail[tt.fail] = boom

			rw := e.do(npmUser(), tt.method, tt.target, tt.body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), "connection reset") {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}
		})
	}
}

// With a trash the unpublish only stamps the rows: nothing is released or
// deleted, the artifact.deleted events still go out and the packument is
// refreshed; rows that are no longer live answer 404.
func TestNPMUnpublishWithTrash(t *testing.T) {
	tests := []struct {
		name       string
		notLive    bool
		want       int
		wantTrash  int
		wantEvents int
	}{
		{"kept in the trash", false, http.StatusOK, 2, 2},
		{"rows no longer live", true, http.StatusNotFound, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNPMEnv(t)
			db := &npmTrashDB{npmMemDB: e.db, notLive: tt.notLive}
			e.h.db = db

			demo := e.db.addPackage(npmTestRepoID, "demo", time.Hour, map[string]string{"latest": "2.0.0"}, "1.0.0", "2.0.0")

			rw := e.do(npmUser(), http.MethodDelete, "/repos/myrepo/demo/-rev/1-x", "")
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tt.want, rw.Body.String())
			}

			if len(db.trashed) != tt.wantTrash {
				t.Errorf("trashed = %v, want %d rows", db.trashed, tt.wantTrash)
			}

			if len(e.db.released) != 0 {
				t.Errorf("quota released for trashed versions: %v", e.db.released)
			}

			if got := e.db.liveVersions(demo.ID); len(got) != 2 {
				t.Errorf("version rows were deleted instead of trashed: %v", got)
			}

			if got := len(e.events.Events()); got != tt.wantEvents {
				t.Errorf("artifact.deleted events = %d, want %d", got, tt.wantEvents)
			}

			if tt.want == http.StatusOK && e.db.staleCount() == 0 {
				t.Error("the packument was not marked stale")
			}
		})
	}
}
