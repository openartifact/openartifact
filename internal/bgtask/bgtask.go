// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package bgtask runs the fire-and-forget goroutines of a server (async
// backups and scans, startup repairs) under one lifecycle: they receive a
// context that is cancelled at shutdown, and shutdown waits for them, bounded.
package bgtask

import (
	"context"
	"sync"
	"time"
)

// Runner owns the lifecycle context of background tasks and counts them.
// The zero value is not usable; call New.
type Runner struct {
	ctx    context.Context //nolint:containedctx // the runner is the owner of a lifecycle context
	cancel context.CancelFunc

	mu     sync.Mutex
	wg     sync.WaitGroup
	closed bool
}

// New returns a Runner whose context ends when parent ends or at Shutdown.
func New(parent context.Context) *Runner {
	ctx, cancel := context.WithCancel(parent)

	return &Runner{ctx: ctx, cancel: cancel}
}

// Context is the lifecycle context: cancelled by Shutdown.
func (r *Runner) Context() context.Context { return r.ctx }

// Go runs fn on a new goroutine with the lifecycle context and tracks it until
// it returns. After Shutdown no goroutine is started: fn runs on the caller's
// goroutine with the already cancelled context, so it can record the failure
// of the work it was handed (for example a job row that must not stay
// "running") instead of dropping it silently.
func (r *Runner) Go(fn func(ctx context.Context)) {
	r.mu.Lock()

	if r.closed {
		r.mu.Unlock()
		fn(r.ctx)

		return
	}

	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()

		fn(r.ctx)
	}()
}

// Shutdown cancels the lifecycle context and waits for the running tasks, at
// most timeout. It reports whether every task returned in time and is safe to
// call more than once.
func (r *Runner) Shutdown(timeout time.Duration) bool {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()

	r.cancel()

	done := make(chan struct{})

	go func() {
		r.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
