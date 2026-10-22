// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"

	"github.com/openartifact/openartifact/internal/format/base"
)

// maxRequestBytes caps what one group request may hold in memory across all
// its members' captures: the per-member limits alone would let N members each
// use their maximum. A capture that would exceed it counts as a failed member.
const maxRequestBytes = 256 << 20

// mergeSession is one group request's claim on the Handler's merge gate and
// its capture budget. The gate slot is taken lazily, when the first member
// response starts being captured, not up front: a member (a proxy) may spend a
// long time on its upstream before it has anything to write, and must not hold
// a slot meanwhile. At most one slot is held per request; release frees it
// (idempotent), so handlers defer it and also call it before streaming the
// merged answer to a possibly slow client. A session is used by one goroutine.
type mergeSession struct {
	gate base.Gate
	ctx  context.Context //nolint:containedctx // scoped to one request, never stored beyond it
	held bool
	used int
}

func (h *Handler) newSession(ctx context.Context) *mergeSession {
	return &mergeSession{gate: h.packuments, ctx: ctx}
}

// capture returns a capture writer for one member, limited to limit bytes and
// bound to the session's gate slot and budget.
func (s *mergeSession) capture(limit int) *captureWriter {
	cw := newCaptureWriter(limit)
	cw.sess = s

	return cw
}

// acquire takes the request's gate slot unless it already holds it.
func (s *mergeSession) acquire() error {
	if s.held {
		return nil
	}

	if err := s.gate.Acquire(s.ctx); err != nil {
		return err //nolint:wrapcheck // plain cancellation
	}

	s.held = true

	return nil
}

// reserve counts n more captured bytes against the request budget and reports
// whether they fit.
func (s *mergeSession) reserve(n int) bool {
	if s.used+n > maxRequestBytes {
		return false
	}

	s.used += n

	return true
}

// release frees the gate slot if held.
func (s *mergeSession) release() {
	if s.held {
		s.held = false
		s.gate.Release()
	}
}
