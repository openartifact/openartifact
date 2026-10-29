// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"time"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/internal/workq"
)

// server.role (docs/operations/scaling.md): `web` serves the API and starts no
// background work, `worker` runs the background work and serves only the
// operational endpoints, `all` does both. Everything is built whatever the
// role, so a web process can still call a worker (to queue its work) and a
// worker can still run what a web process asked for; the role decides what
// starts and what is routed.

const (
	// workerPollInterval is how often a worker process looks for requests.
	workerPollInterval = time.Second
	// allPollInterval is how often a process serving both roles does: it only
	// has requests to take when a separate web process exists, so it does not
	// poll as often as a dedicated worker.
	allPollInterval = 10 * time.Second
	// workerWebhookPoll is how often a worker claims the webhook deliveries
	// that web processes recorded; the default of 30 s only serves retries.
	workerWebhookPoll = 5 * time.Second
)

// start runs fn, which starts one background worker, unless this process only
// serves the API.
func (a *app) start(fn func()) {
	if a.cfg.Server.RunsWorkers() {
		fn()
	}
}

// background runs fn under the server lifecycle, unless this process only
// serves the API.
func (a *app) background(fn func(ctx context.Context)) {
	if a.cfg.Server.RunsWorkers() {
		a.tasks.Go(fn)
	}
}

// setupWorkRequests creates the store the processes hand work through and,
// where this process runs the workers, the consumer that takes the requests.
// Workers register their handlers on the consumer as they are built; it starts
// once every one has (startWorkRequests).
func (a *app) setupWorkRequests() {
	w := &a.workers
	w.queue = workq.NewStore(a.db.GetDB())

	if !a.cfg.Server.RunsWorkers() {
		return
	}

	poll := allPollInterval
	if a.cfg.Server.Role == config.RoleWorker {
		poll = workerPollInterval
	}

	w.requests = workq.NewConsumer(w.queue, a.log).WithPollInterval(poll)
}

// linkRegens makes the metadata workers write a request for a worker process
// instead of regenerating here; on a process that runs the workers they serve
// the requests instead.
func (a *app) linkRegens() {
	w := &a.workers

	remotes := []worker.RegenRemote{w.rpm, w.deb, w.pacman, w.apk, w.groupMeta, w.maven, w.npm}

	for _, r := range remotes {
		if w.requests == nil {
			r.ForwardTo(w.queue)
		} else {
			r.ServeFrom(w.requests)
		}
	}
}

// stopWorkRequests stops taking requests; the work already taken keeps running
// until the workers stop.
func (a *app) stopWorkRequests() {
	if c := a.workers.requests; c != nil {
		c.Stop()
	}
}

// flushWorkRequests gives back the requests whose work did not finish, so
// another worker takes them over at once instead of after the lease lapses.
func (a *app) flushWorkRequests() {
	c := a.workers.requests
	if c == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), backgroundDrainTimeout)
	defer cancel()

	c.Flush(ctx)
}

// startWorkRequests starts taking the requests of the web processes, after
// every handler is registered.
func (a *app) startWorkRequests() {
	if c := a.workers.requests; c != nil {
		c.Start(context.Background())
	}
}
