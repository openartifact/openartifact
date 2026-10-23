// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"errors"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
)

const (
	// listPage is how many ids one List call asks for.
	listPage = 500

	// Listing modes reported in a verify result.
	listingStore    = "list"
	listingDatabase = "database"
)

// idStream is a sorted stream of blob ids.
type idStream interface {
	// peek returns the smallest id not yet popped; ok is false at the end.
	peek(ctx context.Context) (id string, ok bool, err error)
	// pop consumes the id peek returned.
	pop()
}

// listStream streams the ids a store lists, page by page.
type listStream struct {
	store     storage.BlobStore
	token     string
	buf       []string
	exhausted bool
}

func (s *listStream) peek(ctx context.Context) (string, bool, error) {
	for len(s.buf) == 0 && !s.exhausted {
		ids, next, err := s.store.List(ctx, s.token, listPage)
		if err != nil {
			return "", false, err //nolint:wrapcheck // callers match ErrListNotSupported
		}

		s.buf, s.token = ids, next
		if next == "" {
			s.exhausted = true
		}
	}

	if len(s.buf) == 0 {
		return "", false, nil
	}

	return s.buf[0], true, nil
}

func (s *listStream) pop() { s.buf = s.buf[1:] }

// dbStream streams the ids of the blobs table (keyset pages).
type dbStream struct {
	queue     database.BlobReplicationStore
	after     string
	buf       []string
	exhausted bool
}

func (s *dbStream) peek(ctx context.Context) (string, bool, error) {
	if len(s.buf) == 0 && !s.exhausted {
		ids, err := s.queue.ListBlobSHAs(ctx, s.after, listPage)
		if err != nil {
			return "", false, err //nolint:wrapcheck // wrapped by the database layer
		}

		s.buf = ids
		if len(ids) < listPage {
			s.exhausted = true
		}

		if len(ids) > 0 {
			s.after = ids[len(ids)-1]
		}
	}

	if len(s.buf) == 0 {
		return "", false, nil
	}

	return s.buf[0], true, nil
}

func (s *dbStream) pop() { s.buf = s.buf[1:] }

// mergeStream merges sorted streams into one, dropping duplicates (a blob can
// be on two tiers while it moves).
type mergeStream struct {
	parts []idStream
}

func (m *mergeStream) min(ctx context.Context) (string, bool, error) {
	var (
		best  string
		found bool
	)

	for _, p := range m.parts {
		id, ok, err := p.peek(ctx)
		if err != nil {
			return "", false, err
		}

		if ok && (!found || id < best) {
			best, found = id, true
		}
	}

	return best, found, nil
}

func (m *mergeStream) peek(ctx context.Context) (string, bool, error) { return m.min(ctx) }

func (m *mergeStream) pop() {
	// Every part's head was peeked by min; drop the ones equal to the smallest.
	best, ok, err := m.min(context.Background())
	if err != nil || !ok {
		return
	}

	for _, p := range m.parts {
		if id, ok, _ := p.peek(context.Background()); ok && id == best {
			p.pop()
		}
	}
}

// skipThrough drops the ids up to and including cursor.
func skipThrough(ctx context.Context, s idStream, cursor string) error {
	if cursor == "" {
		return nil
	}

	for {
		id, ok, err := s.peek(ctx)
		if err != nil {
			return err
		}

		if !ok || id > cursor {
			return nil
		}

		s.pop()
	}
}

// tierLister is implemented by the tiered store: it exposes the stores of its
// tiers, because its own List is not sorted across them.
type tierLister interface {
	TierStores() []storage.BlobStore
}

// walker walks the ids of both sides in ascending order.
type walker struct {
	p, r idStream // r is nil when the replica cannot list
	mode string
	// cursor is the last id handed out; the walker is only reused while it
	// equals the persisted cursor.
	cursor string
}

// item is an id the walk found on at least one side.
type item struct {
	id string
	// fromDB is true when the id comes from the blobs table, not a listing of
	// the primary store: it may be listed without having bytes.
	fromDB bool
}

// primaryStream builds the primary side: every tier's listing merged, or the
// blobs table when the primary cannot enumerate its objects.
func (v *Verifier) primaryStream(ctx context.Context, cursor string) (idStream, string, error) {
	stores := []storage.BlobStore{v.inner}
	if tl, ok := v.inner.(tierLister); ok {
		stores = tl.TierStores()
	}

	var parts []idStream

	for i, st := range stores {
		ls := &listStream{store: st}
		if _, _, err := ls.peek(ctx); err != nil {
			if errors.Is(err, storage.ErrListNotSupported) {
				if i == 0 {
					return v.dbStream(cursor), listingDatabase, nil
				}

				v.log.Warnw("replication verify: a storage tier cannot list its blobs and is skipped")

				continue
			}

			return nil, "", err
		}

		parts = append(parts, ls)
	}

	if len(parts) == 0 {
		return v.dbStream(cursor), listingDatabase, nil
	}

	var s idStream = &mergeStream{parts: parts}
	if len(parts) == 1 {
		s = parts[0]
	}

	return s, listingStore, skipThrough(ctx, s, cursor)
}

func (v *Verifier) dbStream(cursor string) idStream {
	return &dbStream{queue: v.queue, after: cursor}
}

// replicaStream lists the replica; nil when it cannot enumerate.
func (v *Verifier) replicaStream(ctx context.Context, cursor string) (idStream, error) {
	ls := &listStream{store: v.replica}
	if _, _, err := ls.peek(ctx); err != nil {
		if errors.Is(err, storage.ErrListNotSupported) {
			return nil, nil //nolint:nilnil // nil means "cannot list"
		}

		return nil, err
	}

	return ls, skipThrough(ctx, ls, cursor)
}

// newWalker starts a walk after cursor.
func (v *Verifier) newWalker(ctx context.Context, cursor string) (*walker, error) {
	p, mode, err := v.primaryStream(ctx, cursor)
	if err != nil {
		return nil, err
	}

	r, err := v.replicaStream(ctx, cursor)
	if err != nil {
		return nil, err
	}

	return &walker{p: p, r: r, mode: mode, cursor: cursor}, nil
}

// next returns up to n ids in ascending order and whether the walk ended.
func (w *walker) next(ctx context.Context, n int) ([]item, bool, error) {
	items := make([]item, 0, n)

	for len(items) < n {
		pid, pok, err := w.p.peek(ctx)
		if err != nil {
			return items, false, err
		}

		var (
			rid string
			rok bool
		)

		if w.r != nil {
			if rid, rok, err = w.r.peek(ctx); err != nil {
				return items, false, err
			}
		}

		if !pok && !rok {
			return items, true, nil
		}

		fromDB := w.mode == listingDatabase

		switch {
		case pok && (!rok || pid <= rid):
			items = append(items, item{id: pid, fromDB: fromDB})
			w.p.pop()

			if rok && rid == pid {
				w.r.pop()
			}
		default:
			items = append(items, item{id: rid})
			w.r.pop()
		}

		w.cursor = items[len(items)-1].id
	}

	return items, false, nil
}
