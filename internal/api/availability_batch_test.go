// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
)

// batchStore is a BatchAvailabilityStore over maps that counts its calls.
type batchStore struct {
	members map[string][]*models.Repository                       // by group id
	rules   map[string][]curation.Rule                            // by proxy id
	stored  map[string]map[models.NameVersion]models.VersionHolds // by hosted id

	memberCalls, ruleCalls, heldCalls, storedCalls atomic.Int32
}

func (s *batchStore) ListCurationRulesUncached(_ context.Context, repoID string) ([]curation.Rule, error) {
	s.ruleCalls.Add(1)

	return s.rules[repoID], nil
}

func (s *batchStore) PackageVersionHolds(context.Context, string, string, string) (models.VersionHolds, error) {
	return models.VersionHolds{}, errors.New("the batch must not ask per version")
}

func (s *batchStore) ListGroupMemberRepositories(_ context.Context, groupID string) ([]*models.Repository, error) {
	s.memberCalls.Add(1)

	return s.members[groupID], nil
}

func (s *batchStore) HostedNamesHeld(_ context.Context, repoIDs, names []string) ([]models.HeldName, error) {
	s.heldCalls.Add(1)

	var out []models.HeldName

	for _, id := range repoIDs {
		for i, n := range names {
			for nv := range s.stored[id] {
				if nv.Name == n {
					out = append(out, models.HeldName{Index: i, RepoID: id})
					break
				}
			}
		}
	}

	return out, nil
}

func (s *batchStore) HostedVersionsStored(
	_ context.Context,
	repoIDs []string,
	versions []models.NameVersion,
) ([]models.StoredVersion, error) {
	s.storedCalls.Add(1)

	var out []models.StoredVersion

	for _, id := range repoIDs {
		for i, v := range versions {
			if holds, ok := s.stored[id][v]; ok {
				out = append(out, models.StoredVersion{Index: i, RepoID: id, VersionHolds: holds})
			}
		}
	}

	return out, nil
}

// batchCache is the cached proxy metadata: versions by proxy id and package (a
// package absent from a proxy's map is not cached). It counts reads.
type batchCache struct {
	fakePublished

	versions map[string]map[string][]proxy.DatedVersion

	mu    sync.Mutex
	reads map[cacheKey]int
}

func (c *batchCache) CachedVersions(_ context.Context, repo *models.Repository, pkg string) ([]proxy.DatedVersion, bool) {
	c.mu.Lock()
	if c.reads == nil {
		c.reads = map[cacheKey]int{}
	}
	c.reads[cacheKey{repo.ID, pkg}]++
	c.mu.Unlock()

	list, ok := c.versions[repo.ID][pkg]

	return list, ok
}

func (c *batchCache) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0
	for _, v := range c.reads {
		n += v
	}

	return n
}

// The world of the batch tests: composer repositories, a group over them and
// repositories serving skips. No test changes them.
//
//nolint:gochecknoglobals // shared read-only fixtures of the table tests
var (
	hostedA = &models.Repository{ID: "h-a", Name: "php-hosted", Format: "composer", RepoType: "hosted"}
	hostedB = &models.Repository{ID: "h-b", Name: "php-hosted-2", Format: "composer", RepoType: "hosted"}
	proxyP  = &models.Repository{ID: "p-p", Name: "php-proxy", Format: "composer", RepoType: "proxy", CurationMode: "denylist"}
	proxyQ  = &models.Repository{ID: "p-q", Name: "php-proxy-2", Format: "composer", RepoType: "proxy"}
	blocked = &models.Repository{ID: "p-b", Name: "php-blocked", Format: "composer", RepoType: "proxy", Blocked: true}
	nested  = &models.Repository{ID: "g-n", Name: "php-nested", Format: "composer", RepoType: "group"}
	npmRepo = &models.Repository{ID: "h-n", Name: "npm-hosted", Format: "npm", RepoType: "hosted"}
	secret  = &models.Repository{ID: "h-s", Name: "php-secret", Format: "composer", RepoType: "hosted"}
)

func readable(m *models.Repository) bool { return m.ID != secret.ID }

func group(merge bool, members ...*models.Repository) (*models.Repository, *batchStore) {
	g := &models.Repository{ID: "g", Name: "php", Format: "composer", RepoType: "group", MergeProxyVersions: merge}

	return g, &batchStore{members: map[string][]*models.Repository{"g": members}}
}

func newBatchWorld(st *batchStore) (*batchStore, *batchCache) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	st.rules = map[string][]curation.Rule{
		proxyP.ID: {{ID: "r-evil", Action: curation.ActionBlock, Package: "evil/*", Reason: "malicious"}},
	}
	st.stored = map[string]map[models.NameVersion]models.VersionHolds{
		hostedA.ID: {
			{Name: "acme/lib", Version: "1.0.0"}: {},
			{Name: "acme/lib", Version: "0.9.0"}: {Quarantined: true},
		},
		hostedB.ID: {{Name: "acme/other", Version: "2.0.0"}: {LicenseBlocked: true}},
		secret.ID:  {{Name: "acme/hidden", Version: "1.0.0"}: {}},
		npmRepo.ID: {{Name: "monolog/monolog", Version: "3.0.0"}: {}},
	}

	return st, &batchCache{versions: map[string]map[string][]proxy.DatedVersion{
		proxyP.ID: {
			"monolog/monolog": {{Version: "3.0.0", Published: day}},
			"evil/x":          {{Version: "1.0.0", Published: day}},
			"acme/lib":        {{Version: "2.0.0", Published: day}},
		},
		proxyQ.ID: {
			"monolog/monolog": {{Version: "3.0.0", Published: day}, {Version: "3.1.0", Published: day}},
			"evil/x":          {{Version: "1.0.0", Published: day}},
		},
		blocked.ID: {"monolog/monolog": {{Version: "3.1.0", Published: day}}},
	}}
}

type wantResult struct {
	status, repository, source string
}

func check(t *testing.T, got responses.AvailabilityBatchResponse, want []wantResult) {
	t.Helper()

	if len(got.Results) != len(want) {
		t.Fatalf("got %d results, want %d: %+v", len(got.Results), len(want), got.Results)
	}

	for i, w := range want {
		r := got.Results[i]

		source := ""
		if len(r.Reasons) > 0 {
			source = r.Reasons[0].Source
		}

		if r.Status != w.status || r.Repository != w.repository || source != w.source || r.Available != (w.status == StatusAvailable) {
			t.Errorf("result %d (%s %s) = %s from %s (%s, available %v), want %s from %s (%s)",
				i, r.Package, r.Version, r.Status, r.Repository, source, r.Available, w.status, w.repository, w.source)
		}
	}
}

func q(pkgVersions ...string) []responses.AvailabilityQuery {
	out := make([]responses.AvailabilityQuery, 0, len(pkgVersions)/2)
	for i := 0; i+1 < len(pkgVersions); i += 2 {
		out = append(out, responses.AvailabilityQuery{Package: pkgVersions[i], Version: pkgVersions[i+1]})
	}

	return out
}

// TestAvailabilityBatchOneRepository: a single repository is judged like the GET
// and also says whether the version exists.
func TestAvailabilityBatchOneRepository(t *testing.T) {
	for _, tc := range []struct {
		name  string
		repo  *models.Repository
		query []responses.AvailabilityQuery
		want  []wantResult
	}{
		{"hosted", hostedA, q("acme/lib", "1.0.0", "acme/lib", "0.9.0", "acme/lib", "5.0.0"), []wantResult{
			{StatusAvailable, "php-hosted", ""}, {StatusHeld, "php-hosted", sourceQuarantine}, {StatusNotFound, "php-hosted", sourceNotFound},
		}},
		{"proxy", proxyP, q("monolog/monolog", "3.0.0", "evil/x", "1.0.0", "monolog/monolog", "9.9.9", "unseen/pkg", "1.0.0"), []wantResult{
			{StatusAvailable, "php-proxy", ""}, {StatusHeld, "php-proxy", curation.SourceCuration},
			{StatusNotFound, "php-proxy", sourceNotFound}, {StatusUnknown, "php-proxy", sourceUnknown},
		}},
		{"blocked repository", blocked, q("monolog/monolog", "3.1.0"), []wantResult{
			{StatusHeld, "php-blocked", sourceRepositoryBlocked},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, cache := newBatchWorld(&batchStore{})

			got, err := ExplainAvailabilityBatch(context.Background(), st, cache, tc.repo, tc.query, readable)
			if err != nil {
				t.Fatal(err)
			}

			check(t, got, tc.want)
		})
	}
}

// TestAvailabilityBatchGroup: a group follows the serving path of internal/group.
func TestAvailabilityBatchGroup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		merge   bool
		members []*models.Repository
		query   []responses.AvailabilityQuery
		want    []wantResult
	}{
		{
			"blocked, nested, other-format and unreadable members are skipped", false,
			[]*models.Repository{blocked, nested, npmRepo, secret, proxyQ},
			q("monolog/monolog", "3.1.0", "acme/hidden", "1.0.0"),
			[]wantResult{{StatusAvailable, "php-proxy-2", ""}, {StatusUnknown, "php-proxy-2", sourceUnknown}},
		},
		{
			"hosted wins: a name a hosted member holds is decided by the hosted members only", false,
			[]*models.Repository{proxyP, hostedA},
			q("acme/lib", "2.0.0", "acme/lib", "1.0.0"),
			[]wantResult{{StatusNotFound, "php", sourceNotFound}, {StatusAvailable, "php-hosted", ""}},
		},
		{
			"merge_proxy_versions: proxies serve a name a hosted member holds", true,
			[]*models.Repository{hostedA, proxyP},
			q("acme/lib", "2.0.0"),
			[]wantResult{{StatusAvailable, "php-proxy", ""}},
		},
		{
			"the first member that has the version decides, even when it holds it back", false,
			[]*models.Repository{proxyP, proxyQ},
			q("evil/x", "1.0.0"),
			[]wantResult{{StatusHeld, "php-proxy", curation.SourceCuration}},
		},
		{
			"a member whose cached metadata does not list the version is passed over", false,
			[]*models.Repository{proxyP, proxyQ},
			q("monolog/monolog", "3.1.0"),
			[]wantResult{{StatusAvailable, "php-proxy-2", ""}},
		},
		{
			"a proxy whose metadata is not cached makes the answer unknown", false,
			[]*models.Repository{proxyP, proxyQ},
			q("unseen/pkg", "1.0.0"),
			[]wantResult{{StatusUnknown, "php-proxy", sourceUnknown}},
		},
		{
			"a held hosted version decides; hosted holds come through", false,
			[]*models.Repository{hostedB, proxyQ},
			q("acme/other", "2.0.0"),
			[]wantResult{{StatusHeld, "php-hosted-2", sourceLicensePolicy}},
		},
		{
			"no member has the version", false,
			[]*models.Repository{hostedA, proxyQ},
			q("monolog/monolog", "0.0.1"),
			[]wantResult{{StatusNotFound, "php", sourceNotFound}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, st := group(tc.merge, tc.members...)
			st, cache := newBatchWorld(st)

			got, err := ExplainAvailabilityBatch(context.Background(), st, cache, g, tc.query, readable)
			if err != nil {
				t.Fatal(err)
			}

			check(t, got, tc.want)
		})
	}
}

// TestAvailabilityBatchBlockedGroup: a blocked group refuses everything without
// consulting a member.
func TestAvailabilityBatchBlockedGroup(t *testing.T) {
	g, st := group(false, hostedA, proxyP)
	g.Blocked = true
	st, cache := newBatchWorld(st)

	got, err := ExplainAvailabilityBatch(context.Background(), st, cache, g, q("acme/lib", "1.0.0", "monolog/monolog", "3.0.0"), readable)
	if err != nil {
		t.Fatal(err)
	}

	check(t, got, []wantResult{{StatusHeld, "php", sourceRepositoryBlocked}, {StatusHeld, "php", sourceRepositoryBlocked}})

	if st.memberCalls.Load() != 0 || cache.readCount() != 0 {
		t.Errorf("a blocked group consulted %d member lists and %d caches", st.memberCalls.Load(), cache.readCount())
	}
}

// TestAvailabilityBatchCost: the database is asked the same number of times for
// one entry as for many (members once, rules once per proxy, two hosted
// queries), duplicates are answered once in request order, and the cache is read
// once per proxy and distinct package, only for entries an earlier member has not
// decided.
func TestAvailabilityBatchCost(t *testing.T) {
	g, st := group(false, hostedA, proxyP, proxyQ)
	st, cache := newBatchWorld(st)

	var query []responses.AvailabilityQuery
	for range 300 {
		query = append(query, q("acme/lib", "1.0.0", "monolog/monolog", "3.1.0", "monolog/monolog", "3.0.0")...)
	}

	got, err := ExplainAvailabilityBatch(context.Background(), st, cache, g, query, readable)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Results) != len(query) || got.Results[899].Version != "3.0.0" || got.Results[899].Repository != "php-proxy" {
		t.Fatalf("results are not one per entry in request order: %d, last %+v", len(got.Results), got.Results[len(got.Results)-1])
	}

	calls := []int32{st.memberCalls.Load(), st.ruleCalls.Load(), st.heldCalls.Load(), st.storedCalls.Load()}
	if !slices.Equal(calls, []int32{1, 1, 1, 1}) {
		t.Errorf("members, rules, held, stored calls = %v, want one each (rules: one curated proxy)", calls)
	}

	// acme/lib is decided by the hosted member; monolog/monolog is read from
	// php-proxy (lists 3.0.0 only) and, for 3.1.0, from php-proxy-2.
	want := map[cacheKey]int{{proxyP.ID, "monolog/monolog"}: 1, {proxyQ.ID, "monolog/monolog"}: 1}
	if len(cache.reads) != len(want) || cache.reads[cacheKey{proxyP.ID, "monolog/monolog"}] != 1 ||
		cache.reads[cacheKey{proxyQ.ID, "monolog/monolog"}] != 1 {
		t.Errorf("cache reads = %v, want %v", cache.reads, want)
	}
}

// blockingCache holds every read until release is closed, recording how many
// run at once.
type blockingCache struct {
	fakePublished

	inflight, peak atomic.Int32
	release        chan struct{}
}

func (c *blockingCache) CachedVersions(context.Context, *models.Repository, string) ([]proxy.DatedVersion, bool) {
	n := c.inflight.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}

	<-c.release
	c.inflight.Add(-1)

	return nil, false
}

// TestAvailabilityBatchReadConcurrency: cached-metadata reads run
// proxyReadConcurrency at a time, never more.
func TestAvailabilityBatchReadConcurrency(t *testing.T) {
	cache := &blockingCache{release: make(chan struct{})}

	var query []responses.AvailabilityQuery
	for i := range 40 {
		query = append(query, responses.AvailabilityQuery{Package: "pkg/" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Version: "1"})
	}

	done := make(chan error, 1)

	go func() {
		_, err := ExplainAvailabilityBatch(context.Background(), &batchStore{}, cache, proxyQ, query, readable)
		done <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for cache.inflight.Load() < proxyReadConcurrency && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	close(cache.release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if peak := cache.peak.Load(); peak != proxyReadConcurrency {
		t.Errorf("peak concurrent reads = %d, want %d", peak, proxyReadConcurrency)
	}
}

func TestAvailabilityBatchRefusesDocker(t *testing.T) {
	docker := &models.Repository{ID: "d", Name: "images", Format: "docker", RepoType: "hosted"}

	_, err := ExplainAvailabilityBatch(context.Background(), &batchStore{}, &batchCache{}, docker, q("app", "1"), readable)
	if !errors.Is(err, ErrBatchFormat) {
		t.Errorf("err = %v, want ErrBatchFormat", err)
	}
}

// TestAvailabilityBatchCurationFromPath: a proxy whose format refuses a curated
// version from the request path (composer) answers held for a version its
// rules hide whatever its cache says, without reading it, and in a group that
// refusal decides even when a later member has the version. A format that
// first looks the version up (npm) cannot tell for an uncached package.
func TestAvailabilityBatchCurationFromPath(t *testing.T) {
	npmProxy := &models.Repository{ID: "p-n", Name: "npm-proxy", Format: "npm", RepoType: "proxy", CurationMode: "denylist"}
	npmGroup := &models.Repository{ID: "g-npm", Name: "npm-all", Format: "npm", RepoType: "group"}
	phpGroup, _ := group(false)

	for _, tc := range []struct {
		name    string
		repo    *models.Repository
		members []*models.Repository
		query   []responses.AvailabilityQuery
		want    []wantResult
	}{
		{
			"composer proxy, package never fetched", proxyP, nil, q("evil/never-seen", "1.0.0"),
			[]wantResult{{StatusHeld, "php-proxy", curation.SourceCuration}},
		},
		{
			"composer proxy, version its cached metadata does not list", proxyP, nil, q("evil/x", "9.9.9"),
			[]wantResult{{StatusHeld, "php-proxy", curation.SourceCuration}},
		},
		{
			"composer group: the earlier proxy's rule decides although only the later member lists the version",
			phpGroup, []*models.Repository{proxyP, proxyQ}, q("evil/x", "1.0.0", "evil/never-seen", "1.0.0"),
			[]wantResult{{StatusHeld, "php-proxy", curation.SourceCuration}, {StatusHeld, "php-proxy", curation.SourceCuration}},
		},
		{
			"composer group: a later proxy's rule does not reach a version an earlier member serves",
			phpGroup, []*models.Repository{proxyQ, proxyP}, q("evil/x", "1.0.0"),
			[]wantResult{{StatusAvailable, "php-proxy-2", ""}},
		},
		{
			"npm proxy, package never fetched: the rule only applies once the version is found", npmProxy, nil, q("evil-pkg", "1.0.0"),
			[]wantResult{{StatusUnknown, "npm-proxy", sourceUnknown}},
		},
		{
			"npm group: the same", npmGroup, []*models.Repository{npmProxy}, q("evil-pkg", "1.0.0"),
			[]wantResult{{StatusUnknown, "npm-proxy", sourceUnknown}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, cache := newBatchWorld(&batchStore{members: map[string][]*models.Repository{tc.repo.ID: tc.members}})
			st.rules[npmProxy.ID] = []curation.Rule{{ID: "r-npm", Action: curation.ActionBlock, Package: "evil-*", Reason: "malicious"}}

			got, err := ExplainAvailabilityBatch(context.Background(), st, cache, tc.repo, tc.query, readable)
			if err != nil {
				t.Fatal(err)
			}

			check(t, got, tc.want)

			if tc.repo.Format == "composer" && tc.want[0].status == StatusHeld && cache.readCount() != 0 {
				t.Errorf("a curation hold from the path read the cache %d times", cache.readCount())
			}
		})
	}
}

// TestCuratesFromPathFormats pins which proxy formats refuse a curated version
// from the request path, before they know the upstream has it: the batch
// answers held for those without cached metadata. A format that moves its
// curation check behind the metadata lookup (or the other way) must change this
// list, and with it what the batch may claim.
func TestCuratesFromPathFormats(t *testing.T) {
	var got []string

	for _, f := range repotype.Names() {
		if proxy.CuratesFromPath(f) {
			got = append(got, f)
		}
	}

	slices.Sort(got)

	want := []string{"apk", "composer", "conan", "deb", "ivy", "maven", "pacman", "pypi", "rpm", "sbt"}
	if !slices.Equal(got, want) {
		t.Errorf("formats that curate from the path = %v, want %v", got, want)
	}
}
