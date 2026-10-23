// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// placementCacheTTL bounds how long a cold placement is trusted. A stale
	// entry can only cause a miss on the cached tier (blobs are content
	// addressed, so bytes found anywhere are the right bytes); the miss falls
	// back to the authoritative placement.
	placementCacheTTL = time.Minute
	// placementCacheMax caps the cache; it is cleared when full.
	placementCacheMax = 20000
	// registryRetryInterval rate-limits on-demand registry reloads triggered by
	// a placement on a tier this replica does not know yet.
	registryRetryInterval = 5 * time.Second
)

// placementDB is the read side the tiered store needs.
type placementDB interface {
	GetBlobPlacement(ctx context.Context, sha256 string) (backendID string, size int64, found bool, err error)
	WithBlobLock(ctx context.Context, sha256 string, fn func(ctx context.Context) error) error
}

// Store is a storage.BlobStore over every tier. Reads resolve the tier holding
// a blob transparently; writes go to the tier the blob is already placed on (the
// primary store for new blobs); deletes remove every copy. With only the
// primary tier configured it forwards straight to the primary store.
//
// It also forwards the optional presign and staging capabilities of the primary
// store (client-direct uploads always land on the primary tier).
type Store struct {
	reg     *Registry
	db      placementDB
	tracker *AccessTracker
	log     *logger.Logger
	metrics *metrics.Registry

	cacheMu sync.Mutex
	cache   map[string]placementEntry

	reloadMu   sync.Mutex
	lastReload time.Time
}

type placementEntry struct {
	backend string
	at      time.Time
}

var (
	_ storage.BlobStore    = (*Store)(nil)
	_ storage.Presigner    = (*Store)(nil)
	_ storage.StagingStore = (*Store)(nil)
)

// NewStore creates the tiered store. tracker and reg may be nil.
func NewStore(reg *Registry, db placementDB, tracker *AccessTracker, m *metrics.Registry, log *logger.Logger) *Store {
	return &Store{reg: reg, db: db, tracker: tracker, metrics: m, log: log, cache: map[string]placementEntry{}}
}

func (s *Store) cacheGet(sha string) string {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	e, ok := s.cache[sha]
	if !ok || time.Since(e.at) > placementCacheTTL {
		delete(s.cache, sha)

		return ""
	}

	return e.backend
}

func (s *Store) cachePut(sha, backend string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if backend == "" {
		delete(s.cache, sha)

		return
	}

	if len(s.cache) >= placementCacheMax {
		clear(s.cache)
	}

	s.cache[sha] = placementEntry{backend: backend, at: time.Now()}
}

// forget drops a cached placement (the mover calls it after a flip).
func (s *Store) forget(sha string) {
	s.cacheMu.Lock()
	delete(s.cache, sha)
	s.cacheMu.Unlock()
}

// resolveTier finds the tier for a backend id, reloading the registry once (rate
// limited) when the id is unknown to this replica.
func (s *Store) resolveTier(ctx context.Context, snap *Snapshot, backend string) (*Tier, *Snapshot) {
	if t, ok := snap.ByBackend(backend); ok {
		return t, snap
	}

	s.reloadMu.Lock()
	if time.Since(s.lastReload) > registryRetryInterval {
		s.lastReload = time.Now()
		s.reloadMu.Unlock()

		if err := s.reg.Refresh(ctx); err != nil && s.log != nil {
			s.log.Warnw("storage tier reload failed", "error", err)
		}
	} else {
		s.reloadMu.Unlock()
	}

	snap = s.reg.Snapshot()
	t, _ := snap.ByBackend(backend)

	return t, snap
}

func (s *Store) countRead(t *Tier) {
	if s.metrics != nil {
		s.metrics.TierReadsTotal.WithLabelValues(t.Name).Inc()
	}
}

// sweepTiers runs fn against every usable tier not yet tried. ok reports a
// success (and caches the placement); otherwise err is the first failure that
// was not "not found", or nil when every tier said not found.
func sweepTiers[T any](s *Store, id string, snap *Snapshot, tried map[string]bool, fn func(st storage.BlobStore) (T, error)) (T, *Tier, bool, error) {
	var (
		zero   T
		failed error
	)

	for _, t := range snap.Tiers {
		if tried[t.BackendID] || t.Store == nil {
			continue
		}

		got, err := fn(t.Store)
		if err == nil {
			s.cachePut(id, t.BackendID)

			return got, t, true, nil
		}

		if failed == nil && !errors.Is(err, storage.ErrBlobNotFound) {
			failed = fmt.Errorf("tier %s: %w", t.Name, err)
		}
	}

	return zero, nil, false, failed
}

func (s *Store) touch(ctx context.Context, sha string) {
	// Access times only feed idle-based policies and promote-on-read, which
	// need a cold tier to act on; without one reads stay free of bookkeeping.
	if s.tracker != nil && storage.AccessTracked(ctx) && s.reg.Snapshot().HasCold() {
		s.tracker.Touch(sha)
	}
}

// onTiers runs fn against the tier that holds id and returns its result. It
// tries, in order: the cached (or primary) tier, the authoritative placement
// from the database, then every other tier, moving on only for
// storage.ErrBlobNotFound. The tier that answered is returned.
//
// "Not found" is only reported when every tier that could be asked said so: a
// placement on a tier this server cannot open is ErrTierUnavailable, and a
// database or backend failure met along the way is returned instead of being
// masked as a missing blob (which clients would take as a 404).
func onTiers[T any](ctx context.Context, s *Store, id string, fn func(st storage.BlobStore) (T, error)) (T, *Tier, error) {
	var zero T

	snap := s.reg.Snapshot()
	primary := snap.Primary()

	if !snap.HasCold() {
		v, err := fn(primary.Store)

		return v, primary, err
	}

	first, _ := snap.ByBackend(s.cacheGet(id))
	if first == nil || first.Store == nil {
		first = primary
	}

	v, err := fn(first.Store)
	if err == nil || !errors.Is(err, storage.ErrBlobNotFound) {
		return v, first, err
	}

	notFound := err
	tried := map[string]bool{first.BackendID: true}

	var firstErr error // the first failure that is not "not found"

	backend, _, found, perr := s.db.GetBlobPlacement(ctx, id)
	if perr != nil {
		firstErr = fmt.Errorf("read blob placement: %w", perr)
	}

	if perr == nil && found && !tried[backend] {
		t, cur := s.resolveTier(ctx, snap, backend)
		snap = cur

		if t == nil || t.Store == nil {
			return zero, first, fmt.Errorf("%w: blob %s is placed on a tier this server cannot open", ErrTierUnavailable, id)
		}

		tried[backend] = true

		if v, err = fn(t.Store); err == nil {
			s.cachePut(id, backend)

			return v, t, nil
		}

		if !errors.Is(err, storage.ErrBlobNotFound) {
			return zero, t, err
		}
	}

	// The placement may have just flipped (or the database was unreachable):
	// the bytes can still be on any tier.
	v, t, ok, sweepErr := sweepTiers(s, id, snap, tried, fn)
	if ok {
		return v, t, nil
	}

	if firstErr == nil {
		firstErr = sweepErr
	}

	if firstErr != nil {
		return zero, first, firstErr
	}

	return zero, first, notFound
}

// Get implements storage.BlobStore.
func (s *Store) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	rc, t, err := onTiers(ctx, s, id, func(st storage.BlobStore) (io.ReadCloser, error) { return st.Get(ctx, id) })
	if err != nil {
		return nil, err
	}

	s.countRead(t)
	s.touch(ctx, id)

	return rc, nil
}

// GetSize implements storage.BlobStore.
func (s *Store) GetSize(ctx context.Context, id string) (int64, error) {
	n, _, err := onTiers(ctx, s, id, func(st storage.BlobStore) (int64, error) { return st.GetSize(ctx, id) })

	return n, err
}

// GetMetadata implements storage.BlobStore.
func (s *Store) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	m, _, err := onTiers(ctx, s, id, func(st storage.BlobStore) (map[string]string, error) { return st.GetMetadata(ctx, id) })

	return m, err
}

// Exists implements storage.BlobStore: a blob exists when any tier holds it.
func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	ok, _, err := onTiers(ctx, s, id, func(st storage.BlobStore) (bool, error) {
		found, err := st.Exists(ctx, id)
		if err == nil && !found {
			return false, storage.ErrBlobNotFound
		}

		return found, err
	})
	if errors.Is(err, storage.ErrBlobNotFound) {
		return false, nil
	}

	return ok, err
}

// ExistsBySHA256 implements storage.BlobStore.
func (s *Store) ExistsBySHA256(ctx context.Context, sha string) (string, bool, error) {
	ok, err := s.Exists(ctx, sha)
	if err != nil || !ok {
		return "", false, err
	}

	return sha, true, nil
}

// PresignURL implements storage.BlobStore: the URL is signed by the tier that
// holds the blob; tiers that cannot presign answer ErrPresignNotSupported.
func (s *Store) PresignURL(ctx context.Context, sha string, ttl time.Duration) (string, error) {
	u, t, err := onTiers(ctx, s, sha, func(st storage.BlobStore) (string, error) {
		url, err := st.PresignURL(ctx, sha, ttl)
		if err != nil {
			return "", err
		}

		return url, s.requireExists(ctx, st, sha)
	})
	if err == nil {
		s.countRead(t)
		s.touch(ctx, sha)
	}

	return u, err
}

// PresignGet implements storage.Presigner.
func (s *Store) PresignGet(ctx context.Context, sha string, ttl time.Duration, opts storage.PresignGetOptions) (string, error) {
	u, t, err := onTiers(ctx, s, sha, func(st storage.BlobStore) (string, error) {
		p, ok := st.(storage.Presigner)
		if !ok {
			return "", storage.ErrPresignNotSupported
		}

		url, err := p.PresignGet(ctx, sha, ttl, opts)
		if err != nil {
			return "", err
		}

		return url, s.requireExists(ctx, st, sha)
	})
	if err == nil {
		s.countRead(t)
		s.touch(ctx, sha)
	}

	return u, err
}

// requireExists turns "signed a URL for an object this tier does not hold" into
// ErrBlobNotFound, so onTiers keeps looking: signing alone proves nothing.
func (s *Store) requireExists(ctx context.Context, st storage.BlobStore, sha string) error {
	if !s.reg.Snapshot().HasCold() {
		return nil
	}

	ok, err := st.Exists(ctx, sha)
	if err != nil {
		return fmt.Errorf("check blob before signing: %w", err)
	}

	if !ok {
		return storage.ErrBlobNotFound
	}

	return nil
}

// Put implements storage.BlobStore. A blob that already has a row is written
// to the tier it is placed on (an existing copy is left untouched by every
// backend), so a re-upload never creates a second copy; anything else lands on
// the primary tier. Uploaders hold the per-blob lock around Put, which the
// mover's flip also takes, so the placement read here cannot change under them;
// an upload that does not take the lock can leave a stray copy behind a flip,
// which is harmless (reads find it, Delete removes it) and only costs space.
func (s *Store) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	snap := s.reg.Snapshot()
	primary := snap.Primary()

	if !snap.HasCold() {
		return primary.Store.Put(ctx, data, metadata)
	}

	target := primary

	if sha := metadata["sha256"]; storage.ValidateBlobID(sha) == nil {
		if backend, _, found, err := s.db.GetBlobPlacement(ctx, sha); err == nil && found && backend != "" {
			t, _ := s.resolveTier(ctx, snap, backend)

			if t == nil || t.Store == nil {
				return "", fmt.Errorf("%w: blob %s is placed on a tier this server cannot open", ErrTierUnavailable, sha)
			}

			target = t
		}
	}

	return target.Store.Put(ctx, data, metadata) //nolint:wrapcheck // backend errors (digest mismatch, ...) are matched by callers
}

// Delete implements storage.BlobStore: every tier's copy is removed. A failure
// on the primary store or on the tier the blob is placed on is returned (the
// caller keeps its row and retries); a failure on any other tier is logged only,
// since an unreachable tier must not make unrelated deletes fail.
func (s *Store) Delete(ctx context.Context, id string) error {
	snap := s.reg.Snapshot()
	primary := snap.Primary()

	if !snap.HasCold() {
		return primary.Store.Delete(ctx, id)
	}

	placed, _, found, perr := s.db.GetBlobPlacement(ctx, id)
	s.forget(id)

	if perr == nil && found && placed != "" {
		// A tier created on another replica may not be in this snapshot yet.
		// Deleting without touching it would orphan the bytes (the caller
		// removes the row on success), so resolve it or keep the row.
		t, cur := s.resolveTier(ctx, snap, placed)
		if t == nil {
			return fmt.Errorf("%w: blob %s is placed on a tier this server does not know", ErrTierUnavailable, id)
		}

		snap = cur
	}

	var errs []error

	for _, t := range snap.Tiers {
		if t.Store == nil {
			if found && t.BackendID == placed {
				errs = append(errs, fmt.Errorf("%w: %s", ErrTierUnavailable, t.Name))
			}

			continue
		}

		err := t.Store.Delete(ctx, id)
		if err == nil || errors.Is(err, storage.ErrBlobNotFound) {
			continue
		}

		if t.Primary() || perr != nil || (found && t.BackendID == placed) {
			errs = append(errs, fmt.Errorf("delete from tier %s: %w", t.Name, err))

			continue
		}

		if s.log != nil {
			s.log.Warnw("could not delete blob copy from tier", "blob", id, "tier", t.Name, "error", err)
		}
	}

	return errors.Join(errs...)
}

// DeleteCopy removes the copy of id held by the tier on backendID ("" = the
// primary store) and no other. The blob deletion sweeper uses it to finish
// retiring the old copy of a blob a flip moved: the other tier's copy is the
// authoritative one. A copy that is already gone is storage.ErrBlobNotFound.
func (s *Store) DeleteCopy(ctx context.Context, backendID, id string) error {
	snap := s.reg.Snapshot()

	t, _ := s.resolveTier(ctx, snap, backendID)
	if t == nil || t.Store == nil {
		return fmt.Errorf("%w: backend %q", ErrTierUnavailable, backendID)
	}

	s.forget(id)

	return t.Store.Delete(ctx, id) //nolint:wrapcheck // ErrBlobNotFound is matched by the caller
}

// List implements storage.BlobStore: it walks the tiers in order, primary first.
// A blob mid-move can appear once per tier. Tiers that cannot list are skipped,
// except the primary, whose ErrListNotSupported is returned. Backups do not use
// it: they enumerate the blobs table (see internal/backup).
func (s *Store) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	snap := s.reg.Snapshot()
	if !snap.HasCold() {
		return snap.Primary().Store.List(ctx, token, limit)
	}

	idx, inner := 0, ""

	if token != "" {
		i, rest, ok := strings.Cut(token, ":")

		n, err := strconv.Atoi(i)
		if !ok || err != nil || n < 0 {
			return nil, "", fmt.Errorf("%w: bad list token", storage.ErrListNotSupported)
		}

		idx, inner = n, rest
	}

	for ; idx < len(snap.Tiers); idx, inner = idx+1, "" {
		t := snap.Tiers[idx]
		if t.Store == nil {
			continue
		}

		ids, next, err := t.Store.List(ctx, inner, limit)
		if errors.Is(err, storage.ErrListNotSupported) && !t.Primary() {
			continue
		}

		if err != nil {
			return nil, "", err //nolint:wrapcheck // sentinel errors (ErrListNotSupported) are matched by callers
		}

		switch {
		case next != "":
			return ids, strconv.Itoa(idx) + ":" + next, nil
		case idx+1 < len(snap.Tiers):
			return ids, strconv.Itoa(idx+1) + ":", nil
		default:
			return ids, "", nil
		}
	}

	return nil, "", nil
}

// primaryStaging returns the primary store's staging capability.
func (s *Store) primaryStaging() (storage.StagingStore, bool) {
	st, ok := s.reg.Primary().(storage.StagingStore)

	return st, ok
}

// StagingSupported implements storage.StagingStore.
func (s *Store) StagingSupported() bool {
	st, ok := s.primaryStaging()

	return ok && st.StagingSupported()
}

// PresignStagingPut implements storage.StagingStore.
func (s *Store) PresignStagingPut(ctx context.Context, id string, size int64, ttl time.Duration) (storage.PresignedPut, error) {
	st, ok := s.primaryStaging()
	if !ok {
		return storage.PresignedPut{}, storage.ErrPresignNotSupported
	}

	return st.PresignStagingPut(ctx, id, size, ttl)
}

// StatStaging implements storage.StagingStore.
func (s *Store) StatStaging(ctx context.Context, id string) (storage.StagedObject, error) {
	st, ok := s.primaryStaging()
	if !ok {
		return storage.StagedObject{}, storage.ErrPresignNotSupported
	}

	return st.StatStaging(ctx, id)
}

// OpenStaging implements storage.StagingStore.
func (s *Store) OpenStaging(ctx context.Context, id, version string) (io.ReadCloser, error) {
	st, ok := s.primaryStaging()
	if !ok {
		return nil, storage.ErrPresignNotSupported
	}

	return st.OpenStaging(ctx, id, version)
}

// PromoteStaging implements storage.StagingStore. The verified object lands on
// the primary tier. A blob that was already placed on another tier keeps its
// placement; the extra primary copy is a stray that reads tolerate and Delete
// removes (see docs/operations/storage-tiering.md).
func (s *Store) PromoteStaging(ctx context.Context, id, sha, version string, metadata map[string]string) error {
	st, ok := s.primaryStaging()
	if !ok {
		return storage.ErrPresignNotSupported
	}

	return st.PromoteStaging(ctx, id, sha, version, metadata)
}

// DeleteStaging implements storage.StagingStore.
func (s *Store) DeleteStaging(ctx context.Context, id string) error {
	st, ok := s.primaryStaging()
	if !ok {
		return storage.ErrPresignNotSupported
	}

	return st.DeleteStaging(ctx, id)
}

// WalkStaging implements storage.StagingStore.
func (s *Store) WalkStaging(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	st, ok := s.primaryStaging()
	if !ok {
		return storage.ErrPresignNotSupported
	}

	return st.WalkStaging(ctx, olderThan, fn)
}
