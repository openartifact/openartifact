// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A burst of parallel upstream fetches must leave its connections idle for
// reuse; with the default of 2 idle connections per host the next burst
// reconnects (a TLS handshake each) for all but two of them.
func TestSafeHTTPClientReusesBurstConnections(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	const burst = 24

	var (
		conns   atomic.Int32
		arrived sync.WaitGroup
	)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived.Done()
		arrived.Wait() // hold every request open until the whole burst is in flight
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()

	defer srv.Close()

	client := SafeHTTPClient(10 * time.Second)

	round := func() {
		arrived.Add(burst)

		var wg sync.WaitGroup

		for range burst {
			wg.Go(func() {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
				if err != nil {
					t.Error(err)
					return
				}

				resp, err := client.Do(req)
				if err != nil {
					t.Error(err)
					return
				}

				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			})
		}

		wg.Wait()
	}

	round()

	first := conns.Load()

	round()

	if extra := conns.Load() - first; extra > 0 {
		t.Errorf("second burst opened %d new connections, want 0 (first burst opened %d)", extra, first)
	}
}
