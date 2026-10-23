// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package replication copies every blob of the primary store to a secondary
// backend asynchronously, applies deletions to it, verifies that both sides
// agree and lets reads fall back to the replica (docs/operations/replication.md).
//
// The pieces share one core: Store wraps the primary (tiered) store and queues
// work in the durable blob_replication table, Replicator works the queue off,
// Verifier walks both sides and repairs what is missing or damaged, and Service
// ties them together for the server.
package replication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// enqueueTimeout bounds queueing an operation. It runs on a context
	// detached from the caller's: a client that gave up right after its bytes
	// landed must still get the blob replicated.
	enqueueTimeout = 10 * time.Second

	// backoffBase is the retry delay after the first failure; it doubles with
	// every further one up to Options.MaxBackoff.
	backoffBase = 5 * time.Second

	// failingAttempts is the attempt count from which a queued operation is
	// reported as failing.
	failingAttempts = 5

	// maxErrorLen truncates the error text stored with a failed operation.
	maxErrorLen = 500
)

var (
	// ErrMissingPrimary is the failure of a queued copy whose blob is not on
	// the primary store.
	ErrMissingPrimary = errors.New("blob is missing from the primary store")

	// ErrCorrupt is returned when no side holds bytes that hash to the blob id.
	ErrCorrupt = errors.New("no copy of the blob matches its sha256")

	errPendingDelete = errors.New("blob is queued for deletion on the replica")
	errNoQueue       = errors.New("replication queue is not available")
)

// Fallback is the read fallback mode (storage.ReadFallback*).
type Fallback string

// Options tunes replication. The zero value is not usable: take it from
// OptionsFromConfig.
type Options struct {
	ReadFallback   Fallback
	Interval       time.Duration
	Batch          int
	Concurrency    int
	VerifyInterval time.Duration
	VerifyDeep     bool
	MaxBackoff     time.Duration
}

// OptionsFromConfig converts the storage.replica settings.
func OptionsFromConfig(c storage.ReplicaConfig) Options {
	return Options{
		ReadFallback: Fallback(c.ReadFallback), Interval: c.Interval, Batch: c.Batch, Concurrency: c.Concurrency,
		VerifyInterval: c.VerifyInterval, VerifyDeep: c.VerifyDeep, MaxBackoff: c.MaxBackoff,
	}
}

// LockFunc runs fn under the per-blob lock uploaders and deleters share
// (storage.WithBlobLock).
type LockFunc func(ctx context.Context, sha string, fn func(ctx context.Context) error) error

// core is the state and the repair primitives the store, the replicator and
// the verifier share.
type core struct {
	inner   storage.BlobStore
	replica storage.BlobStore
	queue   database.BlobReplicationStore
	lock    LockFunc
	opts    Options
	log     *logger.Logger
	m       *metrics.ReplicationMetrics

	now    func() time.Time
	jitter func(d time.Duration) time.Duration
	// kick wakes the replicator after an enqueue (nil in tests without one).
	kick func()
}

func (c *core) withLock(ctx context.Context, sha string, fn func(ctx context.Context) error) error {
	if c.lock == nil {
		return fn(ctx)
	}

	return c.lock(ctx, sha, fn)
}

// enqueue queues op for the blob, durably and cheaply (one upsert).
func (c *core) enqueue(ctx context.Context, sha, op string) error {
	if err := storage.ValidateBlobID(sha); err != nil {
		return err //nolint:wrapcheck // sentinel
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), enqueueTimeout)
	defer cancel()

	if err := c.queue.EnqueueBlobReplication(ctx, sha, op); err != nil {
		c.m.EnqueueFailures.Inc()

		return fmt.Errorf("queue %s of blob %s for replication: %w", op, sha, err)
	}

	if c.kick != nil {
		c.kick()
	}

	return nil
}

// pending returns the queued operation of the blob, nil when none.
func (c *core) pending(ctx context.Context, sha string) (*database.BlobReplication, error) {
	return c.queue.GetBlobReplication(ctx, sha) //nolint:wrapcheck // wrapped by the database layer
}

// hasPendingDelete reports whether a deletion of the blob is queued.
func (c *core) hasPendingDelete(ctx context.Context, sha string) (bool, error) {
	op, err := c.pending(ctx, sha)
	if err != nil {
		return false, err
	}

	return op != nil && op.Op == database.BlobReplicationDelete, nil
}

// backoff is the retry delay after attempts failed attempts: exponential from
// backoffBase, capped at MaxBackoff, with jitter so a recovering replica is not
// hit by every blob at once.
func (c *core) backoff(attempts int) time.Duration {
	d := backoffBase

	for range max(attempts, 0) {
		if d >= c.opts.MaxBackoff {
			break
		}

		d *= 2
	}

	d = min(d, c.opts.MaxBackoff)

	return c.jitter(d)
}

// defaultJitter returns a random delay in [d/2, d].
func defaultJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}

	return half + rand.N(half+1) //nolint:gosec // retry jitter, not a secret
}

func truncateError(err error) string {
	msg := err.Error()
	if len(msg) > maxErrorLen {
		msg = msg[:maxErrorLen] + "..."
	}

	return msg
}

// isNotFound reports whether err means the blob does not exist.
func isNotFound(err error) bool { return errors.Is(err, storage.ErrBlobNotFound) }

// hashMatches reads the blob from store and reports whether its bytes hash to
// sha. A blob that does not exist is (false, ErrBlobNotFound).
func hashMatches(ctx context.Context, store storage.BlobStore, sha string) (bool, error) {
	rc, err := store.Get(ctx, sha)
	if err != nil {
		return false, err //nolint:wrapcheck // callers match ErrBlobNotFound
	}

	defer func() { _ = rc.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return false, fmt.Errorf("read blob %s: %w", sha, err)
	}

	return hex.EncodeToString(h.Sum(nil)) == sha, nil
}

// putVerified streams the blob from src into dst. Put checks the digest, so
// bytes that do not hash to sha are never stored.
func putVerified(ctx context.Context, src, dst storage.BlobStore, sha string) (int64, error) {
	rc, err := src.Get(ctx, sha)
	if err != nil {
		return 0, err //nolint:wrapcheck // callers match ErrBlobNotFound
	}

	defer func() { _ = rc.Close() }()

	cr := &countingReader{r: rc}

	if _, err := dst.Put(ctx, cr, map[string]string{"sha256": sha}); err != nil {
		return 0, err //nolint:wrapcheck // callers match ErrDigestMismatch
	}

	return cr.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err //nolint:wrapcheck // io.Reader contract
}

// copyToReplica makes the blob exist on the replica with the primary's bytes.
// It returns the bytes written (0 when the replica already had the blob). A
// copy the replica holds with a different size is replaced once the hashes
// show which side is right (fixMismatch).
func (c *core) copyToReplica(ctx context.Context, sha string) (int64, error) {
	size, err := c.inner.GetSize(ctx, sha)
	if err != nil {
		if isNotFound(err) {
			return 0, fmt.Errorf("%w: %s", ErrMissingPrimary, sha)
		}

		return 0, fmt.Errorf("stat blob %s on the primary store: %w", sha, err)
	}

	rsize, err := c.replica.GetSize(ctx, sha)

	switch {
	case err == nil && rsize == size:
		return 0, nil
	case err == nil:
		return c.fixMismatch(ctx, sha)
	case !isNotFound(err):
		return 0, fmt.Errorf("stat blob %s on the replica: %w", sha, err)
	}

	n, err := putVerified(ctx, c.inner, c.replica, sha)
	if err != nil {
		if isNotFound(err) {
			return 0, fmt.Errorf("%w: %s", ErrMissingPrimary, sha)
		}

		return 0, fmt.Errorf("copy blob %s to the replica: %w", sha, err)
	}

	return n, c.settleCopy(ctx, sha)
}

// settleCopy closes the window between reading the blob from the primary and
// the replica write: when the blob was deleted from the primary meanwhile (and
// its deletion may already have been applied to the replica) the copy just
// written is removed again. It runs under the per-blob lock, so it sees either
// the finished deletion or the deletion that is queued after it.
func (c *core) settleCopy(ctx context.Context, sha string) error {
	return c.withLock(ctx, sha, func(ctx context.Context) error {
		exists, err := c.inner.Exists(ctx, sha)
		if err != nil {
			return fmt.Errorf("re-check blob %s on the primary store: %w", sha, err)
		}

		if exists {
			return nil
		}

		referenced, err := c.queue.BlobReplicationReferenced(ctx, sha)
		if err != nil {
			return fmt.Errorf("check references of blob %s: %w", sha, err)
		}

		if referenced {
			return nil // the primary lost a blob the database needs: keep the copy
		}

		if err := c.replica.Delete(ctx, sha); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove the copy of deleted blob %s from the replica: %w", sha, err)
		}

		return nil
	})
}

// fixMismatch resolves a blob the two sides hold with different bytes: blobs
// are content addressed, so the side whose bytes hash to the id is right and
// overwrites the other. It returns the bytes written to the replica.
func (c *core) fixMismatch(ctx context.Context, sha string) (int64, error) {
	pOK, perr := hashMatches(ctx, c.inner, sha)
	if perr != nil && !isNotFound(perr) {
		return 0, perr
	}

	rOK, rerr := hashMatches(ctx, c.replica, sha)
	if rerr != nil && !isNotFound(rerr) {
		return 0, rerr
	}

	switch {
	case pOK && rOK:
		return 0, nil
	case pOK:
		if err := c.replica.Delete(ctx, sha); err != nil && !isNotFound(err) {
			return 0, fmt.Errorf("remove the damaged replica copy of %s: %w", sha, err)
		}

		n, err := putVerified(ctx, c.inner, c.replica, sha)
		if err != nil {
			return 0, fmt.Errorf("rewrite replica copy of %s: %w", sha, err)
		}

		return n, c.settleCopy(ctx, sha)
	case rOK:
		_, err := c.restorePrimary(ctx, sha, true)

		return 0, err
	default:
		return 0, fmt.Errorf("%w: %s", ErrCorrupt, sha)
	}
}

// restorePrimary writes the replica's copy of the blob back to the primary
// store, under the per-blob lock and unless a deletion of the blob is queued
// (a deleted blob is never resurrected). With force a copy the primary has is
// replaced (it is known to be damaged); otherwise a blob the primary has is
// left alone. It reports whether it wrote the blob.
func (c *core) restorePrimary(ctx context.Context, sha string, force bool) (bool, error) {
	var restored bool

	err := c.withLock(ctx, sha, func(ctx context.Context) error {
		if del, err := c.hasPendingDelete(ctx, sha); err != nil {
			return err
		} else if del {
			return errPendingDelete
		}

		exists, err := c.inner.Exists(ctx, sha)
		if err != nil {
			return fmt.Errorf("check blob %s on the primary store: %w", sha, err)
		}

		if exists && !force {
			return nil
		}

		if exists {
			if err := c.inner.Delete(ctx, sha); err != nil && !isNotFound(err) {
				return fmt.Errorf("remove the damaged primary copy of %s: %w", sha, err)
			}
		}

		if _, err := putVerified(ctx, c.replica, c.inner, sha); err != nil {
			return fmt.Errorf("restore blob %s on the primary store: %w", sha, err)
		}

		restored = true

		return nil
	})

	return restored, err
}
