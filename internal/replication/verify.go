// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/database"
)

const (
	// verifyWindow is how many blobs are examined between two progress saves.
	verifyWindow = 500

	// verifyBudget is how long one tick works before it saves its place and
	// lets go of the cluster-wide lock.
	verifyBudget = 30 * time.Second
)

// VerifyResult is the outcome (or, while running, the progress) of a verify
// pass. It is stored as JSON in blob_replication_state.
type VerifyResult struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Deep is true when the bytes of both copies were hashed.
	Deep bool `json:"deep"`
	// Listing is "list" (the primary store was enumerated) or "database" (it
	// cannot list, so the blobs table was walked).
	Listing string `json:"listing,omitempty"`
	// ReplicaListed is false when the replica cannot enumerate its objects, so
	// blobs only it holds are not found.
	ReplicaListed bool `json:"replica_listed"`

	// Checked is the number of blobs examined.
	Checked int64 `json:"checked"`
	// MissingReplica blobs were not on the replica (a copy is queued).
	MissingReplica int64 `json:"missing_replica"`
	// MissingPrimary blobs were not on the primary store (restored from the replica).
	MissingPrimary int64 `json:"missing_primary"`
	// Mismatched blobs had different sizes (or, deep, contents) on the sides.
	Mismatched int64 `json:"mismatched"`
	// Repaired blobs were fixed or queued for copying.
	Repaired int64 `json:"repaired"`
	// Unreferenced blobs exist only on the replica and nothing needs them:
	// reported, never restored.
	Unreferenced int64 `json:"unreferenced"`
	// Lost blobs are referenced but exist on neither side.
	Lost int64 `json:"lost"`
	// Errors blobs could not be checked or repaired.
	Errors int64 `json:"errors"`
}

// counters are the shared tallies of one window.
type counters struct {
	checked, missingReplica, missingPrimary, mismatched, repairedReplica, repairedPrimary, unreferenced, lost, errs atomic.Int64
}

// Verifier compares the primary store with the replica and repairs the
// differences: a blob missing from the replica is queued for copying, one
// missing from the primary (and still referenced) is restored from the replica,
// one with different bytes is rewritten from the side that hashes right. A
// pass walks both listings in id order and keeps its place in the database, so
// it survives restarts and runs on one replica at a time.
type Verifier struct {
	*core

	sqlDB *sql.DB

	budget time.Duration
	window int

	// walk is the in-memory walk, valid while its cursor is the persisted one.
	walk *walker
}

// startRun parameters.
func (v *Verifier) newRun() VerifyResult {
	return VerifyResult{StartedAt: v.now().UTC(), Deep: v.opts.VerifyDeep}
}

// RequestRun asks for a verify pass; false when one is running or requested.
func (v *Verifier) RequestRun(ctx context.Context) (bool, error) {
	return v.queue.RequestReplicationVerify(ctx) //nolint:wrapcheck // wrapped by the database layer
}

// due reports whether the schedule calls for a pass: replication was never
// verified (this also backfills blobs that existed before replication was
// enabled) or the last pass finished verifyInterval ago.
func (v *Verifier) due(st database.BlobReplicationState) bool {
	if v.opts.VerifyInterval <= 0 {
		return false
	}

	last, ok := decodeResult(st.LastVerify)
	if !ok || last.FinishedAt == nil {
		return true
	}

	return v.now().Sub(*last.FinishedAt) >= v.opts.VerifyInterval
}

func decodeResult(raw json.RawMessage) (VerifyResult, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return VerifyResult{}, false
	}

	var r VerifyResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return VerifyResult{}, false
	}

	return r, true
}

// Tick does one slice of verification work: it takes the cluster-wide lock,
// starts a pass when one was requested or is due, and works on the running one
// until it finishes or the time budget is spent. Without the lock (another
// replica is verifying) it does nothing.
func (v *Verifier) Tick(ctx context.Context) error {
	if v.sqlDB != nil {
		release, acquired, err := tryAdvisoryLock(ctx, v.sqlDB, verifyLockKey)
		if err != nil {
			return err
		}

		if !acquired {
			return nil
		}

		defer release()
	}

	st, err := v.queue.GetBlobReplicationState(ctx)
	if err != nil {
		return fmt.Errorf("read verify state: %w", err)
	}

	run, ok := decodeResult(st.VerifyRun)
	cursor := st.VerifyCursor

	if !ok {
		if !st.VerifyRequested && !v.due(st) {
			return nil
		}

		run = v.newRun()

		raw, merr := json.Marshal(run)
		if merr != nil {
			return fmt.Errorf("encode verify run: %w", merr)
		}

		if started, serr := v.queue.StartReplicationVerify(ctx, raw); serr != nil || !started {
			return serr //nolint:wrapcheck // wrapped by the database layer
		}

		cursor, v.walk = "", nil
	}

	return v.work(ctx, run, cursor)
}

// work processes windows of blobs until the pass ends or the budget is spent.
func (v *Verifier) work(ctx context.Context, run VerifyResult, cursor string) error {
	deadline := v.now().Add(v.budget)

	for ctx.Err() == nil {
		if v.walk == nil || v.walk.cursor != cursor {
			w, err := v.newWalker(ctx, cursor)
			if err != nil {
				v.walk = nil

				return fmt.Errorf("list blobs for verification: %w", err)
			}

			v.walk = w
		}

		run.Listing, run.ReplicaListed = v.walk.mode, v.walk.r != nil

		items, done, err := v.walk.next(ctx, v.window)
		if err != nil {
			v.walk = nil

			return fmt.Errorf("list blobs for verification: %w", err)
		}

		v.checkAll(ctx, items, &run)

		if len(items) > 0 {
			cursor = items[len(items)-1].id
		}

		if done {
			return v.finish(ctx, run)
		}

		if err := v.save(ctx, cursor, run); err != nil {
			return err
		}

		if !v.now().Before(deadline) {
			return nil
		}
	}

	return ctx.Err() //nolint:wrapcheck // cancellation
}

func (v *Verifier) save(ctx context.Context, cursor string, run VerifyResult) error {
	raw, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode verify progress: %w", err)
	}

	if ok, err := v.queue.SaveReplicationVerifyProgress(ctx, cursor, raw); err != nil || !ok {
		v.walk = nil

		if err == nil {
			err = errors.New("verify pass no longer running") //nolint:err113 // internal signal
		}

		return err //nolint:wrapcheck // wrapped by the database layer
	}

	return nil
}

func (v *Verifier) finish(ctx context.Context, run VerifyResult) error {
	now := v.now().UTC()
	run.FinishedAt = &now
	v.walk = nil

	raw, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode verify result: %w", err)
	}

	if err := v.queue.FinishReplicationVerify(ctx, raw); err != nil {
		return err //nolint:wrapcheck // wrapped by the database layer
	}

	v.m.LastVerify.Set(float64(now.Unix()))
	v.log.Infow("replication verify finished", "checked", run.Checked, "missing_replica", run.MissingReplica,
		"missing_primary", run.MissingPrimary, "mismatched", run.Mismatched, "repaired", run.Repaired,
		"unreferenced", run.Unreferenced, "lost", run.Lost, "errors", run.Errors)

	return nil
}

// checkAll examines a window of blobs with the configured concurrency and adds
// the tallies to run and to the metrics.
func (v *Verifier) checkAll(ctx context.Context, items []item, run *VerifyResult) {
	var (
		c    counters
		next atomic.Int64
		wg   sync.WaitGroup
	)

	for range min(v.opts.Concurrency, len(items)) {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= len(items) || ctx.Err() != nil {
					return
				}

				v.checkSafe(ctx, items[i], &c)
			}
		})
	}

	wg.Wait()

	run.Checked += c.checked.Load()
	run.MissingReplica += c.missingReplica.Load()
	run.MissingPrimary += c.missingPrimary.Load()
	run.Mismatched += c.mismatched.Load()
	run.Repaired += c.repairedReplica.Load() + c.repairedPrimary.Load()
	run.Unreferenced += c.unreferenced.Load()
	run.Lost += c.lost.Load()
	run.Errors += c.errs.Load()

	v.m.VerifyChecked.Add(float64(c.checked.Load()))
	v.m.VerifyMissing.WithLabelValues("replica").Add(float64(c.missingReplica.Load()))
	v.m.VerifyMissing.WithLabelValues("primary").Add(float64(c.missingPrimary.Load()))
	v.m.VerifyMismatched.Add(float64(c.mismatched.Load()))
	v.m.VerifyRepaired.WithLabelValues("replica").Add(float64(c.repairedReplica.Load()))
	v.m.VerifyRepaired.WithLabelValues("primary").Add(float64(c.repairedPrimary.Load()))
}

func (v *Verifier) checkSafe(ctx context.Context, it item, c *counters) {
	defer func() {
		if p := recover(); p != nil {
			c.errs.Add(1)
			v.log.Errorw("replication verify: check panicked", "sha256", it.id, "panic", p)
		}
	}()

	v.check(ctx, it, c)
}

// check examines one blob. The listings only say which blobs to look at: the
// facts come from asking each side directly, because a listing can be stale.
func (v *Verifier) check(ctx context.Context, it item, c *counters) {
	c.checked.Add(1)

	psize, perr := v.inner.GetSize(ctx, it.id)
	rsize, rerr := v.replica.GetSize(ctx, it.id)

	if (perr != nil && !isNotFound(perr)) || (rerr != nil && !isNotFound(rerr)) {
		c.errs.Add(1)
		v.log.Warnw("replication verify: could not stat blob", "sha256", it.id, "primary_error", perr, "replica_error", rerr)

		return
	}

	pHas, rHas := perr == nil, rerr == nil

	switch {
	case pHas && rHas:
		v.checkBoth(ctx, it.id, psize != rsize, c)
	case pHas:
		v.missingOnReplica(ctx, it.id, c)
	case rHas:
		v.missingOnPrimary(ctx, it.id, c)
	case it.fromDB:
		v.neither(ctx, it.id, c)
	}
}

// checkBoth handles a blob both sides have: a size difference, or with deep
// verification any difference in content, is resolved by hashing.
func (v *Verifier) checkBoth(ctx context.Context, id string, sizeDiffers bool, c *counters) {
	if !sizeDiffers && !v.opts.VerifyDeep {
		return
	}

	if !sizeDiffers {
		pOK, perr := hashMatches(ctx, v.inner, id)
		rOK, rerr := hashMatches(ctx, v.replica, id)

		if perr != nil || rerr != nil {
			c.errs.Add(1)
			v.log.Warnw("replication verify: could not read blob", "sha256", id, "primary_error", perr, "replica_error", rerr)

			return
		}

		if pOK && rOK {
			return
		}
	}

	c.mismatched.Add(1)

	if _, err := v.fixMismatch(ctx, id); err != nil {
		c.errs.Add(1)
		v.log.Errorw("replication verify: blob differs between primary and replica and could not be repaired", "sha256", id, "error", err)

		return
	}

	c.repairedReplica.Add(1)
}

// missingOnReplica queues the copy unless one (or a deletion, which wins) is
// already queued: a blob with a queued copy is lag, not damage.
func (v *Verifier) missingOnReplica(ctx context.Context, id string, c *counters) {
	op, err := v.pending(ctx, id)
	if err != nil {
		c.errs.Add(1)
		v.log.Warnw("replication verify: could not read the queue", "sha256", id, "error", err)

		return
	}

	if op != nil {
		return
	}

	c.missingReplica.Add(1)

	if err := v.enqueue(ctx, id, database.BlobReplicationCopy); err != nil {
		c.errs.Add(1)
		v.log.Warnw("replication verify: could not queue a copy", "sha256", id, "error", err)

		return
	}

	c.repairedReplica.Add(1)
}

// missingOnPrimary restores a blob the replica has and the primary lost, when
// the database still needs it and no deletion is queued.
func (v *Verifier) missingOnPrimary(ctx context.Context, id string, c *counters) {
	if del, err := v.hasPendingDelete(ctx, id); err != nil || del {
		if err != nil {
			c.errs.Add(1)
		}

		return
	}

	referenced, err := v.queue.BlobReplicationReferenced(ctx, id)
	if err != nil {
		c.errs.Add(1)
		v.log.Warnw("replication verify: could not check references", "sha256", id, "error", err)

		return
	}

	if !referenced {
		c.unreferenced.Add(1)

		return
	}

	c.missingPrimary.Add(1)

	restored, err := v.restorePrimary(ctx, id, false)

	switch {
	case err != nil && errors.Is(err, errPendingDelete):
	case err != nil:
		c.errs.Add(1)
		v.log.Errorw("replication verify: could not restore a blob on the primary store", "sha256", id, "error", err)
	case restored:
		c.repairedPrimary.Add(1)
	}
}

// neither handles a blob the database lists that neither side has.
func (v *Verifier) neither(ctx context.Context, id string, c *counters) {
	referenced, err := v.queue.BlobReplicationReferenced(ctx, id)
	if err != nil {
		c.errs.Add(1)

		return
	}

	if referenced {
		c.lost.Add(1)
		v.log.Errorw("replication verify: a referenced blob is on neither the primary store nor the replica", "sha256", id)
	}
}
