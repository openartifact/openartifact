// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestPeerClientIsTraced: a call to a peer is a client span and carries the
// caller's traceparent.
func TestPeerClientIsTraced(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(telemetry.Install(tp))

	var traceparent string

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("Traceparent")
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(t.Context(), &Peer{BaseURL: srv.URL}, PeerSecrets{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	ctx, span := tp.Tracer("t").Start(t.Context(), "parent")
	defer span.End()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)

	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if traceparent == "" {
		t.Error("peer request carried no traceparent")
	}

	if n := len(rec.Ended()); n != 1 {
		t.Errorf("%d client spans recorded, want 1", n)
	}
}
