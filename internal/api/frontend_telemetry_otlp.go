// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"fmt"
	"unicode/utf8"

	colllogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// What the relay accepts from a browser. The web UI exports batches of at most
// 64 records with a handful of attributes each, so these are generous for it
// and bound what a signed-in user can have the collector (and the backend
// behind it, with the operator's credentials) ingest per request.
const (
	// relayMaxRecords caps the spans, or the log records, of one request;
	// more is refused.
	relayMaxRecords = 256
	// relayMaxAttrs caps the attributes of a resource, scope, span, event, link
	// or log record; the excess is dropped.
	relayMaxAttrs = 32
	// relayMaxEvents caps the events and links of a span; the excess is dropped.
	relayMaxEvents = 16
	// relayMaxKey caps an attribute key and relayMaxName a span name or scope
	// name, in bytes; longer ones are cut.
	relayMaxKey  = 128
	relayMaxName = 256
	// relayMaxValue caps any string or bytes value, in bytes (a stack trace is
	// the long one); longer ones are cut.
	relayMaxValue = 8 << 10
	// relayMaxDepth is how deep an array or map value may nest; deeper is
	// dropped.
	relayMaxDepth = 3

	traceIDLen = 16
	spanIDLen  = 8

	// frontendServiceName is the only service.name the relay lets through, so a
	// session cannot pose as the server's own telemetry.
	frontendServiceName = "openartifact-web"
)

var errOTLPInvalid = errors.New("invalid OTLP payload")

// sanitizeOTLP parses body as the OTLP/HTTP protobuf export request of signal
// ("traces" or "logs") and returns the request re-encoded within the bounds
// above: it never forwards bytes it did not parse. A payload that is not valid
// OTLP, has malformed ids or has too many records is an error; anything that
// is only too big is cut, so a normal export is never refused for it.
func sanitizeOTLP(signal string, body []byte) ([]byte, error) {
	var msg proto.Message

	switch signal {
	case "traces":
		var req colltrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", errOTLPInvalid, err)
		}

		if err := sanitizeTraces(&req); err != nil {
			return nil, err
		}

		msg = &req
	case "logs":
		var req colllogs.ExportLogsServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", errOTLPInvalid, err)
		}

		if err := sanitizeLogs(&req); err != nil {
			return nil, err
		}

		msg = &req
	default:
		return nil, fmt.Errorf("%w: signal %q", errOTLPInvalid, signal)
	}

	out, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errOTLPInvalid, err)
	}

	return out, nil
}

func sanitizeTraces(req *colltrace.ExportTraceServiceRequest) error {
	spans := 0

	for _, rs := range req.GetResourceSpans() {
		rs.Resource = sanitizeResource(rs.GetResource())
		rs.SchemaUrl = cut(rs.GetSchemaUrl(), relayMaxName)

		for _, ss := range rs.GetScopeSpans() {
			sanitizeScope(ss.GetScope())
			ss.SchemaUrl = cut(ss.GetSchemaUrl(), relayMaxName)

			for _, sp := range ss.GetSpans() {
				spans++
				if spans > relayMaxRecords {
					return fmt.Errorf("%w: more than %d spans", errOTLPInvalid, relayMaxRecords)
				}

				if len(sp.GetTraceId()) != traceIDLen || len(sp.GetSpanId()) != spanIDLen ||
					(len(sp.GetParentSpanId()) != 0 && len(sp.GetParentSpanId()) != spanIDLen) {
					return fmt.Errorf("%w: malformed span ids", errOTLPInvalid)
				}

				sp.TraceState = cut(sp.GetTraceState(), relayMaxValue)
				sp.Name = cut(sp.GetName(), relayMaxName)
				sp.Attributes = sanitizeAttrs(sp.GetAttributes())

				sp.Events = capSlice(sp.GetEvents(), relayMaxEvents)
				for _, ev := range sp.GetEvents() {
					ev.Name = cut(ev.GetName(), relayMaxName)
					ev.Attributes = sanitizeAttrs(ev.GetAttributes())
				}

				sp.Links = capSlice(sp.GetLinks(), relayMaxEvents)
				for _, l := range sp.GetLinks() {
					l.TraceState = cut(l.GetTraceState(), relayMaxValue)
					l.Attributes = sanitizeAttrs(l.GetAttributes())
				}

				if st := sp.GetStatus(); st != nil {
					st.Message = cut(st.GetMessage(), relayMaxValue)
				}
			}
		}
	}

	return nil
}

func sanitizeLogs(req *colllogs.ExportLogsServiceRequest) error {
	records := 0

	for _, rl := range req.GetResourceLogs() {
		rl.Resource = sanitizeResource(rl.GetResource())
		rl.SchemaUrl = cut(rl.GetSchemaUrl(), relayMaxName)

		for _, sl := range rl.GetScopeLogs() {
			sanitizeScope(sl.GetScope())
			sl.SchemaUrl = cut(sl.GetSchemaUrl(), relayMaxName)

			for _, lr := range sl.GetLogRecords() {
				records++
				if records > relayMaxRecords {
					return fmt.Errorf("%w: more than %d log records", errOTLPInvalid, relayMaxRecords)
				}

				if (len(lr.GetTraceId()) != 0 && len(lr.GetTraceId()) != traceIDLen) ||
					(len(lr.GetSpanId()) != 0 && len(lr.GetSpanId()) != spanIDLen) {
					return fmt.Errorf("%w: malformed log record ids", errOTLPInvalid)
				}

				lr.SeverityText = cut(lr.GetSeverityText(), relayMaxName)
				lr.EventName = cut(lr.GetEventName(), relayMaxName)
				lr.Attributes = sanitizeAttrs(lr.GetAttributes())

				if lr.GetBody() != nil {
					lr.Body = sanitizeValue(lr.GetBody(), 0)
				}
			}
		}
	}

	return nil
}

// sanitizeResource keeps service.name (forced to frontendServiceName) and
// service.version, nothing else: the resource describes the sender, and the
// sender is the web UI, whatever the browser claims.
func sanitizeResource(r *resourcepb.Resource) *resourcepb.Resource {
	out := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
		Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: frontendServiceName}},
	}}}

	for _, kv := range r.GetAttributes() {
		if kv.GetKey() == "service.version" {
			if v, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_StringValue); ok {
				out.Attributes = append(out.Attributes, &commonpb.KeyValue{
					Key:   kv.GetKey(),
					Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: cut(v.StringValue, relayMaxName)}},
				})
			}
		}
	}

	return out
}

func sanitizeScope(s *commonpb.InstrumentationScope) {
	if s == nil {
		return
	}

	s.Name = cut(s.GetName(), relayMaxName)
	s.Version = cut(s.GetVersion(), relayMaxName)
	s.Attributes = sanitizeAttrs(s.GetAttributes())
}

// sanitizeAttrs keeps the first relayMaxAttrs attributes with their keys and
// values cut to size.
func sanitizeAttrs(in []*commonpb.KeyValue) []*commonpb.KeyValue {
	in = capSlice(in, relayMaxAttrs)
	out := in[:0]

	for _, kv := range in {
		if kv == nil {
			continue
		}

		kv.Key = cut(kv.GetKey(), relayMaxKey)
		kv.KeyStrindex = 0

		if kv.GetValue() != nil {
			kv.Value = sanitizeValue(kv.GetValue(), 0)
		}

		out = append(out, kv)
	}

	return out
}

// sanitizeValue cuts the strings and bytes of v and bounds how deep and wide
// arrays and maps go. It returns nil for a value nested too deep.
func sanitizeValue(v *commonpb.AnyValue, depth int) *commonpb.AnyValue {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		x.StringValue = cut(x.StringValue, relayMaxValue)
	case *commonpb.AnyValue_BytesValue:
		x.BytesValue = capSlice(x.BytesValue, relayMaxValue)
	case *commonpb.AnyValue_ArrayValue:
		if depth >= relayMaxDepth || x.ArrayValue == nil {
			return nil
		}

		vals := capSlice(x.ArrayValue.GetValues(), relayMaxAttrs)
		out := vals[:0]

		for _, e := range vals {
			if s := sanitizeValue(e, depth+1); s != nil {
				out = append(out, s)
			}
		}

		x.ArrayValue.Values = out
	case *commonpb.AnyValue_KvlistValue:
		if depth >= relayMaxDepth || x.KvlistValue == nil {
			return nil
		}

		x.KvlistValue.Values = sanitizeKvlist(x.KvlistValue.GetValues(), depth+1)
	case *commonpb.AnyValue_StringValueStrindex:
		// A dictionary reference: no dictionary exists in these signals.
		return nil
	}

	return v
}

func sanitizeKvlist(in []*commonpb.KeyValue, depth int) []*commonpb.KeyValue {
	in = capSlice(in, relayMaxAttrs)
	out := in[:0]

	for _, kv := range in {
		if kv == nil {
			continue
		}

		kv.Key = cut(kv.GetKey(), relayMaxKey)
		kv.KeyStrindex = 0

		if kv.GetValue() != nil {
			kv.Value = sanitizeValue(kv.GetValue(), depth)
		}

		out = append(out, kv)
	}

	return out
}

func capSlice[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}

	return s
}

// cut shortens s to at most n bytes without splitting a UTF-8 sequence
// (protobuf strings must be valid UTF-8, and Unmarshal has checked that they are).
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n]
}
