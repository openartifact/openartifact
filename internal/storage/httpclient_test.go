// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestStreamingHTTPClientIsTraced: the Azure and OCI blob client makes client
// spans and propagates the caller's traceparent.
func TestStreamingHTTPClientIsTraced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(telemetry.Install(tp))

	var traceparent string

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("Traceparent")
	}))
	t.Cleanup(srv.Close)

	ctx, span := tp.Tracer("t").Start(t.Context(), "parent")
	defer span.End()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/blob?sig=secret", nil)

	resp, err := newStreamingHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if traceparent == "" {
		t.Error("blob request carried no traceparent")
	}

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("%d client spans recorded, want 1", len(ended))
	}

	for _, kv := range ended[0].Attributes() {
		if kv.Value.String() == "/blob" || kv.Value.String() == "sig=secret" {
			t.Errorf("span attribute %s carries the path or query", kv.Key)
		}
	}
}
