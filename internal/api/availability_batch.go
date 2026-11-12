// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
)

// MaxAvailabilityBatch bounds the package versions of one batch availability
// request.
const MaxAvailabilityBatch = 1000

// proxyReadConcurrency bounds the cached-metadata reads of a batch that run at
// once (one read per proxy member and distinct package).
const proxyReadConcurrency = 8

// Values of AvailabilityBatchResult.Status.
const (
	StatusAvailable = "available"
	StatusHeld      = "held"
	StatusNotFound  = "not_found"
	StatusUnknown   = "unknown"
)

// Sources of the reasons only a batch gives.
const (
	sourceNotFound = "not_found"
	sourceUnknown  = "unknown"
)

// ErrBatchFormat: the batch reads the generic package tables and the proxy's
// cached package metadata, which a registry (docker) repository has neither of.
var ErrBatchFormat = errors.New("batch availability is not supported for registry (docker) repositories")

// BatchAvailabilityStore is the part of the database ExplainAvailabilityBatch needs.
type BatchAvailabilityStore interface {
	AvailabilityStore
	ListGroupMemberRepositories(ctx context.Context, groupRepoID string) ([]*models.Repository, error)
	HostedNamesHeld(ctx context.Context, repoIDs, names []string) ([]models.HeldName, error)
	HostedVersionsStored(ctx context.Context, repoIDs []string, versions []models.NameVersion) ([]models.StoredVersion, error)
}

// ExplainAvailabilityBatch answers, for each package version, whether a client
// of repo can install it and, when it cannot, why: the answer of
// POST /api/v1/repositories/{name}/availability and of the MCP check_dependencies
// tool. The caller has already checked read access to repo and the bounds of
// queries; canRead says whether the caller may read a group member.
//
// One repository is judged like the GET judges it, and also says whether the
// version exists: a hosted repository that stores no such version answers
// not_found, a proxy whose cached metadata does not list it not_found, and a
// proxy whose metadata is not cached (or whose format cannot list versions)
// unknown.
//
// A group follows the serving path (internal/group): a blocked group refuses
// everything; nested groups, members of another format, blocked members and
// members the caller cannot read are skipped; unless merge_proxy_versions is
// set, a name a hosted member holds is decided by the hosted members only
// (hosted wins); otherwise the first member in order that has the version
// decides it, held or not, because the member's 403 goes to the client. A proxy
// member whose cache cannot say makes the answer unknown: it might have the
// version.
//
// The cost does not grow with the queries by more than one cached-metadata read
// per proxy member and distinct package, run at most proxyReadConcurrency at a
// time and only for queries no earlier member decided: the members, the curation
// rules of each proxy member and the hosted data (two queries) are loaded once.
func ExplainAvailabilityBatch(
	ctx context.Context, db BatchAvailabilityStore, published PublishTimeSource, repo *models.Repository,
	queries []responses.AvailabilityQuery, canRead func(*models.Repository) bool,
) (responses.AvailabilityBatchResponse, error) {
	if repotype.MountOf(repo.Format) == repotype.MountOCI {
		return responses.AvailabilityBatchResponse{}, ErrBatchFormat
	}

	unique, slot := dedupeQueries(queries)

	b := &availabilityBatch{
		db: db, published: published, repo: repo, queries: unique, now: time.Now(),
		results: make([]batchResult, len(unique)),
		cached:  map[cacheKey]cachedVersions{},
	}

	if err := b.run(ctx, canRead); err != nil {
		return responses.AvailabilityBatchResponse{}, err
	}

	out := responses.AvailabilityBatchResponse{Repository: repo.Name, Results: make([]responses.AvailabilityBatchResult, len(queries))}
	for i, q := range queries {
		res := b.results[slot[i]].out
		res.Package, res.Version = q.Package, q.Version
		out.Results[i] = res
	}

	return out, nil
}

// dedupeQueries returns the distinct package versions of queries and, for each
// query, the index of its distinct version.
func dedupeQueries(queries []responses.AvailabilityQuery) ([]models.NameVersion, []int) {
	index := make(map[models.NameVersion]int, len(queries))
	unique := make([]models.NameVersion, 0, len(queries))
	slot := make([]int, len(queries))

	for i, q := range queries {
		nv := models.NameVersion{Name: q.Package, Version: q.Version}

		j, seen := index[nv]
		if !seen {
			j = len(unique)
			index[nv] = j
			unique = append(unique, nv)
		}

		slot[i] = j
	}

	return unique, slot
}

// cacheKey names one cached-metadata read: a proxy member and a package.
type cacheKey struct {
	repoID, pkg string
}

// cachedVersions is what the cached metadata of a proxy says about a package:
// ok is false when it cannot say (not cached, or the format lists no versions).
type cachedVersions struct {
	versions map[string]time.Time
	ok       bool
}

// storedKey names a stored version of a hosted member.
type storedKey struct {
	index  int
	repoID string
}

// batchResult is the answer for one distinct package version while the batch
// runs: cursor is the next member to consult.
type batchResult struct {
	decided bool
	cursor  int
	out     responses.AvailabilityBatchResult
}

// availabilityBatch is one run of ExplainAvailabilityBatch.
type availabilityBatch struct {
	db        BatchAvailabilityStore
	published PublishTimeSource
	repo      *models.Repository
	queries   []models.NameVersion
	now       time.Time

	// members are the repositories consulted in order: repo itself, or the
	// group members serving consults. hosted are the hosted ones among them.
	members []*models.Repository
	hosted  []*models.Repository
	gates   map[string]curation.Gate // by proxy member id
	// owned marks the queries whose name a hosted member holds (hosted wins).
	owned   map[int]bool
	stored  map[storedKey]models.VersionHolds
	cached  map[cacheKey]cachedVersions
	results []batchResult
}

func (b *availabilityBatch) isGroup() bool { return b.repo.RepoType == repotype.Group }

// run loads what the batch needs once and decides every query.
func (b *availabilityBatch) run(ctx context.Context, canRead func(*models.Repository) bool) error {
	if b.repo.Blocked {
		for i := range b.results {
			b.decide(i, b.repo.Name, StatusHeld, blockedResponse())
		}

		return nil
	}

	if err := b.loadMembers(ctx, canRead); err != nil {
		return err
	}

	if err := b.loadGates(ctx); err != nil {
		return err
	}

	if err := b.loadHosted(ctx); err != nil {
		return err
	}

	return b.decideAll(ctx)
}

// loadMembers sets the members consulted: the repository itself, or the members
// of a group the way group.Handler.members and the dispatcher's read check
// select them.
func (b *availabilityBatch) loadMembers(ctx context.Context, canRead func(*models.Repository) bool) error {
	if !b.isGroup() {
		b.members = []*models.Repository{b.repo}
	} else {
		repos, err := b.db.ListGroupMemberRepositories(ctx, b.repo.ID)
		if err != nil {
			return fmt.Errorf("list group members: %w", err)
		}

		for _, m := range repos {
			if m.RepoType == repotype.Group || m.Format != b.repo.Format || m.Blocked || !canRead(m) {
				continue
			}

			b.members = append(b.members, m)
		}
	}

	for _, m := range b.members {
		if m.RepoType == repotype.Hosted {
			b.hosted = append(b.hosted, m)
		}
	}

	return nil
}

// loadGates builds the gate of every proxy member, loading its curation rules once.
func (b *availabilityBatch) loadGates(ctx context.Context) error {
	b.gates = map[string]curation.Gate{}

	for _, m := range b.members {
		if m.RepoType != repotype.Proxy {
			continue
		}

		var rules []curation.Rule

		if m.CurationMode != "" && m.CurationMode != curation.ModeOff {
			var err error

			if rules, err = b.db.ListCurationRulesUncached(ctx, m.ID); err != nil {
				return fmt.Errorf("list curation rules: %w", err)
			}
		}

		b.gates[m.ID] = curation.NewGate(m.Format, m.CurationMode, rules, m.MinAgeHours, b.now)
	}

	return nil
}

// loadHosted reads, in one query each, which names the hosted members hold (only
// a group that does not merge proxy versions needs it) and which of the queried
// versions they store, with their holds.
func (b *availabilityBatch) loadHosted(ctx context.Context) error {
	b.owned, b.stored = map[int]bool{}, map[storedKey]models.VersionHolds{}

	if len(b.hosted) == 0 {
		return nil
	}

	ids := make([]string, len(b.hosted))
	for i, m := range b.hosted {
		ids[i] = m.ID
	}

	if b.isGroup() && !b.repo.MergeProxyVersions {
		names := make([]string, len(b.queries))
		for i, q := range b.queries {
			names[i] = q.Name
		}

		held, err := b.db.HostedNamesHeld(ctx, ids, names)
		if err != nil {
			return fmt.Errorf("hosted names held: %w", err)
		}

		for _, h := range held {
			b.owned[h.Index] = true
		}
	}

	stored, err := b.db.HostedVersionsStored(ctx, ids, b.queries)
	if err != nil {
		return fmt.Errorf("hosted versions stored: %w", err)
	}

	for _, s := range stored {
		b.stored[storedKey{s.Index, s.RepoID}] = s.VersionHolds
	}

	return nil
}

// decideAll walks every query through its members, reading the cached metadata
// of the proxy members a query reaches in rounds, until every query is decided.
func (b *availabilityBatch) decideAll(ctx context.Context) error {
	for {
		need := map[cacheKey]*models.Repository{}

		for i := range b.results {
			if !b.results[i].decided {
				b.advance(i, need)
			}
		}

		if len(need) == 0 {
			return nil
		}

		if err := b.readCached(ctx, need); err != nil {
			return err
		}
	}
}

// candidates are the members that may decide query i: the hosted members alone
// when one of them holds the name (hosted wins), otherwise every member.
func (b *availabilityBatch) candidates(i int) []*models.Repository {
	if b.owned[i] {
		return b.hosted
	}

	return b.members
}

// advance consults the members of query i from its cursor on until one decides
// it, or until a proxy member whose cached metadata has not been read yet; that
// read is added to need.
func (b *availabilityBatch) advance(i int, need map[cacheKey]*models.Repository) {
	q := b.queries[i]
	members := b.candidates(i)

	for ; b.results[i].cursor < len(members); b.results[i].cursor++ {
		m := members[b.results[i].cursor]

		if m.RepoType == repotype.Hosted {
			if holds, ok := b.stored[storedKey{i, m.ID}]; ok {
				b.decideHosted(i, m, holds)
				return
			}

			continue
		}

		key := cacheKey{m.ID, q.Name}

		cv, read := b.cached[key]
		if !read {
			need[key] = m
			return
		}

		if !cv.ok {
			b.decide(i, m.Name, StatusUnknown, unknownResponse())
			return
		}

		if published, has := cv.versions[q.Version]; has {
			b.decideProxy(i, m, published)
			return
		}
	}

	b.decide(i, b.repo.Name, StatusNotFound, b.notFoundResponse(i))
}

// readCached reads the cached metadata of the (proxy member, package) pairs in
// need, at most proxyReadConcurrency at a time. A read has no error to report:
// CachedVersions answers ok=false when it cannot say, which is the "unknown"
// state, so the reads run under a plain WaitGroup and semaphore.
func (b *availabilityBatch) readCached(ctx context.Context, need map[cacheKey]*models.Repository) error {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, proxyReadConcurrency)
	)

	for key, m := range need {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			cv := cachedVersions{}

			if b.published != nil {
				if list, ok := b.published.CachedVersions(ctx, m, key.pkg); ok {
					cv = cachedVersions{versions: datedVersions(list), ok: true}
				}
			}

			mu.Lock()
			b.cached[key] = cv
			mu.Unlock()
		})
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("read cached metadata: %w", err)
	}

	return nil
}

func datedVersions(list []proxy.DatedVersion) map[string]time.Time {
	out := make(map[string]time.Time, len(list))
	for _, v := range list {
		out[v.Version] = v.Published
	}

	return out
}

func (b *availabilityBatch) decide(i int, repository, status string, resp responses.AvailabilityResponse) {
	resp.Available = status == StatusAvailable
	b.results[i] = batchResult{decided: true, out: responses.AvailabilityBatchResult{
		Status: status, Repository: repository, AvailabilityResponse: resp,
	}}
}

// decideHosted decides query i on the stored version of hosted member m.
func (b *availabilityBatch) decideHosted(i int, m *models.Repository, holds models.VersionHolds) {
	resp := responses.AvailabilityResponse{Reasons: holdsReasons(holds), MinAge: minAgeOff}

	status := StatusAvailable
	if len(resp.Reasons) > 0 {
		status = StatusHeld
	}

	b.decide(i, m.Name, status, resp)
}

// decideProxy decides query i on proxy member m, whose cached metadata lists the
// version, with the gate of m (published is zero when the metadata dates none).
func (b *availabilityBatch) decideProxy(i int, m *models.Repository, published time.Time) {
	q := b.queries[i]
	gate := b.gates[m.ID]
	resp := responses.AvailabilityResponse{Reasons: []responses.AvailabilityReason{}, MinAge: minAgeOff}

	if !published.IsZero() {
		resp.PublishedAt = published.UTC().Format(time.RFC3339)
	}

	switch {
	case !gate.AgeActive():
	case published.IsZero():
		resp.MinAge = minAgeUnknown
	default:
		resp.MinAge = minAgeChecked
	}

	status := StatusAvailable
	if hidden, hold := gate.Hidden(q.Name, q.Version, published); hidden {
		resp.Reasons = append(resp.Reasons, holdReason(hold))
		status = StatusHeld
	}

	b.decide(i, m.Name, status, resp)
}

// notFoundResponse says why no member has query i.
func (b *availabilityBatch) notFoundResponse(i int) responses.AvailabilityResponse {
	detail := "no member of the group has this version"

	switch {
	case b.isGroup() && b.owned[i]:
		detail = "a hosted member holds this package but not this version; with merge_proxy_versions off, proxies are not consulted"
	case b.isGroup():
	case b.repo.RepoType == repotype.Hosted:
		detail = "the repository stores no such version"
	default:
		detail = "the cached upstream metadata does not list this version"
	}

	return responses.AvailabilityResponse{
		Reasons: []responses.AvailabilityReason{{Source: sourceNotFound, Detail: detail}}, MinAge: minAgeOff,
	}
}

func blockedResponse() responses.AvailabilityResponse {
	return responses.AvailabilityResponse{
		Reasons: []responses.AvailabilityReason{{Source: sourceRepositoryBlocked, Detail: "the repository is blocked"}}, MinAge: minAgeOff,
	}
}

func unknownResponse() responses.AvailabilityResponse {
	return responses.AvailabilityResponse{
		Reasons: []responses.AvailabilityReason{{
			Source: sourceUnknown,
			Detail: "the proxy has not cached the upstream metadata of this package (or its format lists no versions), " +
				"so whether the version exists is unknown",
		}},
		MinAge: minAgeOff,
	}
}

// holdsReasons states the quarantine and license-policy holds of a stored version.
func holdsReasons(holds models.VersionHolds) []responses.AvailabilityReason {
	reasons := []responses.AvailabilityReason{}

	if holds.Quarantined {
		reasons = append(reasons, responses.AvailabilityReason{
			Source: sourceQuarantine, Detail: "a file of the version is quarantined by a vulnerability or secret scan",
		})
	}

	if holds.LicenseBlocked {
		reasons = append(reasons, responses.AvailabilityReason{
			Source: sourceLicensePolicy, Detail: "a file of the version is held by the repository's license policy",
		})
	}

	return reasons
}
