// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Outcome is the result of moving one blob.
type Outcome int

// Move outcomes.
const (
	// OutcomeMoved: the blob's placement now is the target and the old copy is gone.
	OutcomeMoved Outcome = iota
	// OutcomeSkipped: nothing to do (already on the target, or another move owns the blob).
	OutcomeSkipped
	// OutcomeGone: the blob was deleted meanwhile.
	OutcomeGone
)

// Move steps, in order. They name the points a crash is injected at in tests
// and appear in logs.
const (
	stepClaimed  = "claimed"  // journal row written, nothing copied
	stepCopied   = "copied"   // bytes written to the target tier
	stepVerified = "verified" // target copy checked, placement unchanged
	stepFlipped  = "flipped"  // placement committed on the target tier
	stepCleaned  = "cleaned"  // old copy deleted, journal row still present
)

// errInjectedCrash is returned by the test fault hook to abandon a move without
// any failure bookkeeping, exactly as a killed process would.
var errInjectedCrash = errors.New("injected crash")

var (
	errCopyChecksum = errors.New("copied blob does not match its sha256")
	errCopySize     = errors.New("copied blob size differs from the recorded size")
	errSourceGone   = errors.New("source copy is missing")
	errSameStorage  = errors.New("source and target tiers share their storage")
)

// MoverConfig tunes the mover.
type MoverConfig struct {
	// VerifyReadback re-reads the target copy and compares its sha256 before the
	// flip, on top of the size check. Disable it only for backends that already
	// verify digests on write and when read bandwidth to the target is scarce.
	VerifyReadback bool
}

// Mover relocates blobs between tiers with the crash-safe protocol described
// in docs/operations/storage-tiering.md. It is safe to call from several
// goroutines for different blobs; two movers for the same blob are serialized
// by the journal's primary key.
type Mover struct {
	db       DB
	reg      *Registry
	store    *Store // optional: its placement cache is invalidated after a flip
	throttle *Throttle
	cfg      MoverConfig
	log      *logger.Logger
	metrics  *metrics.Registry

	// fault, when set, is consulted after each step (tests only).
	fault func(step string) error
}

// NewMover creates a mover. store, throttle, m and log may be nil.
func NewMover(db DB, reg *Registry, store *Store, throttle *Throttle, cfg MoverConfig, m *metrics.Registry, log *logger.Logger) *Mover {
	return &Mover{db: db, reg: reg, store: store, throttle: throttle, cfg: cfg, metrics: m, log: log}
}

func (m *Mover) hit(step string) error {
	if m.fault == nil {
		return nil
	}

	return m.fault(step)
}

func (m *Mover) tierName(backend string) string {
	if t, ok := m.reg.Snapshot().ByBackend(backend); ok {
		return t.Name
	}

	return "unknown"
}

func (m *Mover) record(from, to string, out Outcome, err error, size int64) {
	if m.metrics == nil {
		return
	}

	f, t := m.tierName(from), m.tierName(to)

	switch {
	case err != nil:
		m.metrics.TierMovesTotal.WithLabelValues(f, t, "failed").Inc()
	case out == OutcomeMoved:
		m.metrics.TierMovesTotal.WithLabelValues(f, t, "moved").Inc()
		m.metrics.TierMovedBytesTotal.WithLabelValues(f, t).Add(float64(size))
	default:
		m.metrics.TierMovesTotal.WithLabelValues(f, t, "skipped").Inc()
	}
}

// Move relocates one blob to the tier on backend `to` ("" = primary store).
func (m *Mover) Move(ctx context.Context, sha, to, reason string) (Outcome, error) {
	from, size, found, err := m.db.GetBlobPlacement(ctx, sha)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("read placement: %w", err)
	}

	if !found {
		return OutcomeGone, nil
	}

	if from == to {
		return OutcomeSkipped, nil
	}

	// Nothing may be journaled for a move that cannot run: an unopenable tier
	// would otherwise leave one failing journal row per blob and burn the
	// recovery budget.
	if _, _, err := m.tiers(from, to); err != nil {
		return OutcomeSkipped, err
	}

	row, created, err := m.db.BeginStorageTierMove(ctx, &models.StorageTierMove{
		SHA256: sha, FromBackendID: from, ToBackendID: to, Reason: reason, SizeBytes: size,
	})
	if err != nil {
		return OutcomeSkipped, err //nolint:wrapcheck // already wrapped by the database layer
	}

	if !created && (row.FromBackendID != from || row.ToBackendID != to) {
		// A journal row for a different move owns the blob: recovery finishes it first.
		return OutcomeSkipped, nil
	}

	return m.Resume(ctx, row)
}

// Resume drives a journaled move to completion from whatever state it was left
// in. It is the single execution path: a fresh move and a post-crash recovery
// run the same code, so recovery needs no special cases.
func (m *Mover) Resume(ctx context.Context, row *models.StorageTierMove) (Outcome, error) {
	out, err := m.run(ctx, row)
	if err != nil && !errors.Is(err, errInjectedCrash) && ctx.Err() == nil {
		if ferr := m.db.FailStorageTierMove(context.WithoutCancel(ctx), row.SHA256, err.Error()); ferr != nil && m.log != nil {
			m.log.Warnw("could not record tier move failure", "blob", row.SHA256, "error", ferr)
		}
	}

	m.record(row.FromBackendID, row.ToBackendID, out, err, row.SizeBytes)

	return out, err
}

func (m *Mover) run(ctx context.Context, row *models.StorageTierMove) (Outcome, error) {
	sha := row.SHA256

	if err := m.hit(stepClaimed); err != nil {
		return OutcomeSkipped, err
	}

	if row.State == models.TierMoveCopying {
		out, done, err := m.copyAndFlip(ctx, row)
		if err != nil || done {
			return out, err
		}
	}

	// The placement is on the target tier now; only the old copy remains.
	if err := m.removeOldCopy(ctx, row); err != nil {
		return OutcomeSkipped, err
	}

	if err := m.hit(stepCleaned); err != nil {
		return OutcomeSkipped, err
	}

	if err := m.db.FinishStorageTierMove(ctx, sha); err != nil {
		return OutcomeSkipped, fmt.Errorf("finish move: %w", err)
	}

	return OutcomeMoved, nil
}

// copyAndFlip runs the copying phase: copy, verify, flip. done is true when the
// move ended without a flip (blob deleted, or its placement changed under us).
func (m *Mover) copyAndFlip(ctx context.Context, row *models.StorageTierMove) (Outcome, bool, error) {
	sha, from, to := row.SHA256, row.FromBackendID, row.ToBackendID

	src, dst, err := m.tiers(from, to)
	if err != nil {
		return OutcomeSkipped, true, err
	}

	// A row left by a crashed attempt (or a retry) may find the work already done.
	switch cur, _, found, perr := m.db.GetBlobPlacement(ctx, sha); {
	case perr != nil:
		return OutcomeSkipped, true, fmt.Errorf("read placement: %w", perr)
	case !found:
		return m.abandon(ctx, row, dst, OutcomeGone)
	case cur == to:
		return OutcomeMoved, false, nil
	case cur != from:
		return m.abandon(ctx, row, dst, OutcomeSkipped)
	}

	if err := m.copyBlob(ctx, sha, row.SizeBytes, src.Store, dst.Store); err != nil {
		return OutcomeSkipped, true, err
	}

	if err := m.hit(stepCopied); err != nil {
		return OutcomeSkipped, true, err
	}

	if err := m.verifyCopy(ctx, sha, row.SizeBytes, dst); err != nil {
		return OutcomeSkipped, true, err
	}

	if err := m.hit(stepVerified); err != nil {
		return OutcomeSkipped, true, err
	}

	flip, err := m.db.FlipBlobPlacement(ctx, sha, from, to, func(ctx context.Context) error {
		return m.confirmCopy(ctx, sha, row.SizeBytes, dst.Store)
	})
	if err != nil {
		return OutcomeSkipped, true, err //nolint:wrapcheck // already wrapped by the database layer
	}

	switch flip {
	case models.FlipBlobGone:
		return m.abandon(ctx, row, dst, OutcomeGone)
	case models.FlipPlacementChanged:
		return m.abandon(ctx, row, dst, OutcomeSkipped)
	case models.FlipDone:
	}

	if m.store != nil {
		m.store.forget(sha)
	}

	if err := m.hit(stepFlipped); err != nil {
		return OutcomeSkipped, true, err
	}

	return OutcomeMoved, false, nil
}

func (m *Mover) tiers(from, to string) (*Tier, *Tier, error) {
	snap := m.reg.Snapshot()

	src, ok := snap.ByBackend(from)
	if !ok || src.Store == nil {
		return nil, nil, fmt.Errorf("%w: source backend %q", ErrTierUnavailable, from)
	}

	dst, ok := snap.ByBackend(to)
	if !ok || dst.Store == nil {
		return nil, nil, fmt.Errorf("%w: target backend %q", ErrTierUnavailable, to)
	}

	// The registry refuses aliased backends; this is the last line before a
	// "copy" of a blob onto itself is followed by deleting the source.
	if src.identity != "" && src.identity == dst.identity {
		return nil, nil, fmt.Errorf("%w: %w (%s)", ErrTierUnavailable, errSameStorage, src.identity)
	}

	return src, dst, nil
}

// copyBlob writes the source bytes to dst, hashing them on the way, unless dst
// already holds a copy of the right size (a resumed move).
func (m *Mover) copyBlob(ctx context.Context, sha string, size int64, src, dst storage.BlobStore) error {
	if ok, err := dst.Exists(ctx, sha); err == nil && ok {
		if n, err := dst.GetSize(ctx, sha); err == nil && n == size {
			return nil
		}
	}

	rc, err := src.Get(ctx, sha)
	if errors.Is(err, storage.ErrBlobNotFound) {
		return fmt.Errorf("%w: %s", errSourceGone, sha)
	}

	if err != nil {
		return fmt.Errorf("open source copy: %w", err)
	}

	defer func() { _ = rc.Close() }()

	h := sha256.New()
	tee := io.TeeReader(m.throttle.Reader(ctx, rc), h)

	if _, err := dst.Put(ctx, tee, map[string]string{"sha256": sha}); err != nil {
		return fmt.Errorf("write target copy: %w", err)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		// The source bytes are corrupt: whatever the target stored is not the blob.
		_ = dst.Delete(context.WithoutCancel(ctx), sha)

		return fmt.Errorf("%w: source read hashed to %s", errCopyChecksum, got)
	}

	return nil
}

// confirmCopy checks, cheaply, that the target holds a blob of the right size.
func (m *Mover) confirmCopy(ctx context.Context, sha string, size int64, dst storage.BlobStore) error {
	ok, err := dst.Exists(ctx, sha)
	if err != nil {
		return fmt.Errorf("check target copy: %w", err)
	}

	if !ok {
		return fmt.Errorf("%w: target copy of %s is missing", storage.ErrBlobNotFound, sha)
	}

	n, err := dst.GetSize(ctx, sha)
	if err != nil {
		return fmt.Errorf("check target copy size: %w", err)
	}

	if n != size {
		return fmt.Errorf("%w: have %d, want %d", errCopySize, n, size)
	}

	return nil
}

// verifyCopy proves the target copy is the blob before the placement may point
// at it: the right size and, when configured, the right sha256 read back from
// the target. A bad copy is removed so a retry writes it afresh.
func (m *Mover) verifyCopy(ctx context.Context, sha string, size int64, dst *Tier) error {
	err := m.confirmCopy(ctx, sha, size, dst.Store)

	if err == nil && m.cfg.VerifyReadback {
		err = m.readBackHash(ctx, sha, dst.Store)
	}

	if err != nil && (errors.Is(err, errCopySize) || errors.Is(err, errCopyChecksum)) {
		_ = dst.Store.Delete(context.WithoutCancel(ctx), sha)
	}

	return err
}

func (m *Mover) readBackHash(ctx context.Context, sha string, dst storage.BlobStore) error {
	rc, err := dst.Get(ctx, sha)
	if err != nil {
		return fmt.Errorf("read back target copy: %w", err)
	}

	defer func() { _ = rc.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, m.throttle.Reader(ctx, rc)); err != nil {
		return fmt.Errorf("read back target copy: %w", err)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return fmt.Errorf("%w: read back hashed to %s", errCopyChecksum, got)
	}

	return nil
}

// abandon ends a move that must not flip (the blob was deleted, or its
// placement is no longer the move's source): the target copy is removed unless
// the placement is the target, and the journal row is dropped.
func (m *Mover) abandon(ctx context.Context, row *models.StorageTierMove, dst *Tier, out Outcome) (Outcome, bool, error) {
	err := m.db.WithBlobLock(ctx, row.SHA256, func(ctx context.Context) error {
		cur, _, found, perr := m.db.GetBlobPlacement(ctx, row.SHA256)
		if perr != nil {
			return fmt.Errorf("read placement: %w", perr)
		}

		if found && cur == row.ToBackendID {
			return nil // the target copy is authoritative; keep it
		}

		if derr := dst.Store.Delete(ctx, row.SHA256); derr != nil && !errors.Is(derr, storage.ErrBlobNotFound) {
			return fmt.Errorf("remove unused target copy: %w", derr)
		}

		return nil
	})
	if err != nil {
		return OutcomeSkipped, true, err
	}

	if err := m.db.FinishStorageTierMove(ctx, row.SHA256); err != nil {
		return OutcomeSkipped, true, fmt.Errorf("finish move: %w", err)
	}

	return out, true, nil
}

// removeOldCopy deletes the source copy of a move whose flip committed. It runs
// under the per-blob lock and re-reads the placement: the old copy is removed
// only while the placement is still the move's target, so a blob that was moved
// again (or deleted) is never stripped of its authoritative copy. While the
// blob still exists it also re-confirms the target copy under the lock, so the
// source is never deleted unless the bytes it would leave behind are really
// there (a target that vanished, or turns out to be the source itself).
func (m *Mover) removeOldCopy(ctx context.Context, row *models.StorageTierMove) error {
	src, dst, err := m.tiers(row.FromBackendID, row.ToBackendID)
	if err != nil {
		return err
	}

	return m.db.WithBlobLock(ctx, row.SHA256, func(ctx context.Context) error {
		cur, _, found, perr := m.db.GetBlobPlacement(ctx, row.SHA256)
		if perr != nil {
			return fmt.Errorf("read placement: %w", perr)
		}

		if found && cur != row.ToBackendID {
			// Placement moved on again; the source may be authoritative. Leave it.
			return nil
		}

		if found {
			if cerr := m.confirmCopy(ctx, row.SHA256, row.SizeBytes, dst.Store); cerr != nil {
				return fmt.Errorf("keeping the old copy: %w", cerr)
			}
		}

		if derr := src.Store.Delete(ctx, row.SHA256); derr != nil && !errors.Is(derr, storage.ErrBlobNotFound) {
			return fmt.Errorf("delete old copy: %w", derr)
		}

		return nil
	})
}
