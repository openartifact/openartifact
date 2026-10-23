// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	sq "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

const (
	// groupPartialRetry is how soon a generation that could not read every
	// proxy member (an upstream failure, not a missing file) is redone.
	groupPartialRetry = time.Minute
	// groupMinRefresh bounds how often proxy members' upstream metadata is
	// re-read, whatever their cache TTL is configured to.
	groupMinRefresh = time.Minute
	// groupLockPoll is how often an on-demand generation retries a lock another
	// generation holds.
	groupLockPoll = 250 * time.Millisecond
	// groupOwnerChunk is the number of owner rows inserted per statement.
	groupOwnerChunk = 5000
)

// MemberOpener reads files of a proxy member as the proxy serves them
// (proxy.Service).
type MemberOpener interface {
	Open(ctx context.Context, repo *models.Repository, relPath string) (io.ReadCloser, error)
}

// RPMPackageLoader returns every package of a hosted rpm repository with the
// data its metadata needs (RPMetadataWorker.PackagesForMetadata).
type RPMPackageLoader interface {
	PackagesForMetadata(ctx context.Context, repoID string) ([]pkgrpm.Package, error)
}

// GroupMetadataWorker generates the metadata of deb and rpm group
// repositories: the Release/Packages indexes or repodata a client needs, merged
// across the group's hosted and proxy members in member order, plus the table
// of which member owns which file so downloads are served from the member
// whose checksum the index lists.
//
// The result is stored in the group's own repository_metadata rows. It is
// regenerated, through a coalescing queue, whenever a member (or the group)
// changes (NotifyStale, NotifyMemberChanged), and refreshed in the background
// once a proxy member's cached upstream metadata may have changed.
type GroupMetadataWorker struct {
	db        sq.Database
	blobStore storage.BlobStore
	opener    MemberOpener
	rpm       RPMPackageLoader
	signers   gpg.Resolver
	log       *logger.Logger
	q         *regenQueue[string]
}

// NewGroupMetadataWorker creates the worker. signers resolves the key that signs
// each group's metadata (nil, or a nil result, for an unsigned group); it is
// asked per generation so a key change takes effect.
func NewGroupMetadataWorker(db sq.Database, blobStore storage.BlobStore, opener MemberOpener, rpm RPMPackageLoader, signers gpg.Resolver, log *logger.Logger) *GroupMetadataWorker {
	w := &GroupMetadataWorker{db: db, blobStore: blobStore, opener: opener, rpm: rpm, signers: signers, log: log}
	w.q = newRegenQueue("group", log, func(ctx context.Context, groupID string) error {
		return w.generate(ctx, groupID, "", false, nil)
	})

	return w
}

// WithDebounce sets the coalescing delay of NotifyStale.
func (w *GroupMetadataWorker) WithDebounce(d time.Duration) *GroupMetadataWorker {
	w.q.setDebounce(d)
	return w
}

// WithMetrics records regeneration counts and durations in reg.
func (w *GroupMetadataWorker) WithMetrics(reg *metrics.Registry) *GroupMetadataWorker {
	w.q.setMetrics(reg)
	return w
}

// WithRegenLimiter shares a concurrency limiter with the other metadata workers.
func (w *GroupMetadataWorker) WithRegenLimiter(l *RegenLimiter) *GroupMetadataWorker {
	w.q.setLimiter(l)
	return w
}

// Stop drains pending regenerations and waits for running ones (bounded).
func (w *GroupMetadataWorker) Stop() { w.q.Stop() }

// NotifyStale records that the group's metadata is out of date and schedules a
// coalesced regeneration.
func (w *GroupMetadataWorker) NotifyStale(groupID string) {
	if err := w.db.MarkGroupMetadataStale(context.Background(), groupID); err != nil {
		w.log.Errorw("group worker: failed to mark metadata stale", "group_id", groupID, "error", err)
	}

	w.q.Notify(groupID)
}

// NotifyMemberChanged marks every deb/rpm group that lists repoID as a member
// stale. It is the hook of the hosted workers' NotifyStale: uploads, deletes,
// trash and restores of a member all end up there.
func (w *GroupMetadataWorker) NotifyMemberChanged(repoID string) {
	ids, err := w.db.ListMetadataGroupsOfMember(context.Background(), repoID)
	if err != nil {
		w.log.Errorw("group worker: failed to list groups of member", "repo_id", repoID, "error", err)
		return
	}

	for _, id := range ids {
		w.NotifyStale(id)
	}
}

// Refresh schedules a regeneration unless one is already waiting or running.
func (w *GroupMetadataWorker) Refresh(groupID string) { w.q.notifyIfIdle(groupID) }

// RegenerateStale schedules every group whose metadata is stale or was never
// generated; it runs once at startup (a restored backup has no generated group
// metadata, a migration may invalidate it).
func (w *GroupMetadataWorker) RegenerateStale(ctx context.Context) {
	ids, err := w.db.ListStaleMetadataGroups(ctx)
	if err != nil {
		w.log.Warnw("group worker: listing stale groups failed", "error", err)
		return
	}

	for _, id := range ids {
		w.q.Notify(id)
	}
}

// Generate synchronously generates the metadata of the group, waiting for a
// generation another process runs. dist is a Debian distribution a client asked
// for: group.ErrDistNotFound is returned when no member has one the group does
// not serve yet.
//
// readable are the IDs of the members the caller may read. The metadata a caller
// is served lists only those members' packages: when they are not all of the
// group's members, it is generated (and signed) as a variant of its own, stored
// under Scope(readable), and dropped whenever the group is regenerated.
func (w *GroupMetadataWorker) Generate(ctx context.Context, groupID, dist string, readable []string) error {
	return w.generate(ctx, groupID, dist, true, readable)
}

// variantPrefix prefixes the paths of the metadata variants of a group; no path
// of a deb or rpm repository starts with it.
const variantPrefix = ".oa-variant/"

// Scope returns the path prefix under which the metadata for a caller who can
// read the members with the given IDs is stored: empty when that is every
// member the group lists (the common case, one shared index), a per-subset
// prefix otherwise. ok is false when the caller can read none of them.
func (w *GroupMetadataWorker) Scope(ctx context.Context, groupID string, readable []string) (string, bool, error) {
	grp, err := w.db.GetRepositoryByID(ctx, groupID)
	if err != nil || grp == nil {
		return "", false, fmt.Errorf("group worker: loading group %s: %w", groupID, err)
	}

	members, err := w.liveMembers(ctx, grp)
	if err != nil {
		return "", false, err
	}

	subset, scope := scopeMembers(members, readable)

	return scope, len(subset) > 0, nil
}

// scopeMembers narrows members (in priority order) to the readable IDs and
// names the variant of that subset: "" for all of them, else a prefix keyed by
// the sorted member IDs. A nil readable means every member.
func scopeMembers(members []*models.Repository, readable []string) ([]*models.Repository, string) {
	if readable == nil {
		return members, ""
	}

	subset := make([]*models.Repository, 0, len(members))
	ids := make([]string, 0, len(members))

	for _, m := range members {
		if slices.Contains(readable, m.ID) {
			subset = append(subset, m)
			ids = append(ids, m.ID)
		}
	}

	if len(subset) == len(members) {
		return members, ""
	}

	sort.Strings(ids)

	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))

	return subset, variantPrefix + hex.EncodeToString(sum[:16]) + "/"
}

// groupJob is one generation of a group.
type groupJob struct {
	w       *GroupMetadataWorker
	group   *models.Repository
	members []*models.Repository
	// scope is the variant prefix the files are stored under, "" for the
	// group's shared index.
	scope  string
	signer gpg.Signer
	// blobs are the generated files keyed by group-relative path.
	blobs map[string][]byte
	// owners maps each file the index lists to the member serving it.
	owners map[string]groupOwner
	// partial is set when a proxy member could not be read (as opposed to
	// not having a file): the result is published but refreshed soon.
	partial bool
	// hostedNames are the package names of the hosted members, hidden from
	// proxy members unless the group merges proxy versions.
	hostedNames map[string]struct{}
	// dists are the Debian distributions the generation produced.
	dists []string
}

type groupOwner struct {
	member string
	sha256 string
}

// claim registers member as the owner of path; false means another member
// already owns it for different content, so the entry must be left out.
func (j *groupJob) claim(path, member, sum string) bool {
	if strings.HasPrefix(path, variantPrefix) {
		return false
	}

	if cur, ok := j.owners[path]; ok {
		return cur.sha256 == sum || cur.sha256 == "" || sum == ""
	}

	j.owners[path] = groupOwner{member: member, sha256: sum}

	return true
}

func (w *GroupMetadataWorker) generate(ctx context.Context, groupID, hint string, wait bool, readable []string) error {
	grp, err := w.db.GetRepositoryByID(ctx, groupID)
	if err != nil {
		return fmt.Errorf("group worker: loading group %s: %w", groupID, err)
	}

	if grp == nil || grp.DeletedAt != nil || grp.RepoType != repotype.Group || (grp.Format != "deb" && grp.Format != "rpm") {
		return nil
	}

	all, err := w.liveMembers(ctx, grp)
	if err != nil {
		return err
	}

	members, scope := scopeMembers(all, readable)
	if len(members) == 0 {
		return group.ErrDistNotFound
	}

	if hint != "" {
		if err := w.checkDist(ctx, grp, hint, members); err != nil {
			return err
		}
	}

	before := w.generatedAt(ctx, groupID)

	release, waited, err := w.lock(ctx, groupID, wait)
	if err != nil {
		return err
	}

	defer release()

	// Another generation, which this one waited for, may have produced what
	// the caller needs.
	if waited && w.freshSince(ctx, grp, hint, scope, before) {
		return nil
	}

	return w.run(ctx, grp, hint, members, scope)
}

// lock takes the group's advisory lock; with wait set it retries until ctx
// ends and reports whether it had to wait.
func (w *GroupMetadataWorker) lock(ctx context.Context, groupID string, wait bool) (func(), bool, error) {
	sqlDB := w.db.GetDB()
	if sqlDB == nil {
		return func() {}, false, nil
	}

	waited := false

	for {
		release, err := lockForRegeneration(ctx, sqlDB, "group:"+groupID)
		if err == nil {
			return release, waited, nil
		}

		if !wait || !errors.Is(err, errLockBusy) {
			return nil, waited, fmt.Errorf("group worker: group %s: %w", groupID, err)
		}

		waited = true

		select {
		case <-ctx.Done():
			return nil, waited, fmt.Errorf("group worker: group %s: %w", groupID, ctx.Err())
		case <-time.After(groupLockPoll):
		}
	}
}

// generatedAt is when the group's metadata was last generated, zero if never.
func (w *GroupMetadataWorker) generatedAt(ctx context.Context, groupID string) time.Time {
	st, err := w.db.GetGroupMetadataState(ctx, groupID)
	if err != nil || st == nil || st.GeneratedAt == nil {
		return time.Time{}
	}

	return *st.GeneratedAt
}

// freshSince reports whether the group's metadata (and the hinted distribution,
// if any) was generated after the instant before.
func (w *GroupMetadataWorker) freshSince(ctx context.Context, grp *models.Repository, hint, scope string, before time.Time) bool {
	if scope != "" {
		anchor := "repodata/repomd.xml"
		if grp.Format == "deb" {
			anchor = "dists/" + hint + "/Release"
		}

		meta, err := w.db.GetRepoMetadataByPath(ctx, grp.ID, scope+anchor)

		return err == nil && meta != nil
	}

	if !w.generatedAt(ctx, grp.ID).After(before) {
		return false
	}

	if hint == "" {
		return true
	}

	meta, err := w.db.GetRepoMetadataByPath(ctx, grp.ID, "dists/"+hint+"/Release")

	return err == nil && meta != nil
}

// run is one locked generation of grp.
func (w *GroupMetadataWorker) run(ctx context.Context, grp *models.Repository, hint string, members []*models.Repository, scope string) error {
	if err := w.ensureState(ctx, grp.ID); err != nil {
		return err
	}

	st, err := w.db.GetGroupMetadataState(ctx, grp.ID)
	if err != nil {
		return fmt.Errorf("group worker: reading state: %w", err)
	}

	j := &groupJob{
		w: w, group: grp, members: members, scope: scope,
		blobs:  make(map[string][]byte),
		owners: make(map[string]groupOwner),
	}

	if w.signers != nil {
		signer, err := w.signers.SignerForRepo(ctx, grp.ID)
		if err != nil {
			return fmt.Errorf("group worker: resolving signing key of %s: %w", grp.Name, err)
		}

		j.signer = signer
	}

	dists := st.Dists
	if hint != "" && !slices.Contains(dists, hint) {
		dists = append(slices.Clone(dists), hint)
	}

	switch grp.Format {
	case "deb":
		err = j.buildDeb(ctx, dists)
	case "rpm":
		err = j.buildRPM(ctx)
	}

	if err != nil {
		return err
	}

	refresh := j.refreshAfter(time.Now())

	if err := w.publish(ctx, j, st, refresh); err != nil {
		return err
	}

	w.log.Infow("group metadata regeneration complete", "group_id", grp.ID, "group", grp.Name, "format", grp.Format,
		"members", len(members), "files", len(j.owners), "partial", j.partial)

	return nil
}

// liveMembers returns the members a generation reads, in priority order.
func (w *GroupMetadataWorker) liveMembers(ctx context.Context, grp *models.Repository) ([]*models.Repository, error) {
	repos, err := w.db.ListGroupMemberRepositories(ctx, grp.ID)
	if err != nil {
		return nil, fmt.Errorf("group worker: listing members of %s: %w", grp.Name, err)
	}

	out := make([]*models.Repository, 0, len(repos))

	for _, m := range repos {
		if m.RepoType == repotype.Group || m.Format != grp.Format || m.Blocked {
			continue
		}

		out = append(out, m)
	}

	return out, nil
}

func (w *GroupMetadataWorker) ensureState(ctx context.Context, groupID string) error {
	if _, err := w.db.GetDB().ExecContext(ctx,
		`INSERT INTO group_metadata_state (repository_id) VALUES ($1) ON CONFLICT DO NOTHING`, groupID); err != nil {
		return fmt.Errorf("group worker: creating state: %w", err)
	}

	return nil
}

// refreshAfter is when the generation should be redone for proxy members:
// soon after a partial one, else when the shortest proxy cache TTL elapses.
// Nil for a group without proxy members, which only changes through
// notifications.
func (j *groupJob) refreshAfter(now time.Time) *time.Time {
	if j.partial {
		t := now.Add(groupPartialRetry)
		return &t
	}

	var shortest time.Duration

	for _, m := range j.members {
		if m.RepoType != repotype.Proxy {
			continue
		}

		ttl := time.Duration(m.CacheTTLSeconds) * time.Second
		if shortest == 0 || ttl < shortest {
			shortest = ttl
		}
	}

	if shortest == 0 {
		for _, m := range j.members {
			if m.RepoType == repotype.Proxy {
				shortest = groupMinRefresh
				break
			}
		}
	}

	if shortest == 0 {
		return nil
	}

	t := now.Add(max(shortest, groupMinRefresh))

	return &t
}

// publish stores the generated files and the owner table and clears the stale
// flag in one transaction, then reconciles the blob store.
func (w *GroupMetadataWorker) publish(ctx context.Context, j *groupJob, st *models.GroupMetadataState, refresh *time.Time) error {
	groupID := j.group.ID
	format := j.group.Format

	tx, err := w.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("group worker: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	oldSHA, err := listOldGroupMetadata(ctx, tx, groupID, j.scope)
	if err != nil {
		return err
	}

	newSHA := make(map[string]string, len(j.blobs))
	changed := make(map[string][]byte, len(j.blobs))
	attrs := func(path, sum string) map[string]string {
		return map[string]string{"path": path, "format": format, "repo_id": groupID, "sha256": sum}
	}

	for rel, data := range j.blobs {
		path := j.scope + rel
		sum := fmt.Sprintf("%x", sha256.Sum256(data))
		newSHA[path] = sum

		if oldSHA[path] == sum {
			continue
		}

		changed[path] = data

		if _, err := w.blobStore.Put(ctx, bytes.NewReader(data), attrs(path, sum)); err != nil {
			return fmt.Errorf("group worker: storing blob %s: %w", path, err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO repository_metadata (id, repository_id, path, blob_sha256, blob_size, last_generated)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, now())
			ON CONFLICT (repository_id, path) DO UPDATE
			SET blob_sha256 = $3, blob_size = $4, last_generated = now()
		`, groupID, path, sum, int64(len(data))); err != nil {
			return fmt.Errorf("group worker: updating repository_metadata for %s: %w", path, err)
		}
	}

	stalePaths, reclaim := supersededMetadata(oldSHA, newSHA)

	for _, path := range stalePaths {
		if _, err := tx.ExecContext(ctx, `DELETE FROM repository_metadata WHERE repository_id = $1 AND path = $2`, groupID, path); err != nil {
			return fmt.Errorf("group worker: deleting superseded repository_metadata %s: %w", path, err)
		}
	}

	if err := replaceOwners(ctx, tx, groupID, j.scope, j.owners); err != nil {
		return err
	}

	// A variant is derived from the shared index's state and does not change it.
	if j.scope == "" {
		if err := markGenerated(ctx, tx, groupID, st, j.dists, refresh); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("group worker: commit: %w", err)
	}

	blobs := metadataBlobs{db: w.db, store: w.blobStore, log: w.log, who: "group worker"}
	blobs.ensure(ctx, groupID, changed, newSHA, attrs)
	blobs.reclaim(ctx, reclaim)

	return nil
}

// listOldGroupMetadata returns path -> blob sha256 of the group's generated
// rows: all of them for the shared index (so regenerating it drops the
// variants), those under scope for a variant.
func listOldGroupMetadata(ctx context.Context, tx sq.Transaction, groupID, scope string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1 AND ($2 = '' OR starts_with(path, $2))`, groupID, scope)
	if err != nil {
		return nil, fmt.Errorf("group worker: listing repository_metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	old := make(map[string]string)

	for rows.Next() {
		var path, sum string
		if err := rows.Scan(&path, &sum); err != nil {
			return nil, fmt.Errorf("group worker: scanning repository_metadata: %w", err)
		}

		old[path] = sum
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("group worker: listing repository_metadata: %w", err)
	}

	return old, nil
}

// replaceOwners swaps the group's owner table for owners.
func replaceOwners(ctx context.Context, tx sq.Transaction, groupID, scope string, owners map[string]groupOwner) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_package_owners WHERE group_repo_id = $1 AND ($2 = '' OR starts_with(path, $2))`, groupID, scope); err != nil {
		return fmt.Errorf("group worker: clearing owners: %w", err)
	}

	paths := make([]string, 0, len(owners))
	for p := range owners {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	for len(paths) > 0 {
		n := min(groupOwnerChunk, len(paths))
		chunk := paths[:n]
		paths = paths[n:]

		members := make([]string, n)
		scoped := make([]string, n)

		for i, p := range chunk {
			scoped[i] = scope + p
			members[i] = owners[p].member
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO group_package_owners (group_repo_id, path, member_repo_id)
			SELECT $1, t.p, t.m::uuid FROM unnest($2::text[], $3::text[]) AS t(p, m)
		`, groupID, scoped, members); err != nil {
			return fmt.Errorf("group worker: storing owners: %w", err)
		}
	}

	return nil
}

// markGenerated records the generation: the stale flag is only cleared when no
// change was recorded since the state was read (the change's own notification
// schedules the follow-up run).
func markGenerated(ctx context.Context, tx sq.Transaction, groupID string, st *models.GroupMetadataState, dists []string, refresh *time.Time) error {
	if dists == nil {
		dists = []string{}
	}

	distJSON, err := jsonStrings(dists)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE group_metadata_state
		SET generated_at = now(), refresh_after = $2, dists = $3::jsonb, stale = (stale AND updated_at <> $4)
		WHERE repository_id = $1
	`, groupID, refresh, distJSON, st.UpdatedAt); err != nil {
		return fmt.Errorf("group worker: updating state: %w", err)
	}

	return nil
}

// noteFetchError classifies an error from reading a proxy member: a file the
// upstream does not have is absence, anything else a partial generation.
func (j *groupJob) noteFetchError(m *models.Repository, path string, err error) {
	if errors.Is(err, proxy.ErrNotFound) {
		return
	}

	j.partial = true

	j.w.log.Warnw("group worker: proxy member unreadable", "group", j.group.Name, "member", m.Name, "path", path, "error", err)
}

func jsonStrings(v []string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("group worker: encoding distributions: %w", err)
	}

	return string(b), nil
}
