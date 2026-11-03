// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

const (
	// DefaultDownloadCountFlushInterval is how often batched download counts are
	// written when the configuration does not say.
	DefaultDownloadCountFlushInterval = 5 * time.Second

	// downloadCountKickAt is the number of distinct files with unwritten counts
	// that wakes the flusher before its interval is up.
	downloadCountKickAt = 10000

	// downloadCountMaxPending bounds the in-memory map: a count for a file that
	// does not fit (the flusher cannot keep up, or the database is down) is dropped.
	downloadCountMaxPending = 100000

	// downloadCountChunk is how many files one UPDATE writes, so a flush after a
	// long outage does not hold one huge set of row locks.
	downloadCountChunk = 5000

	downloadCountFlushTimeout = 30 * time.Second
)

// downloadCounter coalesces the download counts of package files in memory and
// writes them with one batched UPDATE per flush, so a download costs no
// database write of its own (a hot file otherwise serializes every download on
// one row lock). Counts are additive deltas: replicas flush independently and
// the sums add up. The zero value is ready to use.
//
// The counts are eventually consistent: a download shows in package_files.
// download_count within one flush interval, and a process that dies without
// shutting down loses the counts since its last flush.
type downloadCounter struct {
	mu      sync.Mutex
	pending map[string]int64
	dropped int64
	// interval is read once, by the first download (see core.startDownloadFlusher).
	interval time.Duration

	// flushMu serializes flushes: they take the whole pending map, and an
	// out-of-order failure merge would otherwise race the next flush.
	flushMu sync.Mutex

	kick     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	started  sync.Once
	stopOnce sync.Once
}

// CountFileDownload records one download of a package file. It never blocks on
// the database: the count is written by the next flush (see FlushDownloadCounts).
func (p *core) CountFileDownload(fileID string) {
	c := &p.downloads

	c.mu.Lock()

	if c.pending == nil {
		c.pending = make(map[string]int64)
	}

	n, known := c.pending[fileID]
	if !known && len(c.pending) >= downloadCountMaxPending {
		c.dropped++
		c.mu.Unlock()

		return
	}

	c.pending[fileID] = n + 1
	kick := !known && len(c.pending) == downloadCountKickAt
	c.mu.Unlock()

	c.started.Do(func() { p.startDownloadFlusher() })

	if kick {
		select {
		case c.kick <- struct{}{}:
		default:
		}
	}
}

func (p *core) startDownloadFlusher() {
	c := &p.downloads

	interval := c.interval
	if interval <= 0 {
		interval = DefaultDownloadCountFlushInterval
	}

	c.kick = make(chan struct{}, 1)
	c.stop = make(chan struct{})
	c.done = make(chan struct{})

	go func() {
		defer close(c.done)

		tick := time.NewTicker(interval)
		defer tick.Stop()

		for {
			select {
			case <-tick.C:
			case <-c.kick:
			case <-c.stop:
				return
			}

			p.flushDownloadCountsQuiet()
		}
	}()
}

func (p *core) flushDownloadCountsQuiet() {
	ctx, cancel := context.WithTimeout(context.Background(), downloadCountFlushTimeout)
	defer cancel()

	if err := p.FlushDownloadCounts(ctx); err != nil && p.log != nil {
		p.log.Warnw("download count flush failed; the counts are kept for the next flush", "error", err)
	}
}

// stopDownloadCounter ends the periodic flush and writes what is still pending
// (core.Close calls it while the pool is open). Idempotent.
func (p *core) stopDownloadCounter() {
	c := &p.downloads

	// A download racing the first start must not start the flusher after the stop.
	c.started.Do(func() {})

	c.stopOnce.Do(func() {
		if c.stop != nil {
			close(c.stop)
			<-c.done
		}
	})

	p.flushDownloadCountsQuiet()
}

// FlushDownloadCounts writes the pending download counts, adding them to
// package_files.download_count. On failure the unwritten counts stay pending
// (merged with any recorded meanwhile) for the next flush.
func (p *core) FlushDownloadCounts(ctx context.Context) error {
	c := &p.downloads

	c.flushMu.Lock()
	defer c.flushMu.Unlock()

	c.mu.Lock()
	batch := c.pending
	c.pending = nil
	dropped := c.dropped
	c.dropped = 0
	c.mu.Unlock()

	if dropped > 0 && p.log != nil {
		p.log.Warnw("download counts dropped: too many files with unwritten counts", "dropped", dropped)
	}

	if len(batch) == 0 {
		return nil
	}

	ids := make([]string, 0, len(batch))
	for id := range batch {
		ids = append(ids, id)
	}

	// Sorted ids keep the row locks of every chunk, and of every replica's
	// flush, in one global order.
	slices.Sort(ids)

	for start := 0; start < len(ids); start += downloadCountChunk {
		chunk := ids[start:min(start+downloadCountChunk, len(ids))]
		counts := make([]int64, len(chunk))

		for i, id := range chunk {
			counts[i] = batch[id]
		}

		err := p.q.AddFileDownloads(ctx, dbsqlc.AddFileDownloadsParams{Ids: chunk, Counts: counts})
		if err == nil {
			continue
		}

		// A malformed id would fail every retry of its chunk forever; its counts
		// are worthless, so drop them. Anything else (database down, timeout) is
		// transient.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "22") {
			continue
		}

		p.restoreDownloadCounts(ids[start:], batch)

		return fmt.Errorf("write download counts: %w", err)
	}

	return nil
}

// restoreDownloadCounts puts counts that could not be written back into the
// pending map, adding to whatever was recorded since.
func (p *core) restoreDownloadCounts(ids []string, batch map[string]int64) {
	c := &p.downloads

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.pending == nil {
		c.pending = make(map[string]int64, len(ids))
	}

	for _, id := range ids {
		n, known := c.pending[id]
		if !known && len(c.pending) >= downloadCountMaxPending {
			c.dropped++
			continue
		}

		c.pending[id] = n + batch[id]
	}
}
