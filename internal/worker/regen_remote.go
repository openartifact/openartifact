// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/workq"
)

// regenKindPrefix prefixes the format of a metadata regeneration request kind,
// e.g. "regen:npm".
const regenKindPrefix = "regen:"

const (
	// forwardTimeout bounds writing one request to the database.
	forwardTimeout = 5 * time.Second
	// forwardIdleWindow is how long notifyIfIdle on a forwarding queue leaves a
	// key alone after writing a request for it.
	forwardIdleWindow = 10 * time.Second
	// forwardedLimit is how many keys a forwarding queue remembers before it
	// forgets the ones outside the window.
	forwardedLimit = 1024
)

// RegenRemote is a metadata worker that can hand its regenerations to a worker
// process (server.role): a web process calls ForwardTo, so NotifyStale writes a
// request to the database instead of scheduling the regeneration in memory, and
// a process that runs the workers calls ServeFrom, so the requests it claims
// are scheduled on this worker.
type RegenRemote interface {
	ForwardTo(s *workq.Store)
	ServeFrom(c *workq.Consumer)
}

// regenCodec turns the key of a queue into the key and payload of a worker
// request, and back.
type regenCodec[K comparable] struct {
	encode func(K) (key string, payload []byte)
	decode func(workq.Request) (K, error)
}

// stringRegenCodec is the codec of the queues keyed by a repository or package
// id: the id is the key of the request.
func stringRegenCodec() regenCodec[string] {
	return regenCodec[string]{
		encode: func(id string) (string, []byte) { return id, nil },
		decode: func(r workq.Request) (string, error) { return r.Key, nil },
	}
}

func (q *regenQueue[K]) setCodec(c regenCodec[K]) { q.codec = c }

// forwardTo turns the queue into the front of a worker process: Notify writes a
// request to s, and nothing is scheduled here. Configure before the first
// Notify.
func (q *regenQueue[K]) forwardTo(s *workq.Store) {
	q.fwd = s
	q.forwarded = make(map[K]time.Time)
}

// forward writes the request for key. With onlyIfNew it leaves the key alone
// when a request for it was written within forwardIdleWindow or is waiting.
func (q *regenQueue[K]) forward(key K, onlyIfNew bool) {
	q.mu.Lock()

	if q.closing {
		q.mu.Unlock()
		q.log.Warnw("metadata regeneration dropped: worker is shutting down", "format", q.format, "key", fmt.Sprint(key))

		return
	}

	if onlyIfNew {
		now := time.Now()

		if at, ok := q.forwarded[key]; ok && now.Sub(at) < forwardIdleWindow {
			q.mu.Unlock()
			return
		}

		q.rememberForwardedLocked(key, now)
	}

	q.mu.Unlock()

	kind := regenKindPrefix + q.format
	id, payload := q.codec.encode(key)

	ctx, cancel := context.WithTimeout(context.Background(), forwardTimeout)
	defer cancel()

	put := q.fwd.Put
	if onlyIfNew {
		put = q.fwd.PutIfAbsent
	}

	if err := put(ctx, kind, id, payload); err != nil {
		q.log.Errorw("metadata regeneration could not be queued for the worker; the metadata stays stale until the next change",
			"format", q.format, "key", fmt.Sprint(key), "error", err)
	}
}

func (q *regenQueue[K]) rememberForwardedLocked(key K, now time.Time) {
	if len(q.forwarded) >= forwardedLimit {
		for k, at := range q.forwarded {
			if now.Sub(at) >= forwardIdleWindow {
				delete(q.forwarded, k)
			}
		}
	}

	q.forwarded[key] = now
}

// serveFrom registers the queue as the handler of its format's requests on c:
// each claimed request is passed to notify (the worker's NotifyStale, which may
// do more than queue), and is deleted once the regeneration it scheduled
// finished. Configure before the first Notify.
func (q *regenQueue[K]) serveFrom(c *workq.Consumer, notify func(K)) {
	kind := regenKindPrefix + q.format

	c.Handle(kind, func(_ context.Context, r workq.Request) workq.Result {
		key, err := q.codec.decode(r)
		if err != nil {
			q.log.Errorw("dropping a malformed metadata regeneration request", "format", q.format, "key", r.Key, "error", err)
			return workq.Done
		}

		notify(key)

		return workq.Later
	})

	q.settle = func(key K, done bool) {
		id, _ := q.codec.encode(key)
		c.Settle(kind, id, done)
	}
}
