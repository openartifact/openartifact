// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/trace"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/tests/integration/otelcoltest"
)

// auditLogRecord is one log record of the collector's OTLP JSON export.
//
//nolint:tagliatelle // OTLP JSON wire format
type auditLogRecord struct {
	TimeUnixNano string `json:"timeUnixNano"`
	SeverityText string `json:"severityText"`
	EventName    string `json:"eventName"`
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	Body         struct {
		StringValue string `json:"stringValue"`
	} `json:"body"`
	Attributes []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue *string `json:"stringValue"`
			IntValue    *string `json:"intValue"`
		} `json:"value"`
	} `json:"attributes"`

	scope string
}

// attrs renders the attributes as strings (an int as its decimal text).
func (r auditLogRecord) attrs() map[string]string {
	out := map[string]string{}

	for _, a := range r.Attributes {
		switch {
		case a.Value.StringValue != nil:
			out[a.Key] = *a.Value.StringValue
		case a.Value.IntValue != nil:
			out[a.Key] = *a.Value.IntValue
		}
	}

	return out
}

// auditRecords decodes every log record of the openartifact.audit scope the
// collector exported so far.
func auditRecords(t *testing.T, col *otelcoltest.Collector) []auditLogRecord {
	t.Helper()

	lines, err := col.Lines(t.Context())
	if err != nil {
		t.Fatalf("read collector export: %v", err)
	}

	var out []auditLogRecord

	for _, line := range lines {
		//nolint:tagliatelle // OTLP JSON wire format
		var req struct {
			ResourceLogs []struct {
				ScopeLogs []struct {
					Scope struct {
						Name string `json:"name"`
					} `json:"scope"`
					LogRecords []auditLogRecord `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}

		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("decode export line: %v\n%s", err, line)
		}

		for _, rl := range req.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				if sl.Scope.Name != audit.Scope {
					continue
				}

				for _, r := range sl.LogRecords {
					r.scope = sl.Scope.Name
					out = append(out, r)
				}
			}
		}
	}

	return out
}

// waitForAuditRecords polls the export until want records match keep.
func waitForAuditRecords(t *testing.T, col *otelcoltest.Collector, want int, keep func(auditLogRecord) bool) []auditLogRecord {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for {
		var got []auditLogRecord

		for _, r := range auditRecords(t, col) {
			if keep(r) {
				got = append(got, r)
			}
		}

		if len(got) >= want || time.Now().After(deadline) {
			return got
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// startAuditStream installs the OTLP log provider for audit only (no traces)
// against endpoint over gRPC and returns the audit logger streaming to it, the
// drop counter and the telemetry shutdown.
func startAuditStream(t *testing.T, endpoint string) (*audit.Logger, *prometheus.CounterVec, func(context.Context) error) {
	t.Helper()

	cfg := telemetry.DefaultConfig()
	cfg.OTLP.Enabled = true
	cfg.OTLP.Endpoint = endpoint
	cfg.OTLP.Insecure = true
	cfg.OTLP.ServiceName = "oa-otlp-audit-it"
	cfg.OTLP.Traces = false
	cfg.OTLP.Audit = true

	dropped := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dropped_total", Help: "h"}, []string{"scope"})

	shutdown, err := telemetry.Setup(t.Context(), cfg, telemetry.Build{Version: "it"}, nil, dropped, nil)
	if err != nil {
		t.Fatal(err)
	}

	return audit.NewLogger(harness.Database, harness.Log, nil, 1000, audit.WithOTLP()), dropped, shutdown
}

// auditHTTPServer serves DELETE /api/v1/repository/{name} (audited by the
// middleware as repository.deleted) behind the request id and audit middleware.
func auditHTTPServer(t *testing.T, al *audit.Logger) *httptest.Server {
	t.Helper()

	r := chi.NewRouter()
	r.Use(middleware.AuditMiddleware(al))
	r.Delete("/api/v1/repository/{name}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	srv := httptest.NewServer(middleware.RequestIDMiddleware(r))
	t.Cleanup(srv.Close)

	return srv
}

// deleteRepository sends one audited request and returns its X-Request-ID.
func deleteRepository(t *testing.T, srv *httptest.Server) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, srv.URL+"/api/v1/repository/it", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	return resp.Header.Get("X-Request-ID")
}

// expectedAttrs is the attribute set the contract derives from a stored row:
// the export columns and the details_json keys (message is the body).
func expectedAttrs(t *testing.T, id string) (map[string]string, time.Time, string) {
	t.Helper()

	var (
		action, details                              string
		createdAt                                    time.Time
		actorType, actorID, resType, resID, repo, ip sql.NullString
		project                                      sql.NullString
	)

	err := harness.DB.QueryRowContext(t.Context(), `
		SELECT action, created_at, actor_type, actor_id::text, resource_type, resource_id,
		       repository_id::text, project_id::text, host(ip_address), details_json::text
		FROM audit_logs WHERE id = $1`, id).
		Scan(&action, &createdAt, &actorType, &actorID, &resType, &resID, &repo, &project, &ip, &details)
	if err != nil {
		t.Fatalf("audit row %s: %v", id, err)
	}

	want := map[string]string{"id": id, "action": action}

	for k, v := range map[string]sql.NullString{
		"actor_type": actorType, "actor_id": actorID, "resource_type": resType, "resource_id": resID,
		"repository_id": repo, "project_id": project, "ip": ip,
	} {
		if v.Valid {
			want[k] = v.String
		}
	}

	var d map[string]any
	if err := json.Unmarshal([]byte(details), &d); err != nil {
		t.Fatal(err)
	}

	message, _ := d["message"].(string)
	delete(d, "message")

	for k, v := range d {
		switch v := v.(type) {
		case string:
			want[k] = v
		case float64:
			want[k] = strconv.FormatInt(int64(v), 10)
		}
	}

	if action == audit.ActionAuthLoginFailed {
		delete(want, "actor")
		delete(want, "username")
	}

	full := make(map[string]string, len(want)+4) //nolint:mnd // the semantic-convention names below
	for k, v := range want {
		full["openartifact.audit."+k] = v
	}

	// The same values under their semantic-convention names; the method and
	// route of a middleware event come from its message.
	for semconv, k := range map[string]string{"client.address": "ip", "http.response.status_code": "status"} {
		if v, ok := want[k]; ok {
			full[semconv] = v
		}
	}

	var req struct{ Method, Path string }
	if json.Unmarshal([]byte(message), &req) == nil && req.Method != "" {
		full["http.request.method"] = req.Method
		if req.Path != "unknown" {
			full["http.route"] = req.Path
		}
	}

	if message == "" {
		message = action
	}

	return full, createdAt, message
}

// checkAgainstRow compares an exported event with its audit_logs row: event
// name, scope, time, body and exactly the contract attributes. It returns the
// event's attributes.
func checkAgainstRow(t *testing.T, r auditLogRecord) map[string]string {
	t.Helper()

	got := r.attrs()
	id := got["openartifact.audit.id"]
	wantAttrs, createdAt, body := expectedAttrs(t, id)

	if r.EventName != audit.EventName || r.scope != audit.Scope {
		t.Errorf("%s: event %q scope %q", id, r.EventName, r.scope)
	}

	if r.TimeUnixNano != strconv.FormatInt(createdAt.UnixNano(), 10) {
		t.Errorf("%s: time %s, row created_at %s", id, r.TimeUnixNano, createdAt.Format(time.RFC3339Nano))
	}

	if r.Body.StringValue != body {
		t.Errorf("%s: body %q, want %q", id, r.Body.StringValue, body)
	}

	for k, v := range wantAttrs {
		if got[k] != v {
			t.Errorf("%s %s: %s = %q, want %q", id, wantAttrs["openartifact.audit.action"], k, got[k], v)
		}
	}

	for k := range got {
		if _, ok := wantAttrs[k]; !ok {
			t.Errorf("%s: attribute %s = %q is not in the row", id, k, got[k])
		}
	}

	return got
}

// TestOTLPAuditEventsArriveOnceWithTheirRow records every known action through
// the audit logger, and one request through the audit middleware, with a real
// Postgres and a real collector. Each event must be exported exactly once, as
// an openartifact.audit event whose attributes, time and body are those of its
// audit_logs row (what `oa audit export` prints), with the caller's trace.
func TestOTLPAuditEventsArriveOnceWithTheirRow(t *testing.T) {
	col := otelcoltest.Start(t)
	al, dropped, shutdown := startAuditStream(t, col.GRPCEndpoint)

	run := uuid.NewString()[:8]
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0xa1, 0xd1, 0x7e}, SpanID: trace.SpanID{0x5e, 0x1f}, TraceFlags: trace.FlagsSampled,
	})

	ip, resType := "192.0.2.10", "it"

	for _, action := range audit.KnownActions {
		ctx := audit.WithRequest(trace.ContextWithSpanContext(t.Context(), sc), audit.Request{
			ID: "it-" + run + "-" + action, Username: "it-user", TokenID: "it-token",
		})
		resID := action

		al.Log(ctx, audit.AuditEvent{
			Action: action, ActorType: "user", Actor: "it-user", IPAddress: &ip,
			ResourceType: &resType, ResourceID: &resID, Details: "it " + action, Status: http.StatusOK,
		})
	}

	httpRequestID := deleteRepository(t, auditHTTPServer(t, al))

	if err := al.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Shutdown flushes the batch processor to the collector.
	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	ours := func(r auditLogRecord) bool {
		id := r.attrs()["openartifact.audit.request_id"]
		return strings.HasPrefix(id, "it-"+run+"-") || id == httpRequestID
	}

	want := len(audit.KnownActions) + 1
	recs := waitForAuditRecords(t, col, want, ours)

	if len(recs) != want {
		t.Fatalf("exported %d audit events, want %d", len(recs), want)
	}

	perAction := map[string]int{}

	for _, r := range recs {
		got := checkAgainstRow(t, r)
		perAction[got["openartifact.audit.request_id"]]++

		if got["openartifact.audit.request_id"] == httpRequestID {
			if got["openartifact.audit.action"] != audit.ActionRepositoryDeleted || got["openartifact.audit.status"] != "204" {
				t.Errorf("request event: %v", got)
			}

			continue
		}

		if r.TraceID != sc.TraceID().String() || r.SpanID != sc.SpanID().String() {
			t.Errorf("%s: trace %s span %s, want %s %s", got["openartifact.audit.id"], r.TraceID, r.SpanID, sc.TraceID(), sc.SpanID())
		}
	}

	for reqID, n := range perAction {
		if n != 1 {
			t.Errorf("%s exported %d times, want once", reqID, n)
		}
	}

	if n := testutil.CollectAndCount(dropped); n != 0 {
		t.Errorf("dropped series = %d, want none", n)
	}
}

// TestOTLPAuditCollectorOutageDropsAndCounts stops the collector and keeps
// sending audited requests: none may wait for the collector, every row still
// reaches audit_logs, and the events that do not fit the export queue are
// dropped and counted under the openartifact.audit scope.
func TestOTLPAuditCollectorOutageDropsAndCounts(t *testing.T) {
	const (
		queue    = 16
		requests = 300
		// slowest is far below the exporter's retry window (a minute): a
		// request that waited for the collector would blow it.
		slowest = 2 * time.Second
	)

	t.Setenv("OTEL_BLRP_MAX_QUEUE_SIZE", strconv.Itoa(queue))

	col := otelcoltest.Start(t)
	al, dropped, shutdown := startAuditStream(t, col.GRPCEndpoint)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// The exporter is still retrying the dead collector: give up.
		_ = shutdown(ctx)
	})

	srv := auditHTTPServer(t, al)

	// One event through a live collector first: the pipeline works.
	first := deleteRepository(t, srv)
	if got := waitForAuditRecords(t, col, 1, func(r auditLogRecord) bool {
		return r.attrs()["openartifact.audit.request_id"] == first
	}); len(got) != 1 {
		t.Fatalf("event before the outage: %d exported, want 1", len(got))
	}

	col.Stop(t)

	ids := make([]string, 0, requests)

	for range requests {
		start := time.Now()

		ids = append(ids, deleteRepository(t, srv))

		if d := time.Since(start); d > slowest {
			t.Fatalf("a request took %s during the collector outage", d)
		}
	}

	// The writer drains at database speed: the stream never holds it up.
	drainCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	if err := al.Shutdown(drainCtx); err != nil {
		t.Fatalf("audit writer did not drain during the outage: %v", err)
	}

	var rows int

	err := harness.DB.QueryRowContext(t.Context(),
		`SELECT count(*) FROM audit_logs WHERE details_json->>'request_id' = ANY(string_to_array($1, ','))`,
		strings.Join(ids, ",")).Scan(&rows)
	if err != nil {
		t.Fatal(err)
	}

	if rows != requests {
		t.Errorf("audit_logs has %d of the %d events recorded during the outage", rows, requests)
	}

	// The batch in the failing export and the queue behind it hold at most
	// two queues of records; every other one is dropped and counted.
	n := testutil.ToFloat64(dropped.WithLabelValues(audit.Scope))
	if n < requests-2*queue || n > requests {
		t.Errorf("dropped{scope=%q} = %v, want between %d and %d", audit.Scope, n, requests-2*queue, requests)
	}
}
