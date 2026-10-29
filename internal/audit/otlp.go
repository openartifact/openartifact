// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package audit

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// Scope is the instrumentation scope and EventName the event name of every
// audit event streamed over OTLP. The name is fixed (the action is an
// attribute): event names must not carry dynamic values.
const (
	Scope     = "openartifact.audit"
	EventName = "openartifact.audit"
)

// attrPrefix starts every attribute of an audit event. The names after it
// are the keys of the export (id, action, actor, ...) and of details_json
// (outcome, username, ...), so the stream and `oa audit export` agree. The
// client address and the HTTP method, route and status also go out under their
// OpenTelemetry semantic-convention names. The set is a documented contract
// (docs/operations/monitoring.md): attributes may be added, never renamed or
// removed.
const attrPrefix = "openartifact.audit."

// WithOTLP also streams every event as an OpenTelemetry log event on the
// Scope logger of the global logger provider (otel.GetLoggerProvider), which
// telemetry.Setup installs when telemetry.otlp.audit is on. The audit_logs
// table stays the source of truth: the stream is best effort.
func WithOTLP() Option {
	return func(l *Logger) {
		l.stream = otel.GetLoggerProvider().Logger(Scope)
	}
}

// emit streams e. The record is handed to the provider's batch processor,
// which never waits for the collector; a full export queue drops it and counts
// it in openartifact_telemetry_log_records_dropped_total{scope="openartifact.audit"}.
func (l *Logger) emit(e entry) {
	if l.stream == nil {
		return
	}

	// The SDK takes the trace and span id of the record from the context.
	l.stream.Emit(trace.ContextWithSpanContext(context.Background(), e.span), newRecord(e))
}

// newRecord renders e as an audit log event. On auth.login_failed the actor is
// the username the client typed, which may well be a password: it is left out
// of the stream (the audit_logs row keeps it, readable by admins only).
func newRecord(e entry) otellog.Record {
	var rec otellog.Record

	rec.SetEventName(EventName)
	rec.SetTimestamp(e.at)

	if e.Outcome == OutcomeFailure {
		rec.SetSeverity(otellog.SeverityWarn)
		rec.SetSeverityText("WARN")
	} else {
		rec.SetSeverity(otellog.SeverityInfo)
		rec.SetSeverityText("INFO")
	}

	body := e.Details
	if body == "" {
		body = e.Action
	}

	rec.SetBody(attribute.StringValue(body))

	attrs := make([]attribute.KeyValue, 0, 24) //nolint:mnd // every attribute below set at once
	str := func(key, v string) {
		if v != "" {
			attrs = append(attrs, attribute.String(key, v))
		}
	}
	add := func(key, v string) { str(attrPrefix+key, v) }

	add("id", e.id)
	add("action", e.Action)
	add("outcome", e.Outcome)

	if e.Action != ActionAuthLoginFailed {
		add("actor", e.Actor)
		add("username", e.Username)
	}

	add("actor_type", e.ActorType)
	add("actor_id", deref(e.ActorID))
	add("token_id", e.TokenID)
	add("resource_type", deref(e.ResourceType))
	add("resource_id", deref(e.ResourceID))
	add("repository_id", deref(e.RepositoryID))
	add("project_id", deref(e.ProjectID))
	add("ip", deref(e.IPAddress))
	add("request_id", e.RequestID)
	add("package", e.Package)
	add("version", e.Version)
	add("subject", e.Subject)

	str("client.address", deref(e.IPAddress))
	str("http.request.method", e.Method)
	str("http.route", e.Route)

	if e.Status != 0 {
		attrs = append(attrs, attribute.Int(attrPrefix+"status", e.Status), attribute.Int("http.response.status_code", e.Status))
	}

	rec.AddAttributes(attrs...)

	return rec
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
