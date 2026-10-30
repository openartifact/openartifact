// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/bgtask"
)

// pomRescanInterval is the least time between two re-evaluations of the poms
// waiting for a parent in one repository. A parent pom upload only schedules
// the work, so a burst of pom uploads (a multi-module deploy) costs one scan of
// the waiting poms per interval instead of one per upload.
const pomRescanInterval = 10 * time.Second

// pomRescanState is what pomRescans keeps per repository.
type pomRescanState struct {
	// running is set while a task owns the repository: it is waiting for the
	// interval to pass or evaluating.
	running bool
	// pending is set by a schedule that arrived while running: one more
	// evaluation is due after the current one.
	pending bool
	// last is the "last evaluated" marker: when the latest evaluation of the
	// repository ended.
	last time.Time
}

// pomRescans coalesces the re-evaluation of the unresolved poms of a repository
// after a pom that may be their parent was stored. Per repository at most one
// evaluation runs and one more waits, and two evaluations start at least
// interval apart; an upload never waits for an evaluation. The state is per
// process: replicas coalesce their own uploads.
type pomRescans struct {
	tasks    *bgtask.Runner
	interval time.Duration
	// run evaluates one repository under the task's context.
	run func(ctx context.Context, repoID string)

	mu    sync.Mutex
	repos map[string]*pomRescanState
}

func newPOMRescans(tasks *bgtask.Runner, interval time.Duration, run func(ctx context.Context, repoID string)) *pomRescans {
	return &pomRescans{tasks: tasks, interval: interval, run: run, repos: map[string]*pomRescanState{}}
}

// schedule asks for the waiting poms of repoID to be re-evaluated. It returns at
// once: the first request starts a background task, a request while one is
// active only marks one more run as due.
func (p *pomRescans) schedule(repoID string) {
	p.mu.Lock()

	// A marker older than the interval no longer delays anything: drop it, so the
	// map holds only repositories evaluated within the last interval.
	for id, st := range p.repos {
		if !st.running && time.Since(st.last) >= p.interval {
			delete(p.repos, id)
		}
	}

	st := p.repos[repoID]
	if st == nil {
		st = &pomRescanState{}
		p.repos[repoID] = st
	}

	if st.running {
		st.pending = true
		p.mu.Unlock()

		return
	}

	st.running = true
	p.mu.Unlock()

	p.tasks.Go(func(ctx context.Context) { p.drain(ctx, repoID, st) })
}

// drain evaluates the repository, then again for as long as a schedule arrived
// meanwhile, keeping the interval between evaluations. It stops when ctx ends.
func (p *pomRescans) drain(ctx context.Context, repoID string, st *pomRescanState) {
	for {
		if ctx.Err() != nil {
			p.release(st)

			return
		}

		p.mu.Lock()
		wait := p.interval - time.Since(st.last)
		p.mu.Unlock()

		if wait > 0 && bgtask.Sleep(ctx, wait) != nil {
			p.release(st)

			return
		}

		// Anything scheduled from here on is not covered by this evaluation.
		p.mu.Lock()
		st.pending = false
		p.mu.Unlock()

		p.run(ctx, repoID)

		p.mu.Lock()
		st.last = time.Now()

		if !st.pending || ctx.Err() != nil {
			st.running = false
			p.mu.Unlock()

			return
		}

		p.mu.Unlock()
	}
}

func (p *pomRescans) release(st *pomRescanState) {
	p.mu.Lock()
	st.running = false
	p.mu.Unlock()
}
