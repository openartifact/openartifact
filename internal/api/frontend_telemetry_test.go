// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	colllogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// collectorRequest is what the fake collector received.
type collectorRequest struct {
	path, auth, ctype, cookies string
	body                       []byte
}

// fakeCollector records the last request the relay forwarded and answers
// with status.
type fakeCollector struct {
	srv *httptest.Server

	mu     sync.Mutex
	status int
	last   *collectorRequest
}

func newFakeCollector(t *testing.T) *fakeCollector {
	t.Helper()

	c := &fakeCollector{status: http.StatusOK}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		c.mu.Lock()
		c.last = &collectorRequest{
			path: r.URL.Path, auth: r.Header.Get("Authorization"), ctype: r.Header.Get("Content-Type"),
			cookies: r.Header.Get("Cookie"), body: body,
		}
		status := c.status
		c.mu.Unlock()

		w.WriteHeader(status)
		_, _ = w.Write([]byte("export-response"))
	}))
	t.Cleanup(c.srv.Close)

	return c
}

// take returns and clears the last forwarded request (nil if none).
func (c *fakeCollector) take() *collectorRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	last := c.last
	c.last = nil

	return last
}

func (c *fakeCollector) answer(status int) {
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
}

func frontendConfig(endpoint string) telemetry.Config {
	cfg := telemetry.DefaultConfig()
	cfg.OTLP.Enabled = true
	cfg.OTLP.SampleRatio = 0.25
	cfg.OTLP.Headers = map[string]string{"Authorization": "Bearer collector-secret"}
	cfg.Frontend = telemetry.FrontendConfig{Enabled: true, Endpoint: endpoint}

	return cfg
}

func newFrontendRouter(cfg telemetry.Config, reg *metrics.Registry, p *auth.Principal) http.Handler {
	h := NewFrontendTelemetryHandler(cfg, reg, nil)
	h.limit = newUserLimiter(1e6, 1e6) // the rate limit has its own test

	return routerFor(h, p)
}

// routerFor mounts h behind a middleware that authenticates every request as p.
func routerFor(h *FrontendTelemetryHandler, p *auth.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), p)))
		})
	})
	h.RegisterAuthed(r)

	return r
}

func relayRequest(signal, contentType string, body []byte) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/telemetry/"+signal, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Cookie", "oa_session=browser-session")

	return req
}

// TestFrontendTelemetryRelayMatrix is the status matrix of the relay. The
// RBAC layer answers 401 to anonymous callers before the handler runs; the
// handler repeats the check so it never relies on the route's position.
func TestFrontendTelemetryRelayMatrix(t *testing.T) {
	collector := newFakeCollector(t)
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	id := "user-1"
	deployToken := &auth.Principal{UserID: &id, Username: "ci", Credential: auth.CredentialBearerToken, TokenKind: auth.TokenKindAccess}
	anon := &auth.Principal{IsAnonymous: true, Credential: auth.CredentialNone}
	tracesBody, logsBody := tracePayload(1), logsPayload(1)

	tests := []struct {
		name     string
		cfg      telemetry.Config
		p        *auth.Principal
		req      *http.Request
		want     int
		counted  string
		forwards bool
	}{
		{
			"relayed",
			frontendConfig(collector.srv.URL),
			sessionPrincipal("user-1"),
			relayRequest("traces", otlpProtobuf, tracesBody),
			200,
			relayRelayed,
			true,
		},
		{
			"unauthenticated",
			frontendConfig(collector.srv.URL),
			anon,
			relayRequest("logs", otlpProtobuf, logsBody),
			401,
			relayRejected,
			false,
		},
		{
			"deploy token",
			frontendConfig(collector.srv.URL),
			deployToken,
			relayRequest("logs", otlpProtobuf, logsBody),
			403,
			relayRejected,
			false,
		},
		{
			"body over cap", frontendConfig(collector.srv.URL), sessionPrincipal("user-1"),
			relayRequest("logs", otlpProtobuf, bytes.Repeat([]byte{1}, frontendTelemetryMaxBody+1)), 413, relayRejected, false,
		},
		{
			"wrong content type",
			frontendConfig(collector.srv.URL),
			sessionPrincipal("user-1"),
			relayRequest("logs", "application/json", logsBody),
			415,
			relayRejected,
			false,
		},
		{
			"metrics signal",
			frontendConfig(collector.srv.URL),
			sessionPrincipal("user-1"),
			relayRequest("metrics", otlpProtobuf, logsBody),
			404,
			"",
			false,
		},
		{
			"feature off",
			telemetry.DefaultConfig(),
			sessionPrincipal("user-1"),
			relayRequest("logs", otlpProtobuf, logsBody),
			404,
			"",
			false,
		},
		{
			"collector down",
			frontendConfig(downURL),
			sessionPrincipal("user-1"),
			relayRequest("logs", otlpProtobuf, logsBody),
			502,
			relayFailed,
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			collector.take()
			reg := metrics.NewRegistryOn(prometheus.NewRegistry())

			rec := httptest.NewRecorder()
			newFrontendRouter(tc.cfg, reg, tc.p).ServeHTTP(rec, tc.req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}

			signal := strings.TrimPrefix(tc.req.URL.Path, "/api/v1/telemetry/")

			for _, result := range []string{relayRelayed, relayRejected, relayFailed} {
				want := 0.0
				if result == tc.counted {
					want = 1
				}

				if got := testutil.ToFloat64(reg.FrontendTelemetryRequestsTotal.WithLabelValues(signal, result)); got != want {
					t.Errorf("counter{%s,%s} = %v, want %v", signal, result, got, want)
				}
			}

			if forwarded := collector.take() != nil; forwarded != tc.forwards {
				t.Fatalf("forwarded = %v, want %v", forwarded, tc.forwards)
			}
		})
	}
}

// TestFrontendTelemetryRelayForwardsValidated checks what reaches the
// collector: the validated export as sent, the OTLP/HTTP signal path, the
// configured headers, and never the browser's cookies.
func TestFrontendTelemetryRelayForwardsValidated(t *testing.T) {
	collector := newFakeCollector(t)
	payload, tracesBody := logsPayload(3), tracePayload(3)

	rec := httptest.NewRecorder()
	newFrontendRouter(frontendConfig(collector.srv.URL+"/otlp/"), nil, sessionPrincipal("user-1")).
		ServeHTTP(rec, relayRequest("logs", otlpProtobuf+"; charset=binary", payload))

	if rec.Code != http.StatusOK || rec.Body.String() != "export-response" || rec.Header().Get("Content-Type") != otlpProtobuf {
		t.Fatalf("answer = %d %q (%s)", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}

	got := collector.take()
	if got == nil {
		t.Fatal("nothing forwarded")
	}

	if got.path != "/otlp/v1/logs" || got.ctype != otlpProtobuf {
		t.Errorf("collector got path %q, body %q, type %q", got.path, got.body, got.ctype)
	}

	var forwarded colllogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(got.body, &forwarded); err != nil {
		t.Fatalf("forwarded body is not an OTLP logs request: %v", err)
	}

	if n := len(forwarded.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()); n != 3 {
		t.Errorf("forwarded %d log records, want the 3 sent", n)
	}

	if got.auth != "Bearer collector-secret" || got.cookies != "" {
		t.Errorf("collector got Authorization %q, Cookie %q", got.auth, got.cookies)
	}

	collector.answer(http.StatusBadRequest)
	rec = httptest.NewRecorder()
	newFrontendRouter(
		frontendConfig(collector.srv.URL),
		nil,
		sessionPrincipal("user-1"),
	).ServeHTTP(rec, relayRequest("traces", otlpProtobuf, tracesBody))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("collector 400 answered %d, want 400", rec.Code)
	}

	collector.answer(http.StatusServiceUnavailable)
	rec = httptest.NewRecorder()
	newFrontendRouter(
		frontendConfig(collector.srv.URL),
		nil,
		sessionPrincipal("user-1"),
	).ServeHTTP(rec, relayRequest("traces", otlpProtobuf, tracesBody))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("collector 503 answered %d, want 502", rec.Code)
	}
}

// TestFrontendTelemetryRelayRefusesRedirects: a 307 from the collector must not replay the body and the
// collector headers to the Location; it answers 502 like any other failing collector.
func TestFrontendTelemetryRelayRefusesRedirects(t *testing.T) {
	var hits atomic.Int32

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	rec := httptest.NewRecorder()
	newFrontendRouter(frontendConfig(redirector.URL), nil, sessionPrincipal("user-1")).
		ServeHTTP(rec, relayRequest("traces", otlpProtobuf, tracePayload(1)))

	if rec.Code != http.StatusBadGateway || hits.Load() != 0 {
		t.Errorf("answered %d with %d requests at the redirect target, want 502 and none", rec.Code, hits.Load())
	}
}

// TestFrontendTelemetryRelayBusy: with frontendTelemetryMaxInflight forwards stuck at the collector, the next
// request is answered 503 with Retry-After at once, and the relay recovers when they finish.
func TestFrontendTelemetryRelayBusy(t *testing.T) {
	gate := make(chan struct{})

	var (
		entered atomic.Int32
		release sync.Once
	)

	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered.Add(1)
		<-gate
	}))
	t.Cleanup(collector.Close)
	t.Cleanup(func() { release.Do(func() { close(gate) }) }) // runs first: Close waits for the requests

	router := newFrontendRouter(frontendConfig(collector.URL), nil, sessionPrincipal("user-1"))
	tracesBody, logsBody := tracePayload(1), logsPayload(1)
	codes := make([]int, frontendTelemetryMaxInflight)

	var wg sync.WaitGroup

	for i := range codes {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, relayRequest("traces", otlpProtobuf, tracesBody))
			codes[i] = rec.Code
		})
	}

	deadline := time.Now().Add(10 * time.Second)
	for entered.Load() < frontendTelemetryMaxInflight && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if n := entered.Load(); n != frontendTelemetryMaxInflight {
		t.Fatalf("%d forwards reached the collector, want %d", n, frontendTelemetryMaxInflight)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, relayRequest("logs", otlpProtobuf, logsBody))

	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over the cap = %d (Retry-After %q), want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}

	release.Do(func() { close(gate) })
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("forward %d answered %d, want 200", i, code)
		}
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, relayRequest("traces", otlpProtobuf, tracesBody))

	if rec.Code != http.StatusOK {
		t.Errorf("after the forwards finished = %d, want 200", rec.Code)
	}
}

func TestFrontendTelemetryConfigEndpoint(t *testing.T) {
	get := func(cfg telemetry.Config) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/telemetry/frontend", nil)
		newFrontendRouter(cfg, nil, sessionPrincipal("user-1")).ServeHTTP(rec, req)

		return rec
	}

	if rec := get(telemetry.DefaultConfig()); rec.Code != http.StatusNotFound {
		t.Errorf("off: status %d, want 404", rec.Code)
	}

	onWithoutOTLP := frontendConfig("http://localhost:4318")
	onWithoutOTLP.OTLP.Enabled = false

	if rec := get(onWithoutOTLP); rec.Code != http.StatusNotFound {
		t.Errorf("frontend on, OTLP off: status %d, want 404", rec.Code)
	}

	rec := get(frontendConfig("http://localhost:4318"))
	if rec.Code != http.StatusOK {
		t.Fatalf("on: status %d", rec.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if got["enabled"] != true || got["sample_ratio"] != 0.25 || len(got) != 2 {
		t.Errorf("config = %v, want enabled and sample_ratio 0.25 only", got)
	}
}
