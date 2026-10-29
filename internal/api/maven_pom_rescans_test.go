// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/bgtask"
)

// rescanRecorder is a run function that records when each run started and
// ended, and can hold the runs of chosen repositories.
type rescanRecorder struct {
	mu      sync.Mutex
	started []string
	spans   map[string][][2]time.Time
	hold    map[string]chan struct{}
}

func newRescanRecorder() *rescanRecorder {
	return &rescanRecorder{spans: map[string][][2]time.Time{}, hold: map[string]chan struct{}{}}
}

// holdRuns makes the runs of repoID wait until the returned func is called.
func (r *rescanRecorder) holdRuns(repoID string) func() {
	ch := make(chan struct{})

	r.mu.Lock()
	r.hold[repoID] = ch
	r.mu.Unlock()

	var once sync.Once

	return func() { once.Do(func() { close(ch) }) }
}

func (r *rescanRecorder) run(ctx context.Context, repoID string) {
	start := time.Now()

	r.mu.Lock()
	r.started = append(r.started, repoID)
	ch := r.hold[repoID]
	r.mu.Unlock()

	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
		}
	}

	r.mu.Lock()
	r.spans[repoID] = append(r.spans[repoID], [2]time.Time{start, time.Now()})
	r.mu.Unlock()
}

func (r *rescanRecorder) runsStarted(repoID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := 0

	for _, id := range r.started {
		if id == repoID {
			n++
		}
	}

	return n
}

func (r *rescanRecorder) runsFinished(repoID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.spans[repoID])
}

func newTestRescans(t *testing.T, interval time.Duration, rec *rescanRecorder) *pomRescans {
	t.Helper()

	tasks := bgtask.New(t.Context())
	t.Cleanup(func() { tasks.Shutdown(5 * time.Second) })

	return newPOMRescans(tasks, interval, rec.run)
}

// scheduleReturnsPromptly calls p.schedule n times, failing the test when they do
// not return: an upload makes the call and must not wait for a run, held or not.
func scheduleReturnsPromptly(t *testing.T, p *pomRescans, repoID string, n int) {
	t.Helper()

	done := make(chan struct{})

	go func() {
		defer close(done)

		for range n {
			p.schedule(repoID)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("schedule blocks the caller while a run is in progress")
	}
}

func (p *pomRescans) idle() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, st := range p.repos {
		if st.running {
			return false
		}
	}

	return true
}

func TestPOMRescansCoalesceRequestsMadeDuringARun(t *testing.T) {
	rec := newRescanRecorder()
	release := rec.holdRuns("repo-1")
	t.Cleanup(release)

	p := newTestRescans(t, 0, rec)

	scheduleReturnsPromptly(t, p, "repo-1", 1)
	waitUntil(t, "the first run to start", func() bool { return rec.runsStarted("repo-1") == 1 })

	// While the run is held, a burst of requests must neither block the caller
	// (an upload) nor start more runs.
	scheduleReturnsPromptly(t, p, "repo-1", 50)

	if got := rec.runsStarted("repo-1"); got != 1 {
		t.Fatalf("runs started while one is in progress = %d, want 1", got)
	}

	release()
	waitUntil(t, "the scheduler to go idle", p.idle)

	if got := rec.runsFinished("repo-1"); got != 2 {
		t.Fatalf("runs for 51 requests = %d, want 2 (the running one and one coalesced)", got)
	}
}

func TestPOMRescansRunEachRepositoryOnItsOwn(t *testing.T) {
	rec := newRescanRecorder()
	release := rec.holdRuns("repo-a")
	t.Cleanup(release)

	p := newTestRescans(t, 0, rec)

	scheduleReturnsPromptly(t, p, "repo-a", 1)
	waitUntil(t, "the run of repo-a to start", func() bool { return rec.runsStarted("repo-a") == 1 })

	// repo-a is stuck, repo-b is not held up by it.
	scheduleReturnsPromptly(t, p, "repo-b", 1)
	waitUntil(t, "the run of repo-b", func() bool { return rec.runsFinished("repo-b") == 1 })

	release()
	waitUntil(t, "the scheduler to go idle", p.idle)

	if a, b := rec.runsFinished("repo-a"), rec.runsFinished("repo-b"); a != 1 || b != 1 {
		t.Fatalf("runs = repo-a %d, repo-b %d, want 1 each", a, b)
	}
}

func TestPOMRescansKeepTheIntervalSinceTheLastEvaluation(t *testing.T) {
	const interval = 150 * time.Millisecond

	rec := newRescanRecorder()
	p := newTestRescans(t, interval, rec)

	p.schedule("repo-1")
	waitUntil(t, "the first run", func() bool { return rec.runsFinished("repo-1") == 1 })

	// Requested right after the first evaluation: it must wait for the interval.
	p.schedule("repo-1")
	waitUntil(t, "the second run", func() bool { return rec.runsFinished("repo-1") == 2 })

	rec.mu.Lock()
	defer rec.mu.Unlock()

	first, second := rec.spans["repo-1"][0], rec.spans["repo-1"][1]
	if gap := second[0].Sub(first[1]); gap < interval-10*time.Millisecond {
		t.Fatalf("second evaluation started %v after the first ended, want at least %v", gap, interval)
	}
}

func TestPOMRescansForgetRepositoriesEvaluatedLongAgo(t *testing.T) {
	rec := newRescanRecorder()
	p := newTestRescans(t, time.Millisecond, rec)

	for i := range 20 {
		p.schedule("repo-" + strconv.Itoa(i))
	}

	waitUntil(t, "the scheduler to go idle", p.idle)
	time.Sleep(5 * time.Millisecond)

	// The markers of the 20 repositories are older than the interval: the next
	// request drops them.
	p.schedule("repo-last")
	waitUntil(t, "the scheduler to go idle", p.idle)

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.repos) != 1 {
		t.Fatalf("scheduler keeps %d repositories, want only the recently evaluated one", len(p.repos))
	}
}

func TestPOMRescansStopWhenTheServerShutsDown(t *testing.T) {
	rec := newRescanRecorder()
	t.Cleanup(rec.holdRuns("repo-1"))

	tasks := bgtask.New(context.Background())
	p := newPOMRescans(tasks, 0, rec.run)

	scheduleReturnsPromptly(t, p, "repo-1", 1)
	waitUntil(t, "the run to start", func() bool { return rec.runsStarted("repo-1") == 1 })
	scheduleReturnsPromptly(t, p, "repo-1", 1)

	if !tasks.Shutdown(5 * time.Second) {
		t.Fatal("shutdown did not wait the run out")
	}

	if got := rec.runsStarted("repo-1"); got != 1 {
		t.Fatalf("runs started = %d, want the pending run dropped at shutdown", got)
	}

	if !p.idle() {
		t.Fatal("scheduler still marks the repository running after shutdown")
	}
}

// A multi-module deploy stores many pom-packaged poms in a row. Each used to
// re-scan the repository inside its own request; now they cost a bounded
// number of background evaluations, and the pom that waited for its parent is
// still resolved.
func TestMavenPut_ManyParentUploadsCoalesceTheRescan(t *testing.T) {
	const modules = 20

	e := newMavenParentEnv(t)
	e.handler.rescans.interval = 300 * time.Millisecond

	var runs atomic.Int32

	run := e.handler.rescans.run
	e.handler.rescans.run = func(ctx context.Context, repoID string) {
		runs.Add(1)
		run(ctx, repoID)
	}

	child := e.put("com.acme", "child", "1", pomXML("com.acme", "child", "1", "", parentRef("com.acme", "parent", "1"), ""))
	if child.Code != http.StatusCreated {
		t.Fatalf("child = %d: %s", child.Code, child.Body.String())
	}

	for i := range modules {
		name := "module" + strconv.Itoa(i)
		if rec := e.put("com.acme", name, "1", pomXML("com.acme", name, "1", "pom", "", "MIT")); rec.Code != http.StatusCreated {
			t.Fatalf("%s = %d: %s", name, rec.Code, rec.Body.String())
		}
	}

	if rec := e.put("com.acme", "parent", "1", pomXML("com.acme", "parent", "1", "pom", "", "EPL-2.0")); rec.Code != http.StatusCreated {
		t.Fatalf("parent = %d: %s", rec.Code, rec.Body.String())
	}

	id := child.Header().Get("X-Oa-Artifact-Id")
	if got := e.waitForScan(id, hasLicenses("EPL-2.0")); !hasLicenses("EPL-2.0")(got) {
		t.Fatalf("scan of the child after the parent upload = %+v, want EPL-2.0", got)
	}

	waitUntil(t, "the scheduler to go idle", e.handler.rescans.idle)

	if got := runs.Load(); got < 1 || got > 3 {
		t.Fatalf("%d pom uploads cost %d evaluations, want a bounded number (1 to 3)", modules+1, got)
	}
}
