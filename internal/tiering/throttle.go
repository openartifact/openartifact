// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/bgtask"
)

// throttleChunk caps one Read so the pacing stays smooth for slow rates.
const throttleChunk = 64 << 10

// Throttle limits the aggregate throughput of every reader built from it to a
// byte rate. All movers share one Throttle, so the cap holds however many
// blobs are copied concurrently. A nil or zero-rate Throttle is unlimited.
type Throttle struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	next   time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	nowFn  func() time.Time
	waited time.Duration
}

// NewThrottle creates a limiter of bytesPerSec; zero or negative means unlimited.
func NewThrottle(bytesPerSec int64) *Throttle {
	return &Throttle{rate: float64(bytesPerSec), sleep: bgtask.Sleep, nowFn: time.Now}
}

// Wait blocks until n bytes may pass.
func (t *Throttle) Wait(ctx context.Context, n int) error {
	if t == nil || t.rate <= 0 || n <= 0 {
		return ctx.Err()
	}

	cost := time.Duration(float64(n) / t.rate * float64(time.Second))

	t.mu.Lock()

	now := t.nowFn()
	start := t.next

	if start.Before(now) {
		start = now
	}

	t.next = start.Add(cost)
	wait := start.Sub(now)
	t.waited += wait
	t.mu.Unlock()

	if wait <= 0 {
		return ctx.Err()
	}

	if err := t.sleep(ctx, wait); err != nil {
		return fmt.Errorf("throttle wait: %w", err)
	}

	return nil
}

// Reader wraps r so that reading it consumes the throttle's budget.
func (t *Throttle) Reader(ctx context.Context, r io.Reader) io.Reader {
	if t == nil || t.rate <= 0 {
		return r
	}

	return &throttledReader{ctx: ctx, t: t, r: r}
}

type throttledReader struct {
	ctx context.Context //nolint:containedctx // the reader lives for one blob copy
	t   *Throttle
	r   io.Reader
}

func (tr *throttledReader) Read(p []byte) (int, error) {
	if len(p) > throttleChunk {
		p = p[:throttleChunk]
	}

	n, err := tr.r.Read(p)
	if n > 0 {
		if werr := tr.t.Wait(tr.ctx, n); werr != nil {
			return n, werr
		}
	}

	return n, err //nolint:wrapcheck // io.Reader contract: io.EOF must pass through unwrapped
}
