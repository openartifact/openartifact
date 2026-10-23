// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// sqlcNamePrefix starts every sqlc-generated statement ("-- name: GetUser :one").
const sqlcNamePrefix = "-- name: "

// DBTracer is a pgx tracer (query and batch) that emits one client span per
// statement, named after the sqlc statement ("GetUser") or, for hand-written
// SQL, its leading verb ("SELECT"). The SQL text and its arguments are never
// recorded, so neither values nor identifiers from user input reach a span.
//
// Spans are created only under a sampled request span: background workers
// polling the database do not mint a trace per query. It is safe to install
// unconditionally: while telemetry is disabled every hook returns at once.
type DBTracer struct{}

type dbSpanKey struct{}

var (
	_ pgx.QueryTracer = DBTracer{}
	_ pgx.BatchTracer = DBTracer{}
)

// TraceQueryStart implements pgx.QueryTracer.
func (DBTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	name, verb := statementName(data.SQL)

	return startDBSpan(ctx, name, verb)
}

// TraceQueryEnd implements pgx.QueryTracer.
func (DBTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	endDBSpan(ctx, data.Err)
}

// TraceBatchStart implements pgx.BatchTracer.
func (DBTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return startDBSpan(ctx, "batch", "BATCH")
}

// TraceBatchQuery implements pgx.BatchTracer: each statement of the batch is
// an event on the batch span.
func (DBTracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	if span, ok := ctx.Value(dbSpanKey{}).(trace.Span); ok {
		name, _ := statementName(data.SQL)
		span.AddEvent("db.batch.query", trace.WithAttributes(attribute.String("db.query.summary", name)))
	}
}

// TraceBatchEnd implements pgx.BatchTracer.
func (DBTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	endDBSpan(ctx, data.Err)
}

func startDBSpan(ctx context.Context, name, verb string) context.Context {
	if !Enabled() || !trace.SpanFromContext(ctx).IsRecording() {
		return ctx
	}

	//nolint:spancheck // ended by endDBSpan through the context
	ctx, span := otel.Tracer(scope).Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system.name", "postgresql"),
			attribute.String("db.operation.name", verb),
			attribute.String("db.query.summary", name),
		))

	return context.WithValue(ctx, dbSpanKey{}, span) //nolint:spancheck // ended by endDBSpan
}

func endDBSpan(ctx context.Context, err error) {
	span, ok := ctx.Value(dbSpanKey{}).(trace.Span)
	if !ok {
		return
	}

	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// SQLSTATE and the Go type only: server messages quote values.
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
			span.SetAttributes(attribute.String("db.response.status_code", pgErr.Code))
		} else {
			span.SetAttributes(attribute.String("error.type", typeName(err)))
		}

		span.SetStatus(codes.Error, "")
	}

	span.End()
}

// sqlVerbs bounds the verbs reported for hand-written SQL.
var sqlVerbs = map[string]struct{}{ //nolint:gochecknoglobals // immutable allowlist
	"SELECT": {}, "INSERT": {}, "UPDATE": {}, "DELETE": {}, "WITH": {}, "BEGIN": {},
	"COMMIT": {}, "ROLLBACK": {}, "SET": {}, "SHOW": {}, "LOCK": {}, "CALL": {},
	"CREATE": {}, "ALTER": {}, "DROP": {}, "TRUNCATE": {}, "COPY": {}, "VACUUM": {},
	"ANALYZE": {}, "REFRESH": {}, "LISTEN": {}, "NOTIFY": {}, "DO": {},
}

// statementName returns the span name and the SQL verb of a statement. The
// name is the sqlc statement name when present, otherwise the verb; both come
// from a fixed set (generated names, verb allowlist), so cardinality is bounded.
func statementName(sql string) (string, string) {
	s := strings.TrimSpace(sql)

	if rest, ok := strings.CutPrefix(s, sqlcNamePrefix); ok {
		end := strings.IndexAny(rest, " \t\r\n")
		if end < 0 {
			end = len(rest)
		}

		// "-- name: GetUser :one\nSELECT ..." keeps the verb of the body.
		_, body, _ := strings.Cut(rest, "\n")
		verb := firstVerb(body)

		if id := rest[:end]; isIdent(id) {
			return id, verb
		}

		return verb, verb
	}

	v := firstVerb(s)

	return v, v
}

func firstVerb(s string) string {
	s = strings.TrimSpace(s)

	end := strings.IndexAny(s, " \t\r\n(;")
	if end < 0 {
		end = len(s)
	}

	if v := strings.ToUpper(s[:end]); v != "" {
		if _, ok := sqlVerbs[v]; ok {
			return v
		}
	}

	return "QUERY"
}

func isIdent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}

	for _, r := range s {
		if r != '_' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}

	return true
}
