// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// memExporter keeps every exported record.
type memExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memExporter) Export(_ context.Context, rs []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, r := range rs {
		e.records = append(e.records, r.Clone())
	}

	return nil
}

func (e *memExporter) Shutdown(context.Context) error   { return nil }
func (e *memExporter) ForceFlush(context.Context) error { return nil }

func (e *memExporter) all() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]sdklog.Record(nil), e.records...)
}

// rowDB records inserted rows and fails every insert when err is set. With
// block set, every insert first waits until block is closed.
type rowDB struct {
	databasetest.BaseMockDB

	mu    sync.Mutex
	rows  []*models.AuditLog
	err   error
	block chan struct{}
}

func (d *rowDB) InsertAuditLog(_ context.Context, row *models.AuditLog) error {
	if d.block != nil {
		<-d.block
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.rows = append(d.rows, row)

	return d.err
}

// installProvider makes a synchronous SDK provider exporting to the returned
// exporter the global one for the test.
func installProvider(t *testing.T) *memExporter {
	t.Helper()

	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	otel.SetLoggerProvider(lp)
	t.Cleanup(func() {
		otel.SetLoggerProvider(lognoop.NewLoggerProvider())
		_ = lp.Shutdown(context.Background())
	})

	return exp
}

func newTestLogger(t *testing.T, db *rowDB, opts ...Option) *Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return NewLogger(db, log, nil, 10, opts...)
}

func logAndDrain(t *testing.T, l *Logger, ctx context.Context, ev AuditEvent) {
	t.Helper()

	l.Log(ctx, ev)

	if err := l.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func recordAttrs(r sdklog.Record) map[string]attribute.Value {
	out := map[string]attribute.Value{}

	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		out[string(kv.Key)] = kv.Value

		return true
	})

	return out
}

func TestLogStreamsTheInsertedRow(t *testing.T) {
	exp := installProvider(t)
	db := &rowDB{}
	l := newTestLogger(t, db, WithOTLP())

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3},
		SpanID:     trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	ctx = WithRequest(ctx, Request{ID: "req-1", Username: "alice", TokenID: "tok-1"})

	repo, ip, actorID := "repo-1", "192.0.2.7", "user-1"
	logAndDrain(t, l, ctx, AuditEvent{
		Action: ActionRepositoryCreated, ActorType: "token", ActorID: &actorID, Actor: "alice",
		RepositoryID: &repo, IPAddress: &ip, Details: "created", Status: 201,
		Method: "POST", Route: "/api/v1/repositories",
	})

	if len(db.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(db.rows))
	}

	row := db.rows[0]

	recs := exp.all()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}

	rec := recs[0]

	if rec.EventName() != EventName || rec.InstrumentationScope().Name != Scope {
		t.Errorf("event %q scope %q, want %q on %q", rec.EventName(), rec.InstrumentationScope().Name, EventName, Scope)
	}

	if !rec.Timestamp().Equal(row.CreatedAt) || row.CreatedAt.IsZero() {
		t.Errorf("timestamp %v, row created_at %v: want equal and set", rec.Timestamp(), row.CreatedAt)
	}

	if rec.TraceID() != sc.TraceID() || rec.SpanID() != sc.SpanID() {
		t.Errorf("trace %s/%s, want %s/%s", rec.TraceID(), rec.SpanID(), sc.TraceID(), sc.SpanID())
	}

	if rec.Severity() != otellog.SeverityInfo || rec.Body().AsString() != "created" {
		t.Errorf("severity %v body %q", rec.Severity(), rec.Body().AsString())
	}

	want := map[string]string{
		"id": row.ID, "action": ActionRepositoryCreated, "outcome": OutcomeSuccess,
		"actor": "alice", "username": "alice", "actor_type": "token", "actor_id": actorID,
		"token_id": "tok-1", "repository_id": repo, "ip": ip, "request_id": "req-1",
	}

	attrs := recordAttrs(rec)
	for k, v := range want {
		if got := attrs[attrPrefix+k].AsString(); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	if got := attrs[attrPrefix+"status"].AsInt64(); got != 201 {
		t.Errorf("status = %d, want 201", got)
	}

	checkSemconv(t, attrs, map[string]attribute.Value{
		"client.address": attribute.StringValue(ip), "http.request.method": attribute.StringValue("POST"),
		"http.route": attribute.StringValue("/api/v1/repositories"), "http.response.status_code": attribute.IntValue(201),
	})

	var details map[string]any
	if err := json.Unmarshal(row.DetailsJSON, &details); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"outcome", "username", "token_id", "request_id"} {
		if details[k] != want[k] {
			t.Errorf("details[%s] = %v, want %q", k, details[k], want[k])
		}
	}

	if details["status"] != float64(201) {
		t.Errorf("details[status] = %v, want 201", details["status"])
	}
}

func TestLogStreamsWhenTheInsertFails(t *testing.T) {
	exp := installProvider(t)
	db := &rowDB{err: errors.New("database down")}
	l := newTestLogger(t, db, WithOTLP())

	logAndDrain(t, l, context.Background(), AuditEvent{Action: ActionAuthLogin})

	recs := exp.all()
	if len(recs) != 1 || len(db.rows) != 1 {
		t.Fatalf("records = %d, insert attempts = %d, want 1 and 1", len(recs), len(db.rows))
	}

	if got := recordAttrs(recs[0])[attrPrefix+"id"].AsString(); got != db.rows[0].ID {
		t.Errorf("streamed id %q, row id %q", got, db.rows[0].ID)
	}
}

func checkSemconv(t *testing.T, attrs, want map[string]attribute.Value) {
	t.Helper()

	for k, v := range want {
		if got := attrs[k]; got != v {
			t.Errorf("%s = %s, want %s", k, got, v)
		}
	}
}

// The event is streamed when Log returns, while the database still holds its
// insert: the stream does not wait for the database.
func TestLogStreamsBeforeTheInsert(t *testing.T) {
	exp := installProvider(t)
	db := &rowDB{block: make(chan struct{})}
	l := newTestLogger(t, db, WithOTLP())

	l.Log(context.Background(), AuditEvent{Action: ActionAuthLogin})

	if n := len(exp.all()); n != 1 {
		t.Errorf("records while the insert is pending = %d, want 1", n)
	}

	close(db.block)

	if err := l.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(db.rows) != 1 {
		t.Errorf("rows = %d, want 1", len(db.rows))
	}
}

func TestLoginFailedStreamsNoUsername(t *testing.T) {
	exp := installProvider(t)
	l := newTestLogger(t, &rowDB{}, WithOTLP())

	ip := "192.0.2.9"
	logAndDrain(t, l, context.Background(), AuditEvent{
		Action: ActionAuthLoginFailed, Actor: "hunter2", Username: "hunter2", Outcome: OutcomeFailure,
		IPAddress: &ip, Details: "login failed (status 401)", Status: 401,
	})

	rec := exp.all()[0]
	attrs := recordAttrs(rec)

	for _, k := range []string{"actor", "username"} {
		if v, ok := attrs[attrPrefix+k]; ok {
			t.Errorf("%s streamed on auth.login_failed: %q", k, v.AsString())
		}
	}

	if attrs[attrPrefix+"ip"].AsString() != ip || rec.Severity() != otellog.SeverityWarn {
		t.Errorf("ip %q severity %v, want %q and WARN", attrs[attrPrefix+"ip"].AsString(), rec.Severity(), ip)
	}
}

func TestLogWithoutOTLPStreamsNothing(t *testing.T) {
	exp := installProvider(t)
	db := &rowDB{}
	l := newTestLogger(t, db)

	logAndDrain(t, l, context.Background(), AuditEvent{Action: ActionAuthLogin})

	if n := len(exp.all()); n != 0 || len(db.rows) != 1 {
		t.Errorf("records = %d, rows = %d, want 0 and 1", n, len(db.rows))
	}
}
