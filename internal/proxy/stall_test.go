// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// An upstream that goes silent mid-body must fail the flight within the stall
// timeout, release the requests waiting on it and leave nothing cached.
func TestServe_StalledUpstreamFailsFlightAndWaiters(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	f.svc.WithUpstreamStallTimeout(300 * time.Millisecond)

	const key = "a/b/1.0/b-1.0.jar"

	const waiters = 4

	codes := make(chan int, waiters)

	var wg sync.WaitGroup

	for range waiters {
		wg.Go(func() { codes <- f.do(http.MethodGet, key).Code })
	}

	done := make(chan struct{})

	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("flight and waiters still blocked long after the stall timeout")
	}

	close(codes)

	for code := range codes {
		if code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", code)
		}
	}

	if e := f.db.entry("repo-1", key); e != nil {
		t.Fatalf("stalled download left a cache entry: %+v", e)
	}

	f.db.mu.Lock()
	blobs := len(f.db.blobs)
	f.db.mu.Unlock()

	if blobs != 0 {
		t.Fatalf("stalled download left %d blob rows", blobs)
	}
}
