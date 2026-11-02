// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"sync"
)

// uploadLocks serialises the requests one process serves on one upload
// session: PATCH, PUT (complete) and DELETE (cancel) never interleave, whichever
// order they arrive in, so a completion hashes and stores exactly the chunks it
// read. Requests on different replicas are not serialised by it; the session's
// offset is compare-and-set in the database, so a chunk that loses the race is
// refused, and a chunk appended after a completion read the session's chunks is
// not part of the blob the completion verified.
type uploadLocks struct {
	mu sync.Mutex
	m  map[string]*uploadLock
}

type uploadLock struct {
	held chan struct{} // capacity 1: a token in the channel means "locked"
	refs int
}

// lock waits for the session's turn (or ctx) and returns the release func.
func (l *uploadLocks) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()

	if l.m == nil {
		l.m = make(map[string]*uploadLock)
	}

	e := l.m[key]
	if e == nil {
		e = &uploadLock{held: make(chan struct{}, 1)}
		l.m[key] = e
	}

	e.refs++
	l.mu.Unlock()

	release := func() {
		l.mu.Lock()
		defer l.mu.Unlock()

		e.refs--
		if e.refs == 0 {
			delete(l.m, key)
		}
	}

	select {
	case e.held <- struct{}{}:
		return func() {
			<-e.held

			release()
		}, nil
	case <-ctx.Done():
		release()

		return nil, ctx.Err()
	}
}
