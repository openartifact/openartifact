// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"container/list"
	"context"
	"sync"

	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
)

const (
	// debStanzaCacheBytes bounds the rendered stanzas a worker keeps across
	// generations. A repository of 20 000 packages takes about 20 MB; the least
	// recently merged repositories go first.
	debStanzaCacheBytes = 256 << 20
	// debControlBatch is the number of files whose control fields are read per
	// statement.
	debControlBatch = 2000
	// debStanzaOverhead approximates what a cached stanza holds besides its text:
	// the stamp's strings and the map slot.
	debStanzaOverhead = 400
)

// debStanzaStore is the part of the database the stanza cache reads.
type debStanzaStore interface {
	ListDebPackageStamps(ctx context.Context, repoID string) ([]models.DebPackageStamp, error)
	ListDebPackagesWithControls(ctx context.Context, repoID string, ids []string) ([]models.DebPackageStamp, error)
}

// debFile is a hosted deb file as a group merges it: the stamp it was read
// with (without control fields) and its rendered Packages stanza.
type debFile struct {
	stamp models.DebPackageStamp
	text  string
}

// debStanzaCache remembers the Packages stanzas a deb group rendered for the
// files of hosted members, so that regenerating a group after one upload reads
// and renders only the new file instead of every file of the member. It is a
// cache of pure functions: a stanza is stored under the stamp it was rendered
// from and used only for an equal stamp, so it can never be served for a
// changed file, and a process that has just started simply has an empty one.
type debStanzaCache struct {
	mu    sync.Mutex
	limit int64
	size  int64
	// lru holds *debRepoStanzas, most recently used first.
	lru   *list.List
	repos map[string]*list.Element
}

// debRepoStanzas is the stanzas of one repository's live files by file id. The
// map is replaced, never changed, once published.
type debRepoStanzas struct {
	repoID string
	files  map[string]*debFile
	bytes  int64
}

func newDebStanzaCache(limit int64) *debStanzaCache {
	return &debStanzaCache{limit: limit, lru: list.New(), repos: make(map[string]*list.Element)}
}

// snapshot returns the repository's cached files (nil if none) and marks it
// recently used.
func (c *debStanzaCache) snapshot(repoID string) map[string]*debFile {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.repos[repoID]
	if !ok {
		return nil
	}

	c.lru.MoveToFront(el)

	return el.Value.(*debRepoStanzas).files //nolint:forcetypeassert // the list only holds *debRepoStanzas
}

// store publishes the repository's files, replacing what it had, and evicts the
// least recently used repositories until the cache is within its limit. A
// repository that does not fit on its own is not kept.
func (c *debStanzaCache) store(repoID string, files map[string]*debFile) {
	var bytes int64

	for _, f := range files {
		bytes += int64(len(f.text)) + debStanzaOverhead
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.repos[repoID]; ok {
		c.size -= el.Value.(*debRepoStanzas).bytes //nolint:forcetypeassert // the list only holds *debRepoStanzas
		c.lru.Remove(el)
		delete(c.repos, repoID)
	}

	if bytes > c.limit {
		return
	}

	c.repos[repoID] = c.lru.PushFront(&debRepoStanzas{repoID: repoID, files: files, bytes: bytes})
	c.size += bytes

	for c.size > c.limit {
		el := c.lru.Back()
		old := el.Value.(*debRepoStanzas) //nolint:forcetypeassert // the list only holds *debRepoStanzas
		c.size -= old.bytes
		c.lru.Remove(el)
		delete(c.repos, old.repoID)
	}
}

// files returns the live files of the hosted repository with their stanzas, in
// the order of ListDebPackageStamps. Files the cache has a stanza for under
// their current stamp are not read again; the others are read with their
// controls and rendered.
//
// The listing and the control reads are separate statements. A file deleted in
// between is left out; one replaced in between is listed as it is by then. Either
// way the change notified a regeneration of its own that converges on the
// state.
func (c *debStanzaCache) files(ctx context.Context, db debStanzaStore, repoID string) ([]debFile, error) {
	stamps, err := db.ListDebPackageStamps(ctx, repoID)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the repository
	}

	cached := c.snapshot(repoID)
	out := make([]debFile, len(stamps))

	var missing []string

	for i := range stamps {
		if f := cached[stamps[i].ID]; f != nil && f.stamp.EqualStamp(&stamps[i]) {
			out[i] = *f
			continue
		}

		out[i].stamp = stamps[i]
		missing = append(missing, stamps[i].ID)
	}

	if len(missing) == 0 && len(cached) == len(out) {
		return out, nil // nothing was added, changed or removed
	}

	if len(missing) > 0 {
		if err := c.render(ctx, db, repoID, out, missing); err != nil {
			return nil, err
		}
	}

	live := out[:0]
	files := make(map[string]*debFile, len(out))

	for i := range out {
		if out[i].text == "" {
			continue // deleted since the listing
		}

		live = append(live, out[i])
		files[out[i].stamp.ID] = &live[len(live)-1]
	}

	c.store(repoID, files)

	return live, nil
}

// render reads the controls of the files among missing and fills in their stanza
// (and the stamp they were read with) in out.
func (c *debStanzaCache) render(ctx context.Context, db debStanzaStore, repoID string, out []debFile, missing []string) error {
	pos := make(map[string]int, len(missing))

	for i := range out {
		if out[i].text == "" {
			pos[out[i].stamp.ID] = i
		}
	}

	for len(missing) > 0 {
		n := min(debControlBatch, len(missing))
		batch := missing[:n]
		missing = missing[n:]

		rows, err := db.ListDebPackagesWithControls(ctx, repoID, batch)
		if err != nil {
			return err //nolint:wrapcheck // the caller names the repository
		}

		for i := range rows {
			at, ok := pos[rows[i].ID]
			if !ok {
				continue
			}

			text := formatdeb.FormatPackagesEntry(rows[i].DebPackageEntry)
			rows[i].ControlFields = nil
			out[at] = debFile{stamp: rows[i], text: text}
		}
	}

	return nil
}
