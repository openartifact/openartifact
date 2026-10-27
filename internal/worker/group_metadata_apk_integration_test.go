// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	"github.com/openartifact/openartifact/internal/group"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/apk/apktest"
)

const (
	apkMainDir      = "v3.20/main/x86_64"
	apkCommunityDir = "v3.20/community/x86_64"
)

// apkGroupStack is an apk group on a real database: hosted members behind the
// hosted handler, proxy members read from a fake upstream, the group worker and
// the group handler wired as in cmd/server (generation is driven by the test).
type apkGroupStack struct {
	*groupFixture

	keys   *apkkeys.Service
	hosted *ApkMetadataWorker
	router chi.Router
	up     fakeUpstream
	gw     *GroupMetadataWorker
	gh     *group.Handler

	grp    *models.Repository
	denied map[string]bool // member name -> the caller may not read it
	asked  []string        // members a package file was fetched from
}

// newApkGroupStack creates the group over the members: hosted ones are named
// hosted-<n>, proxy ones proxy-<n>.
func newApkGroupStack(t *testing.T, hosted, proxies int) (*apkGroupStack, []*models.Repository) {
	t.Helper()

	if err := crypto.Init(strings.Repeat("s", 32)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	f := newGroupFixture(t)
	log := testLogger(t)
	keys := apkkeys.New(f.pg, log)
	hw := NewApkMetadataWorker(f.pg, f.blobs, keys, log)
	t.Cleanup(hw.Stop)

	h := formatapk.NewHTTPHandler(log, f.pg, f.blobs, "http://oa.test").WithKeys(keys).WithRegenerator(hw)
	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	var members []*models.Repository

	for i := range hosted {
		members = append(members, f.repo("hosted-"+string(rune('a'+i)), "apk", "hosted"))
	}

	for i := range proxies {
		members = append(members, f.repo("proxy-"+string(rune('a'+i)), "apk", "proxy"))
	}

	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.ID
	}

	grp := f.repo("apk-group", "apk", "group", ids...)
	up := fakeUpstream{}
	gw := NewGroupMetadataWorker(f.pg, f.blobs, up, nil, fixedResolver{}, log).WithApkSigners(keys)
	t.Cleanup(gw.Stop)

	s := &apkGroupStack{
		groupFixture: f, keys: keys, hosted: hw, router: router, up: up, gw: gw,
		gh:  group.NewHandler(log, f.pg).WithMetadata(f.blobs, gw).WithApkKeys(keys),
		grp: grp, denied: map[string]bool{},
	}

	return s, members
}

// upload PUTs a package to a hosted member and regenerates its indexes.
func (s *apkGroupStack) upload(member *models.Repository, dir string, o apktest.Options) []byte {
	s.t.Helper()

	data := apktest.Build(s.t, o)

	if o.Version == "" {
		o.Version = "1.0-r0"
	}

	path := "/repos/" + member.Name + "/" + dir + "/" + o.Name + "-" + o.Version + ".apk"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, path, bytes.NewReader(data))
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		s.t.Fatalf("PUT %s: status %d: %s", path, rec.Code, rec.Body.String())
	}

	if err := regenerateApkWhenFree(s.hosted, member.ID); err != nil {
		s.t.Fatalf("regenerate %s: %v", member.Name, err)
	}

	return data
}

// proxyIndex makes the fake upstream of a proxy member list pkgs in dir.
func (s *apkGroupStack) proxyIndex(member *models.Repository, dir string, pkgs ...pkgapk.Package) {
	s.t.Helper()

	data, err := pkgapk.BuildIndex(pkgs, "upstream", nil)
	if err != nil {
		s.t.Fatal(err)
	}

	s.up[member.ID+"|"+dir+"/APKINDEX.tar.gz"] = data
}

func (s *apkGroupStack) backend() group.Backend {
	return group.Backend{
		Authorize: func(_ *http.Request, m *models.Repository, _ string) bool { return !s.denied[m.Name] },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, relPath string) {
			s.asked = append(s.asked, m.Name)

			if m.RepoType == "hosted" {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/repos/" + m.Name + "/" + relPath
				s.router.ServeHTTP(w, r2)

				return
			}

			// A proxy serves the file it has fetched from its upstream.
			if data, ok := s.up[m.ID+"|"+relPath]; ok {
				_, _ = w.Write(data)
				return
			}

			http.NotFound(w, r)
		},
	}
}

func (s *apkGroupStack) get(relPath string) *httptest.ResponseRecorder {
	s.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/apk-group/"+relPath, nil)
	rec := httptest.NewRecorder()
	s.gh.Serve(rec, req, s.grp, relPath, s.backend())

	return rec
}

// publicKey fetches the group's key the way a client installs it.
func (s *apkGroupStack) publicKey() (string, *rsa.PublicKey) {
	s.t.Helper()

	rec := s.get("public.key")
	if rec.Code != http.StatusOK {
		s.t.Fatalf("public.key: status %d: %s", rec.Code, rec.Body.String())
	}

	block, _ := pem.Decode(rec.Body.Bytes())
	if block == nil {
		s.t.Fatalf("public.key is not PEM: %q", rec.Body.String())
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		s.t.Fatal(err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		s.t.Fatalf("public key is %T", pub)
	}

	return "openartifact-apk-group.rsa.pub", rsaPub
}

// index returns the entries of the group's index of dir, which must verify
// against the key the group serves.
func (s *apkGroupStack) index(dir string) []pkgapk.Package {
	s.t.Helper()

	name, pub := s.publicKey()

	rec := s.get(dir + "/APKINDEX.tar.gz")
	if rec.Code != http.StatusOK {
		s.t.Fatalf("GET %s index: status %d: %s", dir, rec.Code, rec.Body.String())
	}

	if signedBy, err := apktest.VerifyIndex(rec.Body.Bytes(), map[string]*rsa.PublicKey{name: pub}); err != nil || signedBy != name {
		s.t.Fatalf("index of %s does not verify against the group's key: %q, %v", dir, signedBy, err)
	}

	pkgs, err := pkgapk.ParseIndex(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		s.t.Fatalf("ParseIndex(%s): %v", dir, err)
	}

	return pkgs
}

func (s *apkGroupStack) generate(dist string, readable []string) {
	s.t.Helper()

	if err := s.gw.Generate(context.Background(), s.grp.ID, dist, readable); err != nil {
		s.t.Fatalf("Generate(%q): %v", dist, err)
	}
}

func TestApkGroupMergesHostedAndProxyMembers(t *testing.T) {
	s, m := newApkGroupStack(t, 2, 1)
	a, b, remote := m[0], m[1], m[2]

	foo := s.upload(a, apkMainDir, apktest.Options{Name: "foo"})
	s.upload(a, apkMainDir, apktest.Options{Name: "shared"})
	s.upload(b, apkMainDir, apktest.Options{Name: "shared"})
	s.upload(b, apkMainDir, apktest.Options{Name: "bar", Version: "2.0-r0"})

	s.proxyIndex(remote, apkMainDir,
		apkEntry("foo", "9.0-r0", "x86_64", apkSum("foo9")), // hosted names win: hidden
		apkEntry("baz", "3.0-r0", "x86_64", apkSum("baz3")),
	)
	s.proxyIndex(remote, apkCommunityDir, apkEntry("gamma", "1.0-r0", "x86_64", apkSum("gamma")))
	s.up[remote.ID+"|"+apkMainDir+"/baz-3.0-r0.apk"] = []byte("BAZ")

	// The first request of a directory only a proxy member has generates it.
	s.generate(apkCommunityDir, nil)

	if got := strings.Join(names(s.index(apkMainDir)), ","); got != "bar-2.0-r0,baz-3.0-r0,foo-1.0-r0,shared-1.0-r0" {
		t.Errorf("main index = %s, want the hosted packages and the proxy's baz, not its foo", got)
	}

	if got := strings.Join(names(s.index(apkCommunityDir)), ","); got != "gamma-1.0-r0" {
		t.Errorf("community index = %s", got)
	}

	owners := map[string]string{
		"foo-1.0-r0.apk": a.ID, "shared-1.0-r0.apk": a.ID, "bar-2.0-r0.apk": b.ID, "baz-3.0-r0.apk": remote.ID,
	}
	for file, want := range owners {
		if got := s.owner(s.grp.ID, apkMainDir+"/"+file); got != want {
			t.Errorf("owner of %s = %s, want %s", file, got, want)
		}
	}

	// Each file comes from the member the index lists it for, and only from it.
	rec := s.get(apkMainDir + "/foo-1.0-r0.apk")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), foo) {
		t.Errorf("foo = %d (%d bytes)", rec.Code, rec.Body.Len())
	}

	if rec := s.get(apkMainDir + "/baz-3.0-r0.apk"); rec.Code != http.StatusOK || rec.Body.String() != "BAZ" {
		t.Errorf("baz = %d %q", rec.Code, rec.Body.String())
	}

	if want := []string{a.Name, remote.Name}; !slices.Equal(s.asked, want) {
		t.Errorf("members asked = %v, want exactly the owners %v", s.asked, want)
	}

	// A release the index does not list is not served, whoever has the file.
	s.up[remote.ID+"|"+apkMainDir+"/foo-9.0-r0.apk"] = []byte("EVIL")

	if rec := s.get(apkMainDir + "/foo-9.0-r0.apk"); rec.Code != http.StatusNotFound {
		t.Errorf("hidden proxy release = %d, want 404", rec.Code)
	}

	// An unknown directory is a 404.
	if rec := s.get("v3.20/main/aarch64/APKINDEX.tar.gz"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown directory = %d, want 404", rec.Code)
	}

	// The group has a key of its own, not its members'.
	name, groupKey := s.publicKey()

	memberKey := httptest.NewRecorder()
	s.router.ServeHTTP(memberKey, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/hosted-a/public.key", nil))

	if memberKey.Code != http.StatusOK || bytes.Contains(memberKey.Body.Bytes(), []byte(name)) {
		t.Fatalf("member key: status %d", memberKey.Code)
	}

	block, _ := pem.Decode(memberKey.Body.Bytes())
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil || groupKey.Equal(parsed) {
		t.Errorf("the group is signed with a member's key (%v)", err)
	}

	groups, err := s.pg.ListMetadataGroupsOfMember(context.Background(), a.ID)
	if err != nil || !slices.Equal(groups, []string{s.grp.ID}) {
		t.Errorf("groups of a hosted member = %v, %v; want the apk group", groups, err)
	}
}

func TestApkGroupMergeProxyVersionsKeepsProxyReleasesOfHostedNames(t *testing.T) {
	s, m := newApkGroupStack(t, 1, 1)
	s.grp.MergeProxyVersions = true

	if err := s.pg.UpdateRepository(context.Background(), s.grp); err != nil {
		t.Fatal(err)
	}

	s.upload(m[0], apkMainDir, apktest.Options{Name: "foo"})
	s.proxyIndex(m[1], apkMainDir, apkEntry("foo", "9.0-r0", "x86_64", apkSum("foo9")))

	s.generate(apkMainDir, nil)

	if got := strings.Join(names(s.index(apkMainDir)), ","); got != "foo-1.0-r0,foo-9.0-r0" {
		t.Errorf("index = %s, want both releases", got)
	}
}

func TestApkGroupCallerWhoCannotReadAMemberGetsAVariantWithoutIt(t *testing.T) {
	s, m := newApkGroupStack(t, 2, 0)
	open, secret := m[0], m[1]

	s.upload(open, apkMainDir, apktest.Options{Name: "public-tool"})
	secretPkg := s.upload(secret, apkMainDir, apktest.Options{Name: "classified"})
	s.generate("", nil)

	if got := strings.Join(names(s.index(apkMainDir)), ","); got != "classified-1.0-r0,public-tool-1.0-r0" {
		t.Fatalf("shared index = %s", got)
	}

	s.denied[secret.Name] = true

	// The variant is generated on the first request, signed with the group's key.
	if got := strings.Join(names(s.index(apkMainDir)), ","); got != "public-tool-1.0-r0" {
		t.Errorf("variant index = %s, want only what the caller may read", got)
	}

	if rec := s.get(
		apkMainDir + "/classified-1.0-r0.apk",
	); rec.Code != http.StatusNotFound ||
		bytes.Contains(rec.Body.Bytes(), secretPkg[:16]) {
		t.Errorf("file of an unreadable member = %d", rec.Code)
	}

	if rec := s.get(apkMainDir + "/public-tool-1.0-r0.apk"); rec.Code != http.StatusOK {
		t.Errorf("file of a readable member = %d", rec.Code)
	}

	s.denied[secret.Name] = false

	if got := len(s.index(apkMainDir)); got != 2 {
		t.Errorf("a caller who reads every member got %d entries, want the shared index", got)
	}

	s.denied[open.Name], s.denied[secret.Name] = true, true

	if rec := s.get(apkMainDir + "/APKINDEX.tar.gz"); rec.Code != http.StatusNotFound {
		t.Errorf("index for a caller who reads nothing = %d, want 404", rec.Code)
	}
}

func TestApkGroupIndexIsResignedWithTheRotatedGroupKey(t *testing.T) {
	s, m := newApkGroupStack(t, 1, 0)
	s.upload(m[0], apkMainDir, apktest.Options{Name: "tool"})
	s.generate("", nil)

	oldName, oldKey := s.publicKey()
	oldIndex := s.get(apkMainDir + "/APKINDEX.tar.gz").Body.Bytes()

	var refreshed []string

	s.keys.SetRefresher(refresherFunc(func(id string) { refreshed = append(refreshed, id) }))

	if _, err := s.keys.Generate(context.Background(), s.grp, "test"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(refreshed, []string{s.grp.ID}) {
		t.Fatalf("key change notified %v, want the group", refreshed)
	}

	s.generate("", nil)

	_, newKey := s.publicKey()
	if oldKey.Equal(newKey) {
		t.Fatal("the group key did not change")
	}

	rec := s.get(apkMainDir + "/APKINDEX.tar.gz")
	if _, err := apktest.VerifyIndex(rec.Body.Bytes(), map[string]*rsa.PublicKey{oldName: oldKey}); err == nil {
		t.Error("the regenerated index still verifies with the old key")
	}

	if got := names(s.index(apkMainDir)); len(got) != 1 {
		t.Errorf("entries after re-signing = %v", got)
	}

	// Only the signature changed: the entries are the same.
	before, _ := pkgapk.ParseIndex(bytes.NewReader(oldIndex))
	after, _ := pkgapk.ParseIndex(bytes.NewReader(rec.Body.Bytes()))

	if len(before) != 1 || len(after) != 1 || before[0].Checksum != after[0].Checksum {
		t.Errorf("entries changed with the key: %+v -> %+v", before, after)
	}
}

type refresherFunc func(string)

func (f refresherFunc) NotifyStale(id string) { f(id) }

func TestApkGroupKeepsServingAnEmptiedDirectoryAsEmpty(t *testing.T) {
	s, m := newApkGroupStack(t, 1, 0)
	s.upload(m[0], apkMainDir, apktest.Options{Name: "tool"})
	s.generate("", nil)

	if got := len(s.index(apkMainDir)); got != 1 {
		t.Fatalf("entries = %d", got)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/repos/hosted-a/"+apkMainDir+"/tool-1.0-r0.apk", nil)

	if rec := httptest.NewRecorder(); func() int { s.router.ServeHTTP(rec, req); return rec.Code }() != http.StatusNoContent {
		t.Fatal("DELETE failed")
	}

	if err := regenerateApkWhenFree(s.hosted, m[0].ID); err != nil {
		t.Fatal(err)
	}

	s.generate("", nil)

	if got := s.index(apkMainDir); len(got) != 0 {
		t.Errorf("index of the emptied directory = %v, want an empty signed index", names(got))
	}

	if rec := s.get(apkMainDir + "/tool-1.0-r0.apk"); rec.Code != http.StatusNotFound {
		t.Errorf("file of the deleted package = %d, want 404", rec.Code)
	}
}

func TestApkGroupWithAnUnreachableProxyServesTheHostedMembersAndRetries(t *testing.T) {
	s, m := newApkGroupStack(t, 1, 1)
	s.upload(m[0], apkMainDir, apktest.Options{Name: "tool"})

	down := NewGroupMetadataWorker(s.pg, s.blobs, downUpstream{}, nil, fixedResolver{}, testLogger(t)).WithApkSigners(s.keys)
	t.Cleanup(down.Stop)

	if err := down.Generate(context.Background(), s.grp.ID, apkCommunityDir, nil); err == nil {
		t.Fatal("a directory only an unreachable proxy might have must not be reported as existing")
	}

	if err := down.Generate(context.Background(), s.grp.ID, "", nil); err != nil {
		t.Fatalf("Generate with an unreachable proxy: %v", err)
	}

	if got := strings.Join(names(s.index(apkMainDir)), ","); got != "tool-1.0-r0" {
		t.Errorf("index = %s, want the hosted member's packages", got)
	}

	state, err := s.pg.GetGroupMetadataState(context.Background(), s.grp.ID)
	if err != nil || state == nil || state.RefreshAfter == nil || state.GeneratedAt == nil ||
		state.RefreshAfter.Sub(*state.GeneratedAt) > 2*time.Minute {
		t.Errorf("state after a partial generation = %+v, %v; want a retry within minutes", state, err)
	}
}

func TestApkGroupsAreListedForRegeneration(t *testing.T) {
	s, m := newApkGroupStack(t, 1, 0)

	stale, err := s.pg.ListStaleMetadataGroups(context.Background())
	if err != nil || !slices.Contains(stale, s.grp.ID) {
		t.Errorf("a never generated apk group is not listed stale: %v, %v", stale, err)
	}

	s.upload(m[0], apkMainDir, apktest.Options{Name: "tool"})
	s.generate("", nil)

	stale, err = s.pg.ListStaleMetadataGroups(context.Background())
	if err != nil || slices.Contains(stale, s.grp.ID) {
		t.Errorf("a generated apk group is listed stale: %v, %v", stale, err)
	}
}
