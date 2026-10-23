// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const (
	incomingTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	incomingSpanID  = "00f067aa0ba902b7"
	traceparent     = "00-" + incomingTraceID + "-" + incomingSpanID + "-01"
)

// install activates telemetry with an in-memory recorder for the test.
func install(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	uninstall := Install(tp)

	t.Cleanup(func() {
		uninstall()
		_ = tp.Shutdown(context.Background())
	})

	return rec
}

func spanByKind(t *testing.T, rec *tracetest.SpanRecorder, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	t.Helper()

	for _, s := range rec.Ended() {
		if s.SpanKind() == kind {
			return s
		}
	}

	t.Fatalf("no %v span recorded", kind)

	return nil
}

func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[string(kv.Key)] = kv.Value
	}

	return m
}

func serve(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	return rr
}

func routerWithMiddleware() http.Handler {
	r := chi.NewRouter()
	ok := func(http.ResponseWriter, *http.Request) {}
	r.Get("/repos/{name}/*", ok)
	r.Get("/api/v1/repositories/{name}", ok)
	r.Get("/boom", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })

	return HTTPMiddleware()(r)
}

func TestDisabledIsNoOp(t *testing.T) {
	shutdown, err := Setup(t.Context(), DefaultConfig(), Build{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if Enabled() {
		t.Fatal("Enabled() = true after Setup with telemetry disabled")
	}

	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	var seenTrace bool

	h := HTTPMiddleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenTrace = trace.SpanContextFromContext(r.Context()).IsValid()
	}))
	serve(h, http.MethodGet, "/x", map[string]string{"traceparent": traceparent})

	if seenTrace {
		t.Error("middleware created a span context while disabled")
	}

	// The client must not add headers, and must still work unchanged.
	var got http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()

	// A valid span in the context: a disabled client must still not propagate it.
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{1},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(t.Context(), sc)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)

	resp, err := WrapClient(&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if got.Get("Traceparent") != "" {
		t.Errorf("traceparent sent while disabled: %q", got.Get("Traceparent"))
	}

	if out := (DBTracer{}).TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"}); out != ctx {
		t.Error("DBTracer changed the context while disabled")
	}

	if LogFields(t.Context()) != nil {
		t.Error("LogFields returned fields without a span")
	}
}

func TestServerSpanNamingUsesRouteTemplate(t *testing.T) {
	rec := install(t)
	h := routerWithMiddleware()

	tests := []struct {
		method, target, wantName, wantRoute string
		wantStatus                          int
	}{
		{http.MethodGet, "/repos/alice-private-repo/pkg/1.0.0/file.tgz?token=sekret", "GET /repos/{name}", "/repos/{name}", 200},
		{http.MethodGet, "/repos/bob/other", "GET /repos/{name}", "/repos/{name}", 200},
		{http.MethodGet, "/api/v1/repositories/charlie", "GET /api/v1/repositories/{name}", "/api/v1/repositories/{name}", 200},
		{http.MethodGet, "/nope/12345?access_token=sekret", "GET", "", 404},
		{"BREW", "/boom", "_OTHER", "", 405},
		{http.MethodGet, "/boom", "GET /boom", "/boom", 502},
	}

	for _, tc := range tests {
		serve(h, tc.method, tc.target, nil)
	}

	spans := rec.Ended()
	if len(spans) != len(tests) {
		t.Fatalf("got %d spans, want %d", len(spans), len(tests))
	}

	names := map[string]bool{}

	for i, tc := range tests {
		s := spans[i]
		names[s.Name()] = true

		if s.Name() != tc.wantName {
			t.Errorf("%s %s: span name %q, want %q", tc.method, tc.target, s.Name(), tc.wantName)
		}

		a := attrs(s)
		if got := a["http.response.status_code"].AsInt64(); int(got) != tc.wantStatus {
			t.Errorf("%s: status attr %d, want %d", tc.target, got, tc.wantStatus)
		}

		if got := a["http.route"].AsString(); got != tc.wantRoute {
			t.Errorf("%s: http.route %q, want %q", tc.target, got, tc.wantRoute)
		}

		for k, v := range a {
			if strings.Contains(v.String(), "sekret") || strings.Contains(v.String(), "alice") || strings.Contains(v.String(), "12345") {
				t.Errorf("attribute %s leaks request data: %q", k, v.String())
			}
		}

		if (s.Status().Code == codes.Error) != (tc.wantStatus >= 500) {
			t.Errorf("%s: status %v for HTTP %d", tc.target, s.Status().Code, tc.wantStatus)
		}
	}

	// Distinct repos collapse into one name: cardinality follows the route table.
	if len(names) != 5 {
		t.Errorf("distinct span names = %v, want 5", names)
	}
}

func TestProbesAreNotTraced(t *testing.T) {
	rec := install(t)
	h := HTTPMiddleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, p := range []string{"/health", "/ready", "/metrics"} {
		serve(h, http.MethodGet, p, nil)
	}

	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("%d spans for probe endpoints, want 0", n)
	}
}

func TestPanicIsRecordedAndRepanicked(t *testing.T) {
	rec := install(t)
	h := HTTPMiddleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()

		serve(h, http.MethodGet, "/x", nil)
	}()

	s := spanByKind(t, rec, trace.SpanKindServer)
	if s.Status().Code != codes.Error || attrs(s)["http.response.status_code"].AsInt64() != 500 {
		t.Errorf("panicking request not recorded as 500 error: %v %v", s.Status(), attrs(s))
	}
}

func TestTraceContextPropagationAndBaggageContainment(t *testing.T) {
	rec := install(t)

	// Downstream service reached through the instrumented client.
	var down http.Header

	downstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { down = r.Header.Clone() }))
	defer downstream.Close()

	var gotBaggage string

	h := HTTPMiddleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotBaggage = baggage.FromContext(r.Context()).Member("tenant").Value()

		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, downstream.URL+"/pkg?sig=sekret", nil)
		req.Header.Set("Authorization", "Bearer sekret")

		resp, err := WrapClient(&http.Client{}).Do(req)
		if err != nil {
			t.Error(err)

			return
		}

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}))

	serve(h, http.MethodGet, "/x", map[string]string{"traceparent": traceparent, "baggage": "tenant=acme"})

	// Inbound: the server span continues the caller's trace.
	server := spanByKind(t, rec, trace.SpanKindServer)
	if got := server.SpanContext().TraceID().String(); got != incomingTraceID {
		t.Errorf("server trace id %s, want %s", got, incomingTraceID)
	}

	if got := server.Parent().SpanID().String(); got != incomingSpanID {
		t.Errorf("server parent span %s, want %s", got, incomingSpanID)
	}

	if gotBaggage != "acme" {
		t.Errorf("baggage not extracted: %q", gotBaggage)
	}

	// Outbound: same trace, the client span is the parent seen downstream.
	client := spanByKind(t, rec, trace.SpanKindClient)
	if client.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Error("client span is not a child of the server span")
	}

	out := propagation.TraceContext{}.Extract(t.Context(), propagation.HeaderCarrier(down))
	sc := trace.SpanContextFromContext(out)

	if sc.TraceID().String() != incomingTraceID || sc.SpanID() != client.SpanContext().SpanID() {
		t.Errorf("downstream traceparent %q does not name the client span", down.Get("Traceparent"))
	}

	// Inbound baggage is untrusted and must not leak to third parties.
	if down.Get("Baggage") != "" {
		t.Errorf("baggage forwarded downstream: %q", down.Get("Baggage"))
	}

	// The client span holds the host, never the path, query or credentials.
	for k, v := range attrs(client) {
		for _, bad := range []string{"sekret", "/pkg", "Bearer"} {
			if strings.Contains(v.String(), bad) {
				t.Errorf("client attribute %s leaks %q: %q", k, bad, v.String())
			}
		}
	}

	if client.Name() != "HTTP GET" {
		t.Errorf("client span name %q", client.Name())
	}
}

func TestClientSpanLifecycleAndErrors(t *testing.T) {
	rec := install(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c := WrapClient(&http.Client{})

	do := func(path string) *http.Response {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		return resp
	}

	// The span stays open until the body is consumed: it covers the transfer.
	resp := do("/ok")
	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("span ended before the body was read (%d ended)", n)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if n := len(rec.Ended()); n != 1 {
		t.Fatalf("%d spans after body read, want 1", n)
	}

	resp = do("/missing")
	_ = resp.Body.Close()

	if s := rec.Ended()[1]; s.Status().Code != codes.Error || attrs(s)["http.response.status_code"].AsInt64() != 404 {
		t.Errorf("404 not recorded as client error: %v", s.Status())
	}

	// A transport error must not put the URL (which may hold credentials) in
	// the span.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://user:pw@127.0.0.1:1/path?token=sekret", nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp, err := c.Do(req); err == nil {
		_ = resp.Body.Close()

		t.Fatal("expected a connection error")
	}

	s := rec.Ended()[2]
	if s.Status().Code != codes.Error || s.Status().Description != "" {
		t.Errorf("error span status %+v", s.Status())
	}

	for k, v := range attrs(s) {
		if strings.ContainsAny(v.String(), "?@") || strings.Contains(v.String(), "sekret") || strings.Contains(v.String(), "pw") {
			t.Errorf("attribute %s leaks request data: %q", k, v.String())
		}
	}

	if len(s.Events()) != 0 {
		t.Error("error span recorded events (RecordError would embed the URL)")
	}
}

func TestStatementName(t *testing.T) {
	tests := []struct{ sql, name, verb string }{
		{"-- name: GetUser :one\nSELECT * FROM users WHERE id = $1", "GetUser", "SELECT"},
		{"-- name: InsertAuditLog :exec\nINSERT INTO audit_logs VALUES ($1)", "InsertAuditLog", "INSERT"},
		{"  select 1", "SELECT", "SELECT"},
		{"WITH x AS (SELECT 1) SELECT * FROM x", "WITH", "WITH"},
		{"begin", "BEGIN", "BEGIN"},
		{"SELECT(1)", "SELECT", "SELECT"},
		{"frobnicate users_secret", "QUERY", "QUERY"},
		{"-- name: Bad;DROP :one\nSELECT 1", "SELECT", "SELECT"},
		{"", "QUERY", "QUERY"},
	}

	for _, tc := range tests {
		name, verb := statementName(tc.sql)
		if name != tc.name || verb != tc.verb {
			t.Errorf("statementName(%q) = %q,%q want %q,%q", tc.sql, name, verb, tc.name, tc.verb)
		}
	}
}

func TestDBTracer(t *testing.T) {
	rec := install(t)
	tr := DBTracer{}
	sql := "-- name: GetUserByName :one\nSELECT id FROM users WHERE name = $1"
	args := []any{"alice@example.com"}

	// No request span: workers polling the database do not mint root spans.
	ctx := tr.TraceQueryStart(t.Context(), nil, pgx.TraceQueryStartData{SQL: sql, Args: args})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("%d spans without a parent request span", n)
	}

	parentCtx, parent := otel.Tracer(scope).Start(t.Context(), "request")

	// Success, no rows and a database error.
	ctx = tr.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{SQL: sql, Args: args})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	ctx = tr.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{SQL: sql, Args: args})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: pgx.ErrNoRows})

	ctx = tr.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{SQL: sql, Args: args})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: `duplicate key value (alice@example.com)`}})

	bctx := tr.TraceBatchStart(parentCtx, nil, pgx.TraceBatchStartData{})
	tr.TraceBatchQuery(bctx, nil, pgx.TraceBatchQueryData{SQL: sql, Args: args})
	tr.TraceBatchEnd(bctx, nil, pgx.TraceBatchEndData{})

	parent.End()

	var db []sdktrace.ReadOnlySpan

	for _, s := range rec.Ended() {
		if s.Name() != "request" {
			db = append(db, s)
		}
	}

	if len(db) != 4 {
		t.Fatalf("%d db spans, want 4", len(db))
	}

	assertQuerySpans(t, db[:3])
	assertNoLeak(t, db)
}

func assertQuerySpans(t *testing.T, db []sdktrace.ReadOnlySpan) {
	t.Helper()

	for _, s := range db {
		if s.Name() != "GetUserByName" || s.SpanKind() != trace.SpanKindClient {
			t.Errorf("span %q kind %v", s.Name(), s.SpanKind())
		}

		a := attrs(s)
		if a["db.system.name"].AsString() != "postgresql" || a["db.operation.name"].AsString() != "SELECT" {
			t.Errorf("attrs %v", a)
		}
	}

	if db[0].Status().Code == codes.Error || db[1].Status().Code == codes.Error {
		t.Error("success / ErrNoRows must not be an error span")
	}

	if db[2].Status().Code != codes.Error || attrs(db[2])["db.response.status_code"].AsString() != "23505" {
		t.Errorf("pg error span: %v %v", db[2].Status(), attrs(db[2]))
	}
}

func assertNoLeak(t *testing.T, db []sdktrace.ReadOnlySpan) {
	t.Helper()

	if b := db[3]; b.Name() != "batch" || len(b.Events()) != 1 {
		t.Errorf("batch span %q events %d", b.Name(), len(b.Events()))
	}

	leaks := func(v string) bool { return strings.Contains(v, "alice") || strings.Contains(v, "users") }

	for _, s := range db {
		if s.Status().Description != "" {
			t.Errorf("status description %q (server messages quote values)", s.Status().Description)
		}

		for k, v := range attrs(s) {
			if leaks(v.String()) {
				t.Errorf("attribute %s leaks SQL or args: %q", k, v.String())
			}
		}

		for _, e := range s.Events() {
			for _, kv := range e.Attributes {
				if leaks(kv.Value.String()) {
					t.Errorf("event attribute %s leaks SQL or args", kv.Key)
				}
			}
		}
	}
}

func TestErrorTypeDoesNotEmbedURL(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://user:pw@h/?t=sekret", Err: io.ErrUnexpectedEOF}

	got := errorType(err)
	if strings.Contains(got, "sekret") || strings.Contains(got, "pw") || got != "*errors.errorString" {
		t.Errorf("errorType = %q", got)
	}
}

func TestLogFields(t *testing.T) {
	install(t)

	ctx, span := otel.Tracer(scope).Start(t.Context(), "s")
	defer span.End()

	f := LogFields(ctx)
	if len(f) != 4 || f[0] != "trace_id" || f[2] != "span_id" {
		t.Fatalf("fields = %v", f)
	}

	if f[1] != span.SpanContext().TraceID().String() || f[3] != span.SpanContext().SpanID().String() {
		t.Errorf("fields %v do not match span %v", f, span.SpanContext())
	}
}
