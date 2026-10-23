// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

const (
	// unitFailureLimit is how many pulls may fail on the same unit before it
	// is parked in the deferred queue so the stream moves on.
	unitFailureLimit = 3
	// deferredRetryBatch bounds the deferred units retried per pull.
	deferredRetryBatch = 50
	// baseBackoff is the first retry delay after a failed pull.
	baseBackoff = 5 * time.Second
	// stateWriteTimeout bounds the bookkeeping writes done while shutting down.
	stateWriteTimeout = 10 * time.Second
)

// Backoff is the delay before retry n (0-based) of a failing peer: doubling
// from the base up to max, with +-20% jitter. A peer is never given up on.
func Backoff(n int, poll, maxDelay time.Duration) time.Duration {
	base := min(baseBackoff, poll)
	if base <= 0 {
		base = time.Second
	}

	d := base

	for i := 0; i < n && d < maxDelay; i++ {
		d *= 2
	}

	d = min(d, maxDelay)

	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // jitter, not security
}

// tryLock takes a session advisory lock on a pinned connection; the release
// func (always non-nil) unlocks it, dropping the connection if that fails.
func tryLock(ctx context.Context, db *sql.DB, key string) (func(), bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return func() {}, false, fmt.Errorf("pin connection for advisory lock: %w", err)
	}

	var locked bool

	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&locked); err != nil {
		_ = conn.Close()

		return func() {}, false, fmt.Errorf("advisory lock %s: %w", key, err)
	}

	if !locked {
		_ = conn.Close()

		return func() {}, false, nil
	}

	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), stateWriteTimeout)
		defer cancel()

		if _, err := conn.ExecContext(uctx, "SELECT pg_advisory_unlock(hashtext($1))", key); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}

		_ = conn.Close()
	}, true, nil
}

// PullPeer runs one pull of a peer: it catches the peer's log up from the
// stored cursor, retries the peer's deferred units, and records the outcome.
// Only one replica pulls a peer at a time. A skipped (locked) pull is not an
// error.
func (e *Engine) PullPeer(ctx context.Context, peerID, repoID string) (err error) {
	release, locked, err := tryLock(ctx, e.SQL, "federation:peer:"+peerID)
	if err != nil {
		return err
	}

	if !locked {
		return nil
	}
	defer release()

	peer, err := e.Store.GetPeer(ctx, repoID, peerID)
	if err != nil {
		return err
	}

	if !peer.Enabled {
		return nil
	}

	repo, err := e.Store.RepoByID(ctx, peer.RepoID)
	if err != nil {
		return err
	}

	if err := e.Store.MarkAttempt(ctx, peer.ID); err != nil {
		return err
	}

	defer func() { e.recordOutcome(peer, repo, err) }()

	return e.pull(ctx, peer, repo)
}

// recordOutcome stores the result of a pull; it runs on a fresh context so a
// shutdown in the middle of a pull still leaves the state truthful.
func (e *Engine) recordOutcome(peer *Peer, repo *RepoInfo, pullErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), stateWriteTimeout)
	defer cancel()

	if pullErr == nil {
		if err := e.Store.MarkSuccess(ctx, peer.ID); err != nil {
			e.Log.Warnw("federation: record success", "repo", repo.Name, "peer", peer.Name, "error", err)
		}

		return
	}

	if errors.Is(pullErr, context.Canceled) {
		return
	}

	delay := Backoff(peer.ErrorCount, e.PollInterval, e.MaxBackoff)
	if err := e.Store.MarkFailure(ctx, peer.ID, pullErr.Error(), delay); err != nil {
		e.Log.Warnw("federation: record failure", "repo", repo.Name, "peer", peer.Name, "error", err)
	}

	if e.Metrics != nil {
		e.Metrics.PullErrors.WithLabelValues(repo.Name, peer.Name).Inc()
	}

	e.Log.Warnw("federation: pull failed", "repo", repo.Name, "peer", peer.Name, "retry_in", delay.String(), "error", pullErr)
}

func (e *Engine) newPullCtx(ctx context.Context, peer *Peer, repo *RepoInfo) (*pullCtx, error) {
	codec, err := CodecFor(repo.Format)
	if err != nil {
		return nil, err
	}

	secrets, err := peer.Secrets()
	if err != nil {
		return nil, err
	}

	client, err := NewClient(ctx, peer, secrets, e.RequestTimeout)
	if err != nil {
		return nil, err
	}

	return &pullCtx{repo: repo, peer: peer, client: client, codec: codec}, nil
}

var errPeerMismatch = errors.New("federation: peer repository does not match")

func (e *Engine) pull(ctx context.Context, peer *Peer, repo *RepoInfo) error {
	pc, err := e.newPullCtx(ctx, peer, repo)
	if err != nil {
		return err
	}

	info, err := pc.client.Info(ctx)
	if err != nil {
		return err
	}

	if info.Version != ProtocolVersion {
		return fmt.Errorf("%w: peer speaks protocol %d, this instance %d", errPeerMismatch, info.Version, ProtocolVersion)
	}

	if info.Instance == e.instance {
		return fmt.Errorf("%w: the peer is this instance", errPeerMismatch)
	}

	if info.Format != repo.Format {
		return fmt.Errorf("%w: peer repository is %s, local is %s", errPeerMismatch, info.Format, repo.Format)
	}

	prog := Progress{Cursor: peer.CursorSeq, RemoteHead: info.HeadSeq, RemoteLogID: info.LogID, OldestPending: peer.OldestPendingAt}

	if e.needsFullPass(peer, info) {
		e.Log.Infow("federation: full pass from the start of the peer log", "repo", repo.Name, "peer", peer.Name, "resync_requested", peer.ResyncRequested)

		prog.Cursor = 0
		prog.OldestPending = nil
		prog.ClearResync = true
		pc.repair = true

		if err := e.Store.SaveProgress(ctx, peer.ID, prog); err != nil {
			return err
		}

		prog.ClearResync = false
	}

	if info.HeadSeq > prog.Cursor && prog.OldestPending == nil {
		now := time.Now()
		prog.OldestPending = &now
	}

	if err := e.drain(ctx, pc, &prog); err != nil {
		return err
	}

	return e.retryDeferred(ctx, pc)
}

// needsFullPass reports whether the pull must start from the beginning of the
// peer's log: a resync was asked for, the peer's log was rebuilt (new
// identity), or it is behind our cursor (restored from an older backup).
func (e *Engine) needsFullPass(peer *Peer, info *InfoResponse) bool {
	return peer.ResyncRequested ||
		(peer.RemoteLogID != nil && *peer.RemoteLogID != info.LogID) ||
		info.HeadSeq < peer.CursorSeq
}

// drain pulls log pages until the peer's head is reached, persisting the
// cursor after each page and when it fails midway.
func (e *Engine) drain(ctx context.Context, pc *pullCtx, prog *Progress) (err error) {
	defer func() {
		// Whatever was applied is kept even when the pull failed midway.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stateWriteTimeout)
		defer cancel()

		if serr := e.Store.SaveProgress(sctx, pc.peer.ID, *prog); serr != nil && err == nil {
			err = serr
		}
	}()

	for {
		lr, err := pc.client.Log(ctx, prog.Cursor, e.BatchSize)
		if err != nil {
			return err
		}

		if lr.LogID != prog.RemoteLogID {
			return fmt.Errorf("%w: the peer log changed identity during the pull", errPeerMismatch)
		}

		prog.RemoteHead = lr.HeadSeq

		if len(lr.Entries) > 0 && prog.OldestPending != nil && lr.Entries[0].ChangedAt.Before(*prog.OldestPending) {
			t := lr.Entries[0].ChangedAt
			prog.OldestPending = &t
		}

		if err := e.applyPage(ctx, pc, lr.Entries, prog); err != nil {
			return err
		}

		if !lr.More {
			prog.OldestPending = nil
			prog.RemoteHead = max(lr.HeadSeq, prog.Cursor)

			return nil
		}

		if err := e.Store.SaveProgress(ctx, pc.peer.ID, *prog); err != nil {
			return err
		}

		prog.Applied, prog.Conflicts = 0, 0
	}
}

func (e *Engine) applyPage(ctx context.Context, pc *pullCtx, entries []Entry, prog *Progress) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // cancellation passes through
		}

		res, err := e.applyEntry(ctx, pc, entry)
		if err != nil {
			if herr := e.handleUnitError(ctx, pc, entry, err); herr != nil {
				return herr
			}

			res = outcomeDeferred
		}

		e.countResult(pc, res)

		if res == outcomeApplied {
			prog.Applied++
		}

		prog.Cursor = entry.Seq

		prog.Conflicts += pc.conflicts
		pc.conflicts = 0
	}

	return nil
}

func (e *Engine) countResult(pc *pullCtx, res outcome) {
	if e.Metrics != nil {
		e.Metrics.Applied.WithLabelValues(pc.repo.Name, pc.peer.Name, res.label()).Inc()
	}
}

// handleUnitError decides what a failure applying one unit means: nil when the
// unit was set aside (the stream moves on), the error when the pull must stop.
func (e *Engine) handleUnitError(ctx context.Context, pc *pullCtx, entry Entry, err error) error {
	var de *deferError

	switch {
	case errors.As(err, &de):
		return e.park(ctx, pc, entry, de.reason, err)
	case isPeerWide(err):
		return err
	}

	// Anything else is held against the unit: the pull stops (it is retried
	// with backoff) until the same unit failed unitFailureLimit pulls in a
	// row, then it is parked.
	if e.recordFailure(pc.peer.ID, entry.UnitKey) < unitFailureLimit {
		return err
	}

	return e.park(ctx, pc, entry, "apply_failed", err)
}

func (e *Engine) park(ctx context.Context, pc *pullCtx, entry Entry, reason string, cause error) error {
	e.clearFailure(pc.peer.ID, entry.UnitKey)

	if err := e.Store.Defer(ctx, pc.peer.ID, entry.UnitKey, entry.Seq, reason, cause.Error()); err != nil {
		return err
	}

	e.Log.Warnw("federation: unit set aside", "repo", pc.repo.Name, "peer", pc.peer.Name, "unit", entry.UnitKey, "reason", reason, "error", cause)

	return nil
}

func (e *Engine) recordFailure(peerID, key string) int {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.failures == nil {
		e.failures = map[string]int{}
	}

	id := peerID + "\x00" + key
	e.failures[id]++

	return e.failures[id]
}

func (e *Engine) clearFailure(peerID, key string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.failures, peerID+"\x00"+key)
}

// isPeerWide reports errors that say nothing about one unit: the peer or the
// network is down, credentials are refused, or the pull was cancelled.
func isPeerWide(err error) bool {
	var (
		he *HTTPError
		ne net.Error
	)

	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &he):
		return he.Fatal() || he.Status >= http.StatusInternalServerError ||
			he.Status == http.StatusTooManyRequests || he.Status == http.StatusRequestTimeout
	case errors.As(err, &ne):
		return true
	case errors.Is(err, netutil.ErrBlockedAddress):
		return true
	}

	return false
}

// retryDeferred gives the due deferred units of the peer another go.
func (e *Engine) retryDeferred(ctx context.Context, pc *pullCtx) error {
	due, err := e.Store.ListDeferred(ctx, pc.peer.ID, true, deferredRetryBatch)
	if err != nil {
		return err
	}

	for _, d := range due {
		_, err := e.applyFetched(ctx, pc, d.UnitKey, 0)
		if err == nil {
			if cerr := e.Store.ClearDeferred(ctx, pc.peer.ID, d.UnitKey); cerr != nil {
				return cerr
			}

			continue
		}

		entry := Entry{UnitKey: d.UnitKey, Seq: d.Seq}

		var de *deferError

		switch {
		case errors.As(err, &de):
			err = e.park(ctx, pc, entry, de.reason, err)
		case isPeerWide(err):
		default:
			err = e.park(ctx, pc, entry, "apply_failed", err)
		}

		if err != nil {
			return err
		}
	}

	return nil
}
