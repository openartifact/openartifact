// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// Tag listings are uncached live data, but they still count against the upstream fetch gate: with 2
// slots and 5 concurrent client requests the upstream never sees more than 2 listing requests at once.
func TestOCI_TagListsAreBoundedByTheFetchGate(t *testing.T) {
	const (
		limit   = 2
		clients = 5
	)

	f := newOCIFixture(t)
	f.reg.tags["alpine"] = []string{"3.19", "latest"}
	f.svc.fetches = base.NewGate(limit)

	var cur, peak atomic.Int32

	arrived := make(chan struct{}, clients)
	release := make(chan struct{}, clients)
	inner := f.reg.srv.Config.Handler

	f.reg.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.URL.Path != "/v2/alpine/tags/list" {
			inner.ServeHTTP(w, r)
			return
		}

		n := cur.Add(1)

		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}

		arrived <- struct{}{}

		<-release

		cur.Add(-1)
		inner.ServeHTTP(w, r)
	})

	codes := make(chan int, clients)

	var wg sync.WaitGroup

	for range clients {
		wg.Go(func() {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/hub/alpine/tags/list", nil)
			w := httptest.NewRecorder()
			f.svc.ServeOCI(w, req, f.repo, ocipath.Parse("hub/alpine/tags/list"))
			codes <- w.Code
		})
	}

	for range limit {
		<-arrived
	}

	if got := len(f.svc.fetches); got != limit {
		t.Fatalf("slots held with %d listings in flight = %d", limit, got)
	}

	for i := range clients {
		release <- struct{}{}

		if i < clients-limit {
			<-arrived
		}
	}

	wg.Wait()
	close(codes)

	for c := range codes {
		if c != http.StatusOK {
			t.Errorf("status = %d, want 200", c)
		}
	}

	if got := peak.Load(); got > limit {
		t.Errorf("peak concurrent upstream listings = %d, want <= %d", got, limit)
	}

	if got := len(f.svc.fetches); got != 0 {
		t.Errorf("%d fetch slot(s) leaked", got)
	}
}
