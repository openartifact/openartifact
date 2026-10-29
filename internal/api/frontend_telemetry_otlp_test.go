// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	colllogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// browserResource is what the web UI sends as its resource, plus a claim to be
// the server.
func browserResource() *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		strAttr("service.name", "openartifact"),
		strAttr("service.version", "1.2.3"),
		strAttr("telemetry.sdk.name", "opentelemetry"),
		strAttr("deployment.environment", "prod"),
	}}
}

func newSpan(i int) *tracepb.Span {
	tid := make([]byte, traceIDLen)
	tid[0] = 1
	sid := make([]byte, spanIDLen)
	sid[0] = byte(i + 1)

	return &tracepb.Span{
		TraceId: tid, SpanId: sid, Name: "HTTP GET", Kind: tracepb.Span_SPAN_KIND_CLIENT,
		Attributes: []*commonpb.KeyValue{strAttr("http.request.method", "GET")},
	}
}

func newTraceRequest(spans ...*tracepb.Span) *colltrace.ExportTraceServiceRequest {
	return &colltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: browserResource(), ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
}

func newLogsRequest(recs ...*logspb.LogRecord) *colllogs.ExportLogsServiceRequest {
	return &colllogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: browserResource(), ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}},
	}}}
}

func newLogRecord() *logspb.LogRecord {
	return &logspb.LogRecord{
		EventName: "exception",
		Body:      &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "msg"}},
	}
}

func mustMarshal(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}

	return b
}

func tracePayload(n int) []byte {
	spans := make([]*tracepb.Span, n)
	for i := range spans {
		spans[i] = newSpan(i)
	}

	return mustMarshal(newTraceRequest(spans...))
}

func logsPayload(n int) []byte {
	recs := make([]*logspb.LogRecord, n)
	for i := range recs {
		recs[i] = newLogRecord()
	}

	return mustMarshal(newLogsRequest(recs...))
}

func attrMap(kvs []*commonpb.KeyValue) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		m[kv.GetKey()] = kv.GetValue().GetStringValue()
	}

	return m
}

func TestSanitizeOTLPTraces(t *testing.T) {
	long := strings.Repeat("é", relayMaxValue) // 2 bytes each, cut must stay valid UTF-8

	sp := newSpan(0)
	sp.Name = strings.Repeat("n", relayMaxName*2)
	sp.Attributes = nil

	for i := range relayMaxAttrs * 2 {
		sp.Attributes = append(sp.Attributes, strAttr("k"+strings.Repeat("x", relayMaxKey)+string(rune('a'+i%26)), long))
	}

	for range relayMaxEvents * 2 {
		sp.Events = append(sp.Events, &tracepb.Span_Event{Name: "e"})
	}

	out, err := sanitizeOTLP("traces", mustMarshal(newTraceRequest(sp)))
	if err != nil {
		t.Fatal(err)
	}

	var got colltrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(out, &got); err != nil {
		t.Fatalf("output does not parse (an invalid UTF-8 cut?): %v", err)
	}

	rs := got.GetResourceSpans()[0]

	// The resource claims to be the server and carries extra attributes: it is
	// replaced by the web UI's identity.
	if res := attrMap(
		rs.GetResource().GetAttributes(),
	); len(res) != 2 || res["service.name"] != frontendServiceName ||
		res["service.version"] != "1.2.3" {
		t.Errorf("resource = %v, want service.name %q and service.version only", res, frontendServiceName)
	}

	span := rs.GetScopeSpans()[0].GetSpans()[0]
	if len(span.GetName()) != relayMaxName || len(span.GetAttributes()) != relayMaxAttrs || len(span.GetEvents()) != relayMaxEvents {
		t.Errorf("span name %d bytes, %d attributes, %d events; want %d, %d, %d",
			len(span.GetName()), len(span.GetAttributes()), len(span.GetEvents()), relayMaxName, relayMaxAttrs, relayMaxEvents)
	}

	for _, kv := range span.GetAttributes() {
		if len(kv.GetKey()) > relayMaxKey || len(kv.GetValue().GetStringValue()) > relayMaxValue {
			t.Fatalf("attribute %q holds %d bytes", kv.GetKey()[:8], len(kv.GetValue().GetStringValue()))
		}
	}
}

func TestSanitizeOTLPRefusals(t *testing.T) {
	tooMany := make([]*tracepb.Span, relayMaxRecords+1)
	for i := range tooMany {
		tooMany[i] = newSpan(0)
	}

	badID := newSpan(0)
	badID.TraceId = []byte{1, 2, 3}

	tooManyLogs := make([]*logspb.LogRecord, relayMaxRecords+1)
	for i := range tooManyLogs {
		tooManyLogs[i] = newLogRecord()
	}

	badLog := newLogRecord()
	badLog.SpanId = []byte{1}

	tests := []struct {
		name, signal string
		body         []byte
	}{
		{"not protobuf", "traces", []byte("GET / HTTP/1.1\r\n")},
		{"truncated", "logs", logsPayload(2)[:10]},
		{"too many spans", "traces", mustMarshal(newTraceRequest(tooMany...))},
		{"malformed span ids", "traces", mustMarshal(newTraceRequest(badID))},
		{"too many log records", "logs", mustMarshal(newLogsRequest(tooManyLogs...))},
		{"malformed log ids", "logs", mustMarshal(newLogsRequest(badLog))},
	}

	for _, tc := range tests {
		if _, err := sanitizeOTLP(tc.signal, tc.body); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}

	// A full normal export goes through.
	if _, err := sanitizeOTLP("traces", tracePayload(64)); err != nil {
		t.Errorf("64 spans refused: %v", err)
	}

	if _, err := sanitizeOTLP("logs", logsPayload(64)); err != nil {
		t.Errorf("64 log records refused: %v", err)
	}
}

func TestSanitizeOTLPLogValues(t *testing.T) {
	deep := &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "leaf"}}
	for range relayMaxDepth + 2 {
		deep = &commonpb.AnyValue{
			Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.AnyValue{deep}}},
		}
	}

	rec := newLogRecord()
	rec.Body = deep
	rec.Attributes = []*commonpb.KeyValue{
		{Key: "list", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{}}},
		{Key: "bytes", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: make([]byte, relayMaxValue*2)}}},
	}

	out, err := sanitizeOTLP("logs", mustMarshal(newLogsRequest(rec)))
	if err != nil {
		t.Fatal(err)
	}

	var got colllogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	lr := got.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
	if strings.Contains(lr.GetBody().String(), "leaf") {
		t.Errorf("a body nested deeper than %d survived: %v", relayMaxDepth, lr.GetBody())
	}

	for _, kv := range lr.GetAttributes() {
		if n := len(kv.GetValue().GetBytesValue()); n > relayMaxValue {
			t.Errorf("bytes attribute holds %d bytes", n)
		}
	}
}

// TestFrontendTelemetryRelayRefusesInvalidPayloads: what is not a valid,
// bounded OTLP export never reaches the collector, and is not counted as
// relayed.
func TestFrontendTelemetryRelayRefusesInvalidPayloads(t *testing.T) {
	collector := newFakeCollector(t)

	tooMany := make([]*tracepb.Span, relayMaxRecords+1)
	for i := range tooMany {
		tooMany[i] = newSpan(0)
	}

	for name, req := range map[string]*http.Request{
		"garbage":       relayRequest("traces", otlpProtobuf, []byte("not otlp at all")),
		"too many":      relayRequest("traces", otlpProtobuf, mustMarshal(newTraceRequest(tooMany...))),
		"logs as trace": relayRequest("traces", otlpProtobuf, []byte{0x0a, 0x03, 'a', 'b', 'c'}),
	} {
		rec := httptest.NewRecorder()
		newFrontendRouter(frontendConfig(collector.srv.URL), nil, sessionPrincipal("user-1")).ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}

		if collector.take() != nil {
			t.Errorf("%s: forwarded to the collector", name)
		}
	}
}

// TestFrontendTelemetryRelayRateLimitsPerUser: a user past the burst gets 429
// with Retry-After while another user is unaffected, and the bucket refills.
func TestFrontendTelemetryRelayRateLimitsPerUser(t *testing.T) {
	collector := newFakeCollector(t)
	h := NewFrontendTelemetryHandler(frontendConfig(collector.srv.URL), nil, nil)

	now := time.Now()
	h.limit.now = func() time.Time { return now }

	serve := func(user string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		routerFor(h, sessionPrincipal(user)).ServeHTTP(rec, relayRequest("logs", otlpProtobuf, logsPayload(1)))

		return rec
	}

	for i := range frontendTelemetryBurst {
		if rec := serve("user-1"); rec.Code != http.StatusOK {
			t.Fatalf("request %d within the burst answered %d", i, rec.Code)
		}
	}

	rec := serve("user-1")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over the burst = %d (Retry-After %q), want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}

	if rec := serve("user-2"); rec.Code != http.StatusOK {
		t.Errorf("another user answered %d, want 200", rec.Code)
	}

	now = now.Add(time.Second)

	if rec := serve("user-1"); rec.Code != http.StatusOK {
		t.Errorf("after the bucket refilled = %d, want 200", rec.Code)
	}
}
