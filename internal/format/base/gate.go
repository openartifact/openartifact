// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import "context"

// Gate bounds how many callers run a memory-heavy section at once (whole
// documents read and re-encoded in memory). A nil Gate is unbounded.
type Gate chan struct{}

// NewGate returns a Gate admitting n concurrent holders (n < 1 means 1).
func NewGate(n int) Gate {
	return make(Gate, max(n, 1))
}

// Acquire waits for a slot and returns ctx's error if it ends first. Every
// successful Acquire must be paired with Release.
func (g Gate) Acquire(ctx context.Context) error {
	if g == nil {
		return nil
	}

	select {
	case g <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // plain cancellation
	}
}

// Release frees the slot taken by Acquire.
func (g Gate) Release() {
	if g != nil {
		<-g
	}
}
