// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"sync"
	"time"
)

// lifecycle is the start and stop plumbing the periodic workers share. The
// schedule goroutine closes doneCh when it ends; stopping closes stopCh to ask
// it to. runCtx is the context of the passes and is cancelled when the worker
// stops, so an in-flight pass ends promptly.
type lifecycle struct {
	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	runCtx    context.Context //nolint:containedctx // lifecycle context of the worker
	cancel    context.CancelFunc
}

func newLifecycle() *lifecycle {
	runCtx, cancel := context.WithCancel(context.Background())

	return &lifecycle{stopCh: make(chan struct{}), doneCh: make(chan struct{}), runCtx: runCtx, cancel: cancel}
}

// Stop cancels an in-flight pass and waits for the schedule goroutine to exit.
// It is idempotent and safe without Start.
func (l *lifecycle) Stop() {
	l.stopOnce.Do(func() {
		close(l.stopCh)
		l.cancel()
	})

	// Never started: nothing will close doneCh, and a later Start must not.
	l.startOnce.Do(func() { close(l.doneCh) })

	<-l.doneCh
}

// stopGrace is how long stopGracefully lets the pass in flight finish before
// cancelling it.
const stopGrace = 10 * time.Second

// stopGracefully asks the schedule goroutine to end and lets the pass in
// flight finish for up to stopGrace before cancelling it. started tells
// whether the goroutine runs: if not, there is nothing to wait for.
func (l *lifecycle) stopGracefully(started bool) {
	l.stopOnce.Do(func() { close(l.stopCh) })

	if !started {
		l.cancel()

		return
	}

	select {
	case <-l.doneCh:
	case <-time.After(stopGrace):
		l.cancel()
		<-l.doneCh
	}

	l.cancel()
}

// startEvery is the Start of a worker with a fixed schedule: it runs pass once
// per interval, unless the worker is disabled (enabled false, or a non-positive
// interval), in which case it only marks the worker as done. Only the first
// call counts.
func (l *lifecycle) startEvery(enabled bool, interval time.Duration, pass func()) {
	l.startOnce.Do(func() {
		if !enabled || interval <= 0 {
			close(l.doneCh)

			return
		}

		go l.runEvery(interval, pass)
	})
}

// runEvery is the schedule goroutine of a worker that runs one pass per
// interval: it calls pass on every tick until stopped, then closes doneCh.
func (l *lifecycle) runEvery(interval time.Duration, pass func()) {
	defer close(l.doneCh)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pass()
		case <-l.stopCh:
			return
		}
	}
}
