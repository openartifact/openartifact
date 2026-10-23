// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	listPage = 1000

	// retainBudget bounds how many bytes of sidecar row images stay in memory
	// between the validation pass and the apply pass; larger sets are re-read.
	retainBudget = 256 << 20
)

// ErrListUnsupported is returned when the blob store cannot enumerate its
// objects: without a listing there is nothing to rebuild from.
var ErrListUnsupported = errors.New("the blob store cannot list its objects, so a rebuild from it is impossible")

// Hooks are the post-restore steps that need server wiring (metadata workers).
// Either may be nil.
type Hooks struct {
	// ReconcileUsage recomputes storage usage counters, as a backup restore does.
	ReconcileUsage func(ctx context.Context) error
	// Regenerate schedules the derived metadata (repodata, dists, maven-metadata,
	// packuments) of the repositories that received rows and returns once the
	// work is done.
	Regenerate func(ctx context.Context, repoIDs, npmPackageIDs []string) error
}

// RebuildOptions configures a Rebuilder.
type RebuildOptions struct {
	DryRun          bool
	Deep            bool
	PruneSuperseded bool
	Concurrency     int
}

// Rebuilder re-creates journaled rows from sidecars found in a blob store.
type Rebuilder struct {
	db    *sql.DB
	store storage.BlobStore
	log   *logger.Logger
	opts  RebuildOptions
	hooks Hooks
}

// NewRebuilder creates a Rebuilder.
func NewRebuilder(db *sql.DB, store storage.BlobStore, log *logger.Logger, opts RebuildOptions, hooks Hooks) *Rebuilder {
	opts.Concurrency = max(opts.Concurrency, 1)

	return &Rebuilder{db: db, store: store, log: log, opts: opts, hooks: hooks}
}

// entry is a validated sidecar.
type entry struct {
	table *Table
	key   string
	id    string
	at    time.Time
	row   []byte // retained row image, or nil (re-read on apply)
}

type scanResult struct {
	ids        idSet
	candidates []string
}

// idSet is the sorted set of 32-byte object ids found by the listing.
type idSet struct {
	b      []byte
	sorted bool
}

func (s *idSet) add(id string) {
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 32 {
		return
	}

	if n := len(s.b); n >= 32 && bytes.Compare(s.b[n-32:], raw) > 0 {
		s.sorted = false
	}

	s.b = append(s.b, raw...)
}

func (s *idSet) finish() {
	if s.sorted {
		return
	}

	n := len(s.b) / 32
	idx := make([]int, n)

	for i := range idx {
		idx[i] = i
	}

	sort.Slice(idx, func(a, c int) bool {
		return bytes.Compare(s.b[idx[a]*32:idx[a]*32+32], s.b[idx[c]*32:idx[c]*32+32]) < 0
	})

	out := make([]byte, 0, len(s.b))
	for _, i := range idx {
		out = append(out, s.b[i*32:i*32+32]...)
	}

	s.b, s.sorted = out, true
}

func (s *idSet) has(id string) bool {
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 32 {
		return false
	}

	n := len(s.b) / 32
	i := sort.Search(n, func(i int) bool { return bytes.Compare(s.b[i*32:i*32+32], raw) >= 0 })

	return i < n && bytes.Equal(s.b[i*32:i*32+32], raw)
}

func (s *idSet) len() int { return len(s.b) / 32 }

// Run lists the store, validates the sidecars and restores the rows missing
// from the database. It never updates or deletes an existing row.
func (r *Rebuilder) Run(ctx context.Context) (*Report, error) {
	rep := &Report{
		DryRun: r.opts.DryRun, StartedAt: time.Now().UTC(), SchemaVersion: SchemaVersion(),
		NotRecoverable: notRecoverable, ReposRecovered: []string{},
	}

	scan, err := r.scan(ctx, rep)
	if err != nil {
		return rep, err
	}

	if rep.ObjectsListed > 0 && len(scan.candidates) == 0 {
		rep.Warnings = append(rep.Warnings, "the store holds objects but none is marked as a sidecar: wrong store or storage settings, a journal that was disabled, "+
			"or a copy that dropped object metadata (try --deep)")
	}

	winners, err := r.fetch(ctx, rep, scan.candidates)
	if err != nil {
		return rep, err
	}

	if err := r.apply(ctx, rep, scan.ids, winners); err != nil {
		return rep, err
	}

	rep.FinishedAt = time.Now().UTC()

	return rep, nil
}

// scan lists every object and picks the sidecar candidates by their metadata
// marker (with --deep also by their first bytes).
func (r *Rebuilder) scan(ctx context.Context, rep *Report) (*scanResult, error) {
	res := &scanResult{ids: idSet{sorted: true}}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	sem := make(chan struct{}, r.opts.Concurrency)
	firstErr := atomic.Pointer[error]{}

	token := ""

	for {
		ids, next, err := r.store.List(ctx, token, listPage)
		if errors.Is(err, storage.ErrListNotSupported) {
			return nil, ErrListUnsupported
		}

		if err != nil {
			return nil, fmt.Errorf("list blob store: %w", err)
		}

		for _, id := range ids {
			res.ids.add(id)

			sem <- struct{}{}

			wg.Go(func() {
				defer func() { <-sem }()

				ok, err := r.isSidecarCandidate(ctx, id)
				if err != nil {
					firstErr.CompareAndSwap(nil, &err)

					return
				}

				if ok {
					mu.Lock()
					res.candidates = append(res.candidates, id)
					mu.Unlock()
				}
			})
		}

		rep.ObjectsListed += len(ids)

		if next == "" || len(ids) == 0 {
			break
		}

		token = next
	}

	wg.Wait()

	if e := firstErr.Load(); e != nil {
		return nil, *e
	}

	res.ids.finish()
	sort.Strings(res.candidates)
	rep.SidecarCandidate = len(res.candidates)

	return res, nil
}

func (r *Rebuilder) isSidecarCandidate(ctx context.Context, id string) (bool, error) {
	meta, err := r.store.GetMetadata(ctx, id)
	if err == nil && IsSidecarMeta(meta) {
		return true, nil
	}

	if !r.opts.Deep {
		return false, nil
	}

	rc, err := r.store.Get(ctx, id)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		return false, nil // vanished or unreadable: not a sidecar we can use
	}

	defer func() { _ = rc.Close() }()

	buf := make([]byte, SidecarPrefixLen())
	if _, err := io.ReadFull(rc, buf); err != nil {
		return false, nil // shorter than the prefix: not a sidecar
	}

	return LooksLikeSidecar(buf), nil
}

// readSidecar fetches and validates one sidecar blob.
func (r *Rebuilder) readSidecar(ctx context.Context, id string) (*Sidecar, *Table, error) {
	rc, err := r.store.Get(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("read sidecar: %w", err)
	}

	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, MaxSidecarBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read sidecar: %w", err)
	}

	return DecodeSidecar(id, data)
}

// fetch validates every candidate and keeps, per row, the sidecar with the
// newest written_at (ties: the greater blob id). Older ones are reported as
// superseded, and deleted with --prune-superseded.
func (r *Rebuilder) fetch(ctx context.Context, rep *Report, candidates []string) (map[string]*entry, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		retained atomic.Int64
		ctxErr   error
	)

	winners := map[string]*entry{}

	var superseded []string

	sem := make(chan struct{}, r.opts.Concurrency)

	for _, id := range candidates {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			sc, t, err := r.readSidecar(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					mu.Lock()
					ctxErr = ctx.Err()
					mu.Unlock()

					return
				}

				r.classifyBad(rep, id, err)

				return
			}

			e := &entry{table: t, key: sc.Key, id: id, at: sc.WrittenAt}
			if retained.Add(int64(len(sc.Row))) <= retainBudget {
				e.row = sc.Row
			}

			mu.Lock()
			defer mu.Unlock()

			rep.SidecarsValid++

			k := t.Name + "\x00" + sc.Key

			cur, ok := winners[k]

			switch {
			case !ok:
				winners[k] = e
			case newer(e, cur):
				superseded = append(superseded, cur.id)
				winners[k] = e
			default:
				superseded = append(superseded, e.id)
			}
		})
	}

	wg.Wait()

	if ctxErr != nil {
		return nil, ctxErr
	}

	rep.Superseded = len(superseded)

	if r.opts.PruneSuperseded && !r.opts.DryRun {
		for _, id := range superseded {
			// Sidecars have no blobs row (see Journal): plain Delete is correct.
			if err := r.store.Delete(ctx, id); err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
				rep.addFailure(Item{Blob: id, Reason: "prune superseded sidecar: " + err.Error()})

				continue
			}

			rep.Pruned++
		}
	}

	return winners, nil
}

func newer(a, b *entry) bool {
	if !a.at.Equal(b.at) {
		return a.at.After(b.at)
	}

	return a.id > b.id
}

func (r *Rebuilder) classifyBad(rep *Report, id string, err error) {
	rep.mu.Lock()

	switch {
	case errors.Is(err, ErrSidecarVersion), errors.Is(err, ErrSidecarTable):
		rep.SidecarsUnsupported++
		rep.mu.Unlock()

		return
	}

	rep.mu.Unlock()

	rep.addInvalid(Item{Blob: id, Reason: err.Error()})
}
