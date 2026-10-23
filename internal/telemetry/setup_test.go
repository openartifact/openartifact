// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func newTestTracer(ratio float64) trace.Tracer {
	return sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler(ratio))).Tracer("t")
}

type export struct {
	path, auth, contentType string
	body                    []byte
}

// collector is a throwaway OTLP/HTTP receiver.
type collector struct {
	mu   sync.Mutex
	reqs []export
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)

	c.mu.Lock()
	c.reqs = append(c.reqs, export{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), b})
	c.mu.Unlock()

	w.Header().Set("Content-Type", "application/x-protobuf")
}

func (c *collector) find(path string) (export, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range c.reqs {
		if e.path == path {
			return e, true
		}
	}

	return export{}, false
}

func TestSetupExportsTracesAndMetricsOverOTLPHTTP(t *testing.T) {
	col := &collector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: "openartifact_test_total", Help: "h"}))

	cfg := DefaultConfig()
	cfg.OTLP.Enabled = true
	cfg.OTLP.Protocol = "http/protobuf"
	cfg.OTLP.Endpoint = srv.URL + "/collector/"
	cfg.OTLP.Headers = map[string]string{"Authorization": "Bearer collector-token"}
	cfg.OTLP.ServiceName = "oa-under-test"
	cfg.OTLP.Metrics = true

	shutdown, err := Setup(t.Context(), cfg, Build{Version: "9.9.9", InstanceID: "node-1"}, reg, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !Enabled() {
		t.Fatal("Enabled() = false after Setup")
	}

	_, span := otel.Tracer(scope).Start(t.Context(), "GET /repos/{name}")
	span.End()

	// Shutdown flushes the batch span processor and the periodic metric reader.
	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if Enabled() {
		t.Error("Enabled() = true after shutdown")
	}

	tr, ok := col.find("/collector/v1/traces")
	if !ok {
		t.Fatal("no trace export at <endpoint path>/v1/traces")
	}

	if tr.auth != "Bearer collector-token" || tr.contentType != "application/x-protobuf" {
		t.Errorf("trace export headers: auth=%q content-type=%q", tr.auth, tr.contentType)
	}

	for _, want := range []string{"GET /repos/{name}", "oa-under-test", "9.9.9", "node-1"} {
		if !strings.Contains(string(tr.body), want) {
			t.Errorf("trace payload lacks %q", want)
		}
	}

	m, ok := col.find("/collector/v1/metrics")
	if !ok {
		t.Fatal("no metric export at <endpoint path>/v1/metrics")
	}

	if !strings.Contains(string(m.body), "openartifact_test_total") {
		t.Error("metric payload lacks the Prometheus family bridged from the registry")
	}
}

func TestSetupTracesOnlyByDefault(t *testing.T) {
	col := &collector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.OTLP.Enabled = true
	cfg.OTLP.Protocol = ProtocolHTTP
	cfg.OTLP.Endpoint = srv.URL

	shutdown, err := Setup(t.Context(), cfg, Build{}, prometheus.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}

	_, span := otel.Tracer(scope).Start(t.Context(), "x")
	span.End()

	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, ok := col.find("/v1/metrics"); ok {
		t.Error("metrics exported although telemetry.otlp.metrics is off")
	}

	if _, ok := col.find("/v1/traces"); !ok {
		t.Error("no trace export")
	}
}

func TestSampler(t *testing.T) {
	// ratio 0 records nothing for new traces; ratio 1 records everything.
	for _, tc := range []struct {
		ratio float64
		want  bool
	}{{0, false}, {1, true}} {
		_, span := newTestTracer(tc.ratio).Start(t.Context(), "x")
		if span.IsRecording() != tc.want {
			t.Errorf("ratio %v: recording = %v, want %v", tc.ratio, span.IsRecording(), tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := func(mut func(*OTLPConfig)) Config {
		c := DefaultConfig()
		c.OTLP.Enabled = true
		mut(&c.OTLP)

		return c
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"defaults", ok(func(*OTLPConfig) {}), false},
		{"disabled ignores junk", Config{OTLP: OTLPConfig{Protocol: "carrier-pigeon", SampleRatio: 7}}, false},
		{"bad protocol", ok(func(o *OTLPConfig) { o.Protocol = "udp" }), true},
		{"http/protobuf alias", ok(func(o *OTLPConfig) { o.Protocol = "http/protobuf" }), false},
		{"ratio above 1", ok(func(o *OTLPConfig) { o.SampleRatio = 1.5 }), true},
		{"negative ratio", ok(func(o *OTLPConfig) { o.SampleRatio = -0.1 }), true},
		{"host:port", ok(func(o *OTLPConfig) { o.Endpoint = "otel-collector:4317" }), false},
		{"http url", ok(func(o *OTLPConfig) { o.Endpoint = "http://c:4318/prefix" }), false},
		{"url with userinfo", ok(func(o *OTLPConfig) { o.Endpoint = "https://u:p@c:4318" }), true},
		{"url with query", ok(func(o *OTLPConfig) { o.Endpoint = "https://c:4318?x=1" }), true},
		{"ftp url", ok(func(o *OTLPConfig) { o.Endpoint = "ftp://c" }), true},
		{"empty header name", ok(func(o *OTLPConfig) { o.Headers = map[string]string{" ": "v"} }), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
