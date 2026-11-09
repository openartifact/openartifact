// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/vexsync"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// vexFeedsTick is how often the worker looks for feeds due for a sync.
	vexFeedsTick = 15 * time.Minute

	// vexFeedsApplyInterval is how often the statements of every feed are
	// matched again against the latest scans, for the artifacts scanned since.
	vexFeedsApplyInterval = time.Hour

	// vexFeedSyncTimeout bounds the sync of one feed.
	vexFeedSyncTimeout = 30 * time.Minute

	// vexFeedsApplyTimeout bounds one materialization of every feed.
	vexFeedsApplyTimeout = 15 * time.Minute

	// vexFeedsListTimeout bounds reading the feeds.
	vexFeedsListTimeout = time.Minute
)

// VEXFeedSyncer syncs and materializes the vendor VEX feeds (vexsync.Service).
type VEXFeedSyncer interface {
	SyncFeed(ctx context.Context, feedID string) (models.VEXFeedSyncResult, error)
	ApplyFeeds(ctx context.Context) error
}

// VEXFeedLister lists the configured vendor VEX feeds.
type VEXFeedLister interface {
	ListVEXFeeds(ctx context.Context) ([]*models.VEXFeed, error)
}

// VEXFeedsWorker syncs every enabled vendor VEX feed once per its interval and
// rematerializes the statements of every feed hourly. Each feed is synced
// under its own advisory lock (taken by the syncer), so replicas share the
// work and never sync one feed twice at once; the due times are read from the
// feeds, so a restart does not sync again.
type VEXFeedsWorker struct {
	*lifecycle

	syncer VEXFeedSyncer
	feeds  VEXFeedLister
	log    *logger.Logger

	// lastApply is when this worker last materialized every feed.
	lastApply time.Time

	// now reads the clock; tests move time.
	now func() time.Time
}

// NewVEXFeedsWorker returns the worker.
func NewVEXFeedsWorker(syncer VEXFeedSyncer, feeds VEXFeedLister, log *logger.Logger) *VEXFeedsWorker {
	return &VEXFeedsWorker{lifecycle: newLifecycle(), syncer: syncer, feeds: feeds, log: log, now: time.Now}
}

// Start begins the schedule: a pass at once, then one every vexFeedsTick.
// Only the first call counts.
func (w *VEXFeedsWorker) Start() {
	w.startOnce.Do(func() {
		go w.loop()
	})
}

func (w *VEXFeedsWorker) loop() {
	defer close(w.doneCh)

	ticker := time.NewTicker(vexFeedsTick)
	defer ticker.Stop()

	for {
		w.runScheduled()

		select {
		case <-ticker.C:
		case <-w.stopCh:
			return
		}
	}
}

// runScheduled runs one pass; a panic ends the pass, not the schedule.
func (w *VEXFeedsWorker) runScheduled() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in VEX feed sync", "panic", r)
		}
	}()

	if err := w.RunOnce(w.runCtx); err != nil && w.runCtx.Err() == nil {
		w.log.Errorw("VEX feed pass failed", "error", err)
	}
}

// RunOnce syncs the feeds that are due, then rematerializes every feed when
// the last materialization is an hour old. A feed whose sync fails does not
// stop the others.
func (w *VEXFeedsWorker) RunOnce(ctx context.Context) error {
	lctx, cancel := context.WithTimeout(ctx, vexFeedsListTimeout)
	feeds, err := w.feeds.ListVEXFeeds(lctx)

	cancel()

	if err != nil {
		return fmt.Errorf("list VEX feeds: %w", err)
	}

	for _, f := range feeds {
		if ctx.Err() != nil {
			return nil
		}

		if w.due(f) {
			w.sync(ctx, f)
		}
	}

	if w.now().Sub(w.lastApply) < vexFeedsApplyInterval || len(feeds) == 0 {
		return nil
	}

	actx, cancel := context.WithTimeout(ctx, vexFeedsApplyTimeout)
	defer cancel()

	if err := w.syncer.ApplyFeeds(actx); err != nil {
		return fmt.Errorf("apply VEX feeds: %w", err)
	}

	w.lastApply = w.now()

	return nil
}

// due reports whether the feed is enabled and its last attempt is older than
// its interval (or there was none).
func (w *VEXFeedsWorker) due(f *models.VEXFeed) bool {
	if !f.Enabled {
		return false
	}

	if f.LastAttemptAt == nil {
		return true
	}

	return !f.LastAttemptAt.Add(time.Duration(f.IntervalHours) * time.Hour).After(w.now())
}

func (w *VEXFeedsWorker) sync(ctx context.Context, f *models.VEXFeed) {
	sctx, cancel := context.WithTimeout(ctx, vexFeedSyncTimeout)
	defer cancel()

	if _, err := w.syncer.SyncFeed(sctx, f.ID); err != nil && !errors.Is(err, vexsync.ErrSyncRunning) && ctx.Err() == nil {
		w.log.Errorw("VEX feed sync failed", "feed", f.Name, "error", err)
	}
}
