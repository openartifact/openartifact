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
// is present. With Offline set nothing is downloaded. Stop ends the updaters.
func (r *Registry) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		return
	}

	ctx, r.cancel = context.WithCancel(ctx)

	if r.opts.Offline {
		r.log.Infow("scanner database downloads disabled (offline mode)", "cache_dir", r.opts.CacheDir)
		return
	}

	for _, u := range []struct {
		tool   *cliTool
		update func(context.Context) error
	}{
		{&r.trivy, r.updateTrivyDB},
		{&r.grype, r.updateGrypeDB},
	} {
		if _, err := u.tool.lookup(); err != nil {
			continue
		}

		r.wg.Go(func() { r.refreshLoop(ctx, u.tool, u.update) })
	}
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

	if ctx.Err() != nil {
		return 0, false
	}

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
