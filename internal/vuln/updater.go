// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"time"
)

// Start downloads the vulnerability databases of the available CLI scanners in
// the background and refreshes them every Options.UpdateInterval, retrying a
// failed download with exponential backoff. Scans never download a database
// themselves: they run against the cache and fail with FailureDatabase until it
// is present. A scanner whose install mode is "auto" and that is neither
// configured nor on PATH is installed first. With Offline set nothing is
// downloaded. Stop ends the updaters.
func (r *Registry) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		return
	}

	ctx, r.cancel = context.WithCancel(ctx)

	if r.opts.Offline {
		r.log.Infow("scanner database downloads disabled (offline mode)", "cache_dir", r.opts.CacheDir)
	}

	for _, u := range []struct {
		tool   *cliTool
		update func(context.Context) error
	}{
		{&r.trivy, r.updateTrivyDB},
		{&r.grype, r.updateGrypeDB},
	} {
		r.wg.Go(func() { r.runTool(ctx, u.tool, u.update) })
	}
}

// runTool is one scanner's background life: make the binary resolvable (adopt
// or download the pinned release when its install mode is "auto"), then keep
// its database fresh. Offline mode adopts an installed copy but downloads
// nothing.
func (r *Registry) runTool(ctx context.Context, t *cliTool, update func(context.Context) error) {
	if t.installAuto {
		r.ensureInstalled(ctx, t, !r.opts.Offline)
	}

	if r.opts.Offline {
		return
	}

	if _, err := t.lookup(); err != nil {
		return
	}

	r.refreshLoop(ctx, t, update)
}

// Stop cancels the database updaters and waits for them to return.
func (r *Registry) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	r.wg.Wait()
}

func (r *Registry) refreshLoop(ctx context.Context, t *cliTool, update func(context.Context) error) {
	retry := r.retryInitial

	// After a restart the database on disk is as fresh as its last refresh.
	if mt := dbMTime(t.cacheDir); !mt.IsZero() && r.metrics != nil {
		r.metrics.ScannerDBLastSuccessTimestamp.WithLabelValues(t.name).Set(float64(mt.Unix()))
	}

	for {
		wait, again := r.refreshOnce(ctx, t, update, &retry)
		if !again {
			return
		}

		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// refreshOnce runs one update and returns how long to wait before the next and
// whether there is a next one.
func (r *Registry) refreshOnce(ctx context.Context, t *cliTool, update func(context.Context) error, retry *time.Duration) (time.Duration, bool) {
	t.beginUpdate()

	uctx, cancel := context.WithTimeout(ctx, dbUpdateTimeout)
	err := update(uctx)

	cancel()
	t.endUpdate(err)

	// An update cut short by shutdown says nothing about the mirror.
	if ctx.Err() != nil {
		return 0, false
	}

	r.countDBUpdate(t.name, err)

	if err != nil {
		r.log.Errorw("scanner database update failed",
			"scanner", t.name,
			"cache_dir", t.cacheDir,
			"retry_in", retry.String(),
			"error", err,
		)

		wait := *retry
		*retry = min(*retry*2, r.retryMax)

		return wait, true
	}

	*retry = r.retryInitial

	r.log.Infow("scanner database up to date", "scanner", t.name, "cache_dir", t.cacheDir)

	return r.opts.UpdateInterval, r.opts.UpdateInterval > 0
}

func (r *Registry) countDBUpdate(scanner string, err error) {
	if r.metrics == nil {
		return
	}

	if err != nil {
		r.metrics.ScannerDBUpdateAttemptsTotal.WithLabelValues(scanner, "failure").Inc()

		return
	}

	r.metrics.ScannerDBUpdateAttemptsTotal.WithLabelValues(scanner, "success").Inc()
	r.metrics.ScannerDBLastSuccessTimestamp.WithLabelValues(scanner).SetToCurrentTime()
}
