// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dtrack

import (
	"context"
	"encoding/json"
	"time"

	"github.com/openartifact/openartifact/internal/workq"
)

// RequestKind is the kind of the worker request that asks for a push.
const RequestKind = "dtrack:push"

// forwardTimeout bounds writing one request to the database.
const forwardTimeout = 5 * time.Second

// ForwardTo turns the pusher into the front of a worker process
// (server.role=web): Enqueue writes a worker request to s, and nothing is
// pushed here. Call it before the first Enqueue.
func (p *Pusher) ForwardTo(s *workq.Store) { p.fwd = s }

// ServeFrom makes the pusher push what the requests claimed by c ask for. The
// pusher must be started.
func (p *Pusher) ServeFrom(c *workq.Consumer) { c.Handle(RequestKind, p.handleRequest) }

func (p *Pusher) handleRequest(_ context.Context, r workq.Request) workq.Result {
	var j Job
	if err := json.Unmarshal(r.Payload, &j); err != nil {
		p.log.Errorw("dependency-track: dropping a malformed push request", "key", r.Key, "error", err)
		return workq.Done
	}

	// The pusher de-duplicates and runs the push in memory; the request is done
	// once the job is accepted.
	if !p.Enqueue(j) {
		return workq.Retry
	}

	return workq.Done
}

// forward writes the push request of j; it is false when it could not be
// written.
func (p *Pusher) forward(j Job) bool {
	payload, err := json.Marshal(j)
	if err != nil {
		p.log.Errorw("dependency-track: encoding a push request failed", "error", err)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), forwardTimeout)
	defer cancel()

	if err := p.fwd.Put(ctx, RequestKind, j.RepoID+"/"+j.ArtifactID, payload); err != nil {
		p.log.Errorw(
			"dependency-track: queueing a push for the worker failed",
			"repository_id",
			j.RepoID,
			"artifact",
			j.ArtifactID,
			"error",
			err,
		)
		return false
	}

	return true
}
