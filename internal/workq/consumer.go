// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package workq

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

// Consumer defaults.
const (
	// DefaultPollInterval is how often a Consumer looks for new requests. A
	// request waits that long, on top of the debounce of the worker it feeds.
	DefaultPollInterval = time.Second
	// DefaultLease is how long a claim holds without being renewed; the claim of
	// a process that died is taken over after it.
	DefaultLease = 30 * time.Second
	// DefaultBatch is the most requests claimed per poll.
	DefaultBatch = 100
)

// Result is what a Handler says about a request it was handed.
type Result int

const (
	// Done: the handler did all it will do with the request; delete it now.
	Done Result = iota
	// Later: the work was handed to a worker that calls Consumer.Settle when it
	// finishes; the request is deleted then, and is kept (and its claim renewed)
	// until then.
	Later
	// Retry: the worker cannot take the work now (its queue is full); keep the
	// request, and claim it again once the lease of this claim lapses.
	Retry
)

// Handler starts the work a request asks for. It runs on the consumer's poll
// loop: it must hand the work to a worker and return, not do it.
type Handler func(ctx context.Context, r Request) Result

// backend is the part of Store a Consumer uses.
type backend interface {
	claim(ctx context.Context, kinds []string, limit int, lease time.Duration) ([]Request, error)
	renew(ctx context.Context, held []ref, lease time.Duration) error
	finish(ctx context.Context, r ref, upTo time.Time) error
}

// settlement is a Settle call waiting for the poll loop.
type settlement struct {
	ref  ref
	done bool
}

// finished is a settlement matched with its claim: the work that is over, and
// the newest request it covers.
type finished struct {
	ref  ref
	upTo time.Time
	done bool
}

// Consumer claims the requests of the kinds it has a Handler for and hands them
// to the process's workers. Register handlers before Start.
type Consumer struct {
	store backend
	log   *logger.Logger

	poll  time.Duration
	lease time.Duration
	batch int

	handlers map[string]Handler

	mu sync.Mutex
	// held are the claimed requests whose work is not finished, with the latest
	// RequestedAt claimed: what a finished work covers.
	held    map[ref]time.Time
	settled []settlement

	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	// failing is true while the store keeps failing, so one outage logs once.
	failing bool
}

// NewConsumer returns a Consumer reading store with the default timings.
func NewConsumer(store *Store, log *logger.Logger) *Consumer {
	return newConsumer(store, log)
}

func newConsumer(store backend, log *logger.Logger) *Consumer {
	return &Consumer{
		store: store, log: log,
		poll: DefaultPollInterval, lease: DefaultLease, batch: DefaultBatch,
		handlers: map[string]Handler{}, held: map[ref]time.Time{},
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

// WithPollInterval sets how often the consumer looks for requests. Call before
// Start.
func (c *Consumer) WithPollInterval(d time.Duration) *Consumer {
	c.poll = d
	return c
}

// Handle registers the handler of a kind. Call before Start.
func (c *Consumer) Handle(kind string, h Handler) { c.handlers[kind] = h }

// Start begins polling; it runs until Stop or ctx ends. Only the first call
// counts.
func (c *Consumer) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		loopCtx, cancel := context.WithCancel(ctx)
		c.cancel = cancel

		go c.loop(loopCtx)
	})
}

// Stop ends the polling, so no new request is claimed, and waits for the loop.
// Work already handed over runs on; its Settle calls are applied by Flush. Safe
// without Start.
func (c *Consumer) Stop() {
	c.stopOnce.Do(func() {
		// Never started: nothing will close done, and a later Start must not.
		c.startOnce.Do(func() { close(c.done) })

		if c.cancel != nil {
			c.cancel()
		}
	})

	<-c.done
}

// Settle tells the consumer that the work of (kind, key) it handed over with
// Later is finished (done), or gave up and is to be retried by a later claim
// (not done). It never blocks and is safe to call under a worker's own lock.
// A key that no claim holds is ignored.
func (c *Consumer) Settle(kind, key string, done bool) {
	c.mu.Lock()
	c.settled = append(c.settled, settlement{ref: ref{kind, key}, done: done})
	c.mu.Unlock()

	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Flush applies the Settle calls made so far. Stop first; call it again after
// the workers drained to settle what they finished during shutdown.
func (c *Consumer) Flush(ctx context.Context) {
	c.mu.Lock()
	pending := c.settled
	c.settled = nil

	var out []finished

	for _, s := range pending {
		upTo, ok := c.held[s.ref]
		if !ok {
			continue
		}

		delete(c.held, s.ref)

		out = append(out, finished{ref: s.ref, upTo: upTo, done: s.done})
	}
	c.mu.Unlock()

	for _, f := range out {
		if !f.done {
			continue
		}

		if err := c.store.finish(ctx, f.ref, f.upTo); err != nil {
			c.log.Warnw("worker request: could not delete the finished request; it will be handled again",
				"kind", f.ref.kind, "key", f.ref.key, "error", err)
		}
	}
}

func (c *Consumer) loop(ctx context.Context) {
	defer close(c.done)

	kinds := make([]string, 0, len(c.handlers))
	for k := range c.handlers {
		kinds = append(kinds, k)
	}

	slices.Sort(kinds)

	t := time.NewTicker(c.poll)
	defer t.Stop()

	for {
		c.tick(ctx, kinds)

		if !c.wait(ctx, t) {
			return
		}
	}
}

// wait blocks until the next poll, applying finished work as it is reported; it
// is false once ctx ends.
func (c *Consumer) wait(ctx context.Context, t *time.Ticker) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-c.wake:
			c.Flush(ctx)
		case <-t.C:
			return true
		}
	}
}

// tick applies the finished work, renews the claims in flight and claims what
// came in.
func (c *Consumer) tick(ctx context.Context, kinds []string) {
	c.Flush(ctx)

	err := c.renewHeld(ctx)
	if err == nil {
		err = c.claimAndHandle(ctx, kinds)
	}

	switch {
	case err != nil && ctx.Err() == nil && !c.failing:
		c.failing = true

		c.log.Warnw("worker requests: the queue is unavailable; retrying", "error", err)
	case err == nil && c.failing:
		c.failing = false

		c.log.Infow("worker requests: the queue is available again")
	}
}

func (c *Consumer) renewHeld(ctx context.Context) error {
	c.mu.Lock()
	held := make([]ref, 0, len(c.held))
	for r := range c.held {
		held = append(held, r)
	}
	c.mu.Unlock()

	if len(held) == 0 {
		return nil
	}

	return c.store.renew(ctx, held, c.lease)
}

func (c *Consumer) claimAndHandle(ctx context.Context, kinds []string) error {
	if len(kinds) == 0 {
		return nil
	}

	reqs, err := c.store.claim(ctx, kinds, c.batch, c.lease)
	if err != nil {
		return err
	}

	for _, r := range reqs {
		c.handle(ctx, r)
	}

	return nil
}

func (c *Consumer) handle(ctx context.Context, r Request) {
	k := ref{r.Kind, r.Key}

	// Held before the handler runs: work that finishes at once settles a key
	// that must already be held.
	c.mu.Lock()
	if r.RequestedAt.After(c.held[k]) {
		c.held[k] = r.RequestedAt
	}
	c.mu.Unlock()

	switch c.run(ctx, r) {
	case Later:
	case Retry:
		c.Settle(r.Kind, r.Key, false)
	case Done:
		c.Settle(r.Kind, r.Key, true)
	}
}

// run calls the handler of the request. A handler that panics is a bug the
// request must not keep repeating: it is logged and the request dropped.
func (c *Consumer) run(ctx context.Context, r Request) Result {
	res := Done // what a panicking handler leaves

	func() {
		defer func() {
			if p := recover(); p != nil {
				c.log.Errorw("worker request: handler panicked; dropping the request",
					"kind", r.Kind, "key", r.Key, "panic", p)
			}
		}()

		res = c.handlers[r.Kind](ctx, r)
	}()

	return res
}
