// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package workq

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

type finishCall struct {
	ref  ref
	upTo time.Time
}

// fakeBackend hands out the queued requests once and records what the consumer
// does with them.
type fakeBackend struct {
	mu       sync.Mutex
	queue    []Request
	kinds    []string
	renewed  [][]ref
	finished []finishCall
	released []ref
	claimErr error
}

func (f *fakeBackend) claim(_ context.Context, kinds []string, _ int, _ time.Duration) ([]Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.kinds = kinds

	if f.claimErr != nil {
		return nil, f.claimErr
	}

	out := f.queue
	f.queue = nil

	return out, nil
}

func (f *fakeBackend) renew(_ context.Context, held []ref, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.renewed = append(f.renewed, held)

	return nil
}

func (f *fakeBackend) finish(_ context.Context, r ref, upTo time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.finished = append(f.finished, finishCall{ref: r, upTo: upTo})

	return nil
}

func (f *fakeBackend) release(_ context.Context, r ref) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.released = append(f.released, r)

	return nil
}

func (f *fakeBackend) releasedRefs() []ref {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]ref(nil), f.released...)
}

func (f *fakeBackend) finishedCalls() []finishCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]finishCall(nil), f.finished...)
}

func (f *fakeBackend) renewCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.renewed)
}

func testLog(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	return log
}

func TestConsumerDoneDeletesTheRequestRightAway(t *testing.T) {
	at := time.Now()
	fb := &fakeBackend{queue: []Request{{Kind: "ai:analyze", Key: "k1", RequestedAt: at}}}
	c := newConsumer(fb, testLog(t))

	var got Request

	c.Handle("ai:analyze", func(_ context.Context, r Request) Result {
		got = r
		return Done
	})

	c.tick(context.Background(), []string{"ai:analyze"})
	c.Flush(context.Background())

	if got.Key != "k1" {
		t.Fatalf("handler got %+v", got)
	}

	calls := fb.finishedCalls()
	if len(calls) != 1 || calls[0].ref != (ref{"ai:analyze", "k1"}) || !calls[0].upTo.Equal(at) {
		t.Fatalf("finished = %+v, want one call for k1 up to %v", calls, at)
	}
}

func TestConsumerLaterKeepsTheClaimUntilSettled(t *testing.T) {
	at := time.Now()
	fb := &fakeBackend{queue: []Request{{Kind: "regen:rpm", Key: "repo", RequestedAt: at}}}
	c := newConsumer(fb, testLog(t))
	c.Handle("regen:rpm", func(context.Context, Request) Result { return Later })

	ctx := context.Background()
	kinds := []string{"regen:rpm"}

	c.tick(ctx, kinds)

	if n := len(fb.finishedCalls()); n != 0 {
		t.Fatalf("finished %d requests before the work settled", n)
	}

	// The next poll renews the claim of the work in flight.
	c.tick(ctx, kinds)

	if fb.renewCalls() != 1 {
		t.Fatalf("renewals = %d, want 1", fb.renewCalls())
	}

	c.Settle("regen:rpm", "repo", true)
	c.Flush(ctx)

	calls := fb.finishedCalls()
	if len(calls) != 1 || !calls[0].upTo.Equal(at) {
		t.Fatalf("finished = %+v, want one call up to %v", calls, at)
	}

	// Settled work is no longer renewed.
	c.tick(ctx, kinds)

	if fb.renewCalls() != 1 {
		t.Fatalf("renewals after settling = %d, want still 1", fb.renewCalls())
	}
}

func TestConsumerGivingUpLeavesTheRequestForALaterClaim(t *testing.T) {
	fb := &fakeBackend{queue: []Request{{Kind: "regen:rpm", Key: "repo", RequestedAt: time.Now()}}}
	c := newConsumer(fb, testLog(t))
	c.Handle("regen:rpm", func(context.Context, Request) Result { return Later })

	ctx := context.Background()

	c.tick(ctx, []string{"regen:rpm"})
	c.Settle("regen:rpm", "repo", false)
	c.Flush(ctx)

	if n := len(fb.finishedCalls()); n != 0 {
		t.Fatalf("a request given up on was deleted (%d finish calls)", n)
	}

	c.tick(ctx, []string{"regen:rpm"})

	if fb.renewCalls() != 0 {
		t.Fatal("the claim of work that gave up is still renewed; it must lapse so another process can take over")
	}
}

func TestConsumerReleaseGivesBackGivenUpAndHeldClaims(t *testing.T) {
	at := time.Now()
	fb := &fakeBackend{queue: []Request{
		{Kind: "regen:rpm", Key: "gaveup", RequestedAt: at},
		{Kind: "regen:rpm", Key: "running", RequestedAt: at},
		{Kind: "regen:rpm", Key: "done", RequestedAt: at},
	}}
	c := newConsumer(fb, testLog(t))
	c.Handle("regen:rpm", func(context.Context, Request) Result { return Later })

	ctx := context.Background()

	c.tick(ctx, []string{"regen:rpm"})
	c.Settle("regen:rpm", "gaveup", false)
	c.Settle("regen:rpm", "done", true)
	c.Release(ctx)

	got := map[string]bool{}
	for _, r := range fb.releasedRefs() {
		got[r.key] = true
	}

	if !got["gaveup"] || !got["running"] || got["done"] || len(got) != 2 {
		t.Fatalf("released = %v, want gaveup and running only", got)
	}

	if n := len(fb.finishedCalls()); n != 1 {
		t.Fatalf("finished = %d, want 1 (the done request)", n)
	}
}

func TestConsumerRetryKeepsTheRequest(t *testing.T) {
	fb := &fakeBackend{queue: []Request{{Kind: "dtrack:push", Key: "a", RequestedAt: time.Now()}}}
	c := newConsumer(fb, testLog(t))
	c.Handle("dtrack:push", func(context.Context, Request) Result { return Retry })

	c.tick(context.Background(), []string{"dtrack:push"})
	c.Flush(context.Background())

	if n := len(fb.finishedCalls()); n != 0 {
		t.Fatalf("a request the worker could not take was deleted (%d finish calls)", n)
	}

	c.tick(context.Background(), []string{"dtrack:push"})

	if fb.renewCalls() != 0 {
		t.Fatal("the claim of a retried request is still renewed")
	}
}

func TestConsumerSettleOfAnUnclaimedKeyIsIgnored(t *testing.T) {
	fb := &fakeBackend{}
	c := newConsumer(fb, testLog(t))

	c.Settle("regen:rpm", "never-claimed", true)
	c.Flush(context.Background())

	if n := len(fb.finishedCalls()); n != 0 {
		t.Fatalf("finish called %d times for a key nobody claimed", n)
	}
}

func TestConsumerSettleCoversOnlyWhatWasClaimed(t *testing.T) {
	first := time.Now()
	fb := &fakeBackend{queue: []Request{{Kind: "regen:rpm", Key: "repo", RequestedAt: first}}}
	c := newConsumer(fb, testLog(t))
	c.Handle("regen:rpm", func(context.Context, Request) Result { return Later })

	c.tick(context.Background(), []string{"regen:rpm"})

	// The request is made again while the work runs; the store keeps the newer
	// row because the delete is bounded by what was claimed.
	c.Settle("regen:rpm", "repo", true)
	c.Flush(context.Background())

	calls := fb.finishedCalls()
	if len(calls) != 1 || !calls[0].upTo.Equal(first) {
		t.Fatalf("finished = %+v, want the delete bounded by the claimed request time %v", calls, first)
	}
}

func TestConsumerDropsARequestWhoseHandlerPanics(t *testing.T) {
	fb := &fakeBackend{queue: []Request{{Kind: "ai:analyze", Key: "poison", RequestedAt: time.Now()}}}
	c := newConsumer(fb, testLog(t))
	c.Handle("ai:analyze", func(context.Context, Request) Result { panic("bad payload") })

	c.tick(context.Background(), []string{"ai:analyze"})
	c.Flush(context.Background())

	if n := len(fb.finishedCalls()); n != 1 {
		t.Fatalf("finished %d requests, want the poison request deleted", n)
	}
}

func TestConsumerClaimsOnlyTheKindsItHandles(t *testing.T) {
	fb := &fakeBackend{}
	c := newConsumer(fb, testLog(t))
	c.poll = 5 * time.Millisecond
	c.Handle("vuln:scan", func(context.Context, Request) Result { return Done })
	c.Handle("regen:npm", func(context.Context, Request) Result { return Done })

	c.Start(context.Background())

	deadline := time.Now().Add(5 * time.Second)

	for {
		fb.mu.Lock()
		kinds := fb.kinds
		fb.mu.Unlock()

		if len(kinds) > 0 {
			if len(kinds) != 2 || kinds[0] != "regen:npm" || kinds[1] != "vuln:scan" {
				t.Fatalf("claimed kinds = %v, want [regen:npm vuln:scan]", kinds)
			}

			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the consumer never polled")
		}

		time.Sleep(time.Millisecond)
	}

	c.Stop()
}

func TestConsumerSurvivesAnUnavailableStore(t *testing.T) {
	fb := &fakeBackend{claimErr: errors.New("connection refused")}
	c := newConsumer(fb, testLog(t))
	c.Handle("regen:npm", func(context.Context, Request) Result { return Done })

	c.tick(context.Background(), []string{"regen:npm"})

	if !c.failing {
		t.Fatal("a failing claim must mark the consumer as failing")
	}

	fb.mu.Lock()
	fb.claimErr = nil
	fb.mu.Unlock()

	c.tick(context.Background(), []string{"regen:npm"})

	if c.failing {
		t.Fatal("a successful claim must clear the failing state")
	}
}

func TestConsumerStopWithoutStart(t *testing.T) {
	c := newConsumer(&fakeBackend{}, testLog(t))

	c.Stop()
	c.Stop()
	c.Start(context.Background())
	c.Stop()
}
