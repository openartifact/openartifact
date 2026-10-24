// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestParseDescMatchesClientGolang(t *testing.T) {
	t.Parallel()

	d := prometheus.NewDesc("x_total", `help with "quotes" and | pipe`, []string{"a", "b"}, nil)

	got, err := parseDesc(d.String())
	if err != nil {
		t.Fatal(err)
	}

	want := describedMetric{Name: "x_total", Help: `help with "quotes" and | pipe`, Labels: []string{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseDesc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    describedMetric
		wantErr string
	}{
		{
			name: "no labels",
			in:   `Desc{fqName: "m", help: "h", constLabels: {}, variableLabels: {}}`,
			want: describedMetric{Name: "m", Help: "h"},
		},
		{
			name: "unit field",
			in:   `Desc{fqName: "m", help: "h", unit: "", constLabels: {}, variableLabels: {x,y}}`,
			want: describedMetric{Name: "m", Help: "h", Labels: []string{"x", "y"}},
		},
		{
			name: "constrained label",
			in:   `Desc{fqName: "m", help: "h", unit: "", constLabels: {}, variableLabels: {c(x)}}`,
			want: describedMetric{Name: "m", Help: "h", Labels: []string{"x"}},
		},
		{
			name:    "const labels unsupported",
			in:      `Desc{fqName: "m", help: "h", constLabels: {a="b"}, variableLabels: {}}`,
			wantErr: "constant labels",
		},
		{
			name:    "garbage",
			in:      `nope`,
			wantErr: "unrecognised Desc format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDesc(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// writeGo writes one Go file under dir/pkg and returns the root.
func writeGo(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()

	for name, src := range files {
		p := filepath.Join(root, "internal", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func TestScanMetricSources(t *testing.T) {
	t.Parallel()

	const consts = `package p

import "github.com/prometheus/client_golang/prometheus"

var buckets = []float64{.1, 1, 10}

type c struct{ lag, total, other *prometheus.Desc }

func newC() *c {
	return &c{
		lag:   prometheus.NewDesc("lag_seconds", "h", nil, nil),
		total: prometheus.NewDesc("things_total", "h", nil, nil),
	}
}

func (c *c) Collect(ch chan<- prometheus.Metric) {
	gauge := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	gauge(c.lag, 1)
	ch <- prometheus.MustNewConstMetric(c.total, prometheus.CounterValue, 2)
}

var (
	_ = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "a_total", Help: "h"}, nil)
	_ = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "h_seconds", Buckets: buckets}, nil)
	_ = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "ns", Subsystem: "sub", Name: "def"})
	_ = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "exp", Buckets: prometheus.ExponentialBuckets(1, 2, 3)})
	_ = prometheus.NewGauge(prometheus.GaugeOpts{Name: "g"})
)
`

	root := writeGo(t, map[string]string{
		"p/p.go": consts,
		// test files and the generator itself are ignored
		"p/p_test.go": `package p

import "github.com/prometheus/client_golang/prometheus"

var _ = prometheus.NewGauge(prometheus.GaugeOpts{Name: "ignored"})
`,
	})

	got, err := scanMetricSources(root, []string{"internal"})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]struct {
		typ     string
		buckets []float64
	}{
		"lag_seconds":  {"gauge", nil},
		"things_total": {"counter", nil},
		"a_total":      {"counter", nil},
		"h_seconds":    {"histogram", []float64{.1, 1, 10}},
		"ns_sub_def":   {"histogram", prometheus.DefBuckets},
		"exp":          {"histogram", []float64{1, 2, 4}},
		"g":            {"gauge", nil},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d metrics %v, want %d", len(got), got, len(want))
	}

	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Fatalf("%s missing", name)
		}

		if g.Type != w.typ || !reflect.DeepEqual(g.Buckets, w.buckets) {
			t.Errorf("%s: got %s %v, want %s %v", name, g.Type, g.Buckets, w.typ, w.buckets)
		}
	}
}

func TestScanMetricSourcesErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "desc never emitted",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
type c struct{ d *prometheus.Desc }
var _ = c{d: prometheus.NewDesc("x", "h", nil, nil)}`,
			want: "no MustNewConstMetric",
		},
		{
			name: "desc not assigned",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
func f() { prometheus.NewDesc("x", "h", nil, nil) }`,
			want: "not assigned",
		},
		{
			name: "desc emitted with two types",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
type c struct{ d *prometheus.Desc }
var _ = c{d: prometheus.NewDesc("x", "h", nil, nil)}
func (c *c) f(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.d, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.d, prometheus.CounterValue, 1)
}`,
			want: "several",
		},
		{
			name: "non literal name",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
const n = "x"
var _ = prometheus.NewGauge(prometheus.GaugeOpts{Name: n})`,
			want: "not a string literal",
		},
		{
			name: "opts not literal",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var o = prometheus.GaugeOpts{Name: "x"}
var _ = prometheus.NewGauge(o)`,
			want: "not a literal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := writeGo(t, map[string]string{"p/p.go": tc.src})

			_, err := scanMetricSources(root, []string{"internal"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMergeMetrics(t *testing.T) {
	t.Parallel()

	described := []describedMetric{{Name: "b_total", Help: "hb"}, {Name: "a", Help: "ha", Labels: []string{"l"}}}
	found := map[string]sourceMetric{
		"a":       {Name: "a", Type: "gauge"},
		"b_total": {Name: "b_total", Type: "counter"},
	}

	defs, err := mergeMetrics(described, found)
	if err != nil {
		t.Fatal(err)
	}

	if len(defs) != 2 || defs[0].Name != "a" || defs[0].Type != "gauge" || defs[1].Type != "counter" {
		t.Fatalf("defs = %+v", defs)
	}

	t.Run("unclassifiable", func(t *testing.T) {
		t.Parallel()

		_, err := mergeMetrics(described, map[string]sourceMetric{"a": {Name: "a", Type: "gauge"}})
		if err == nil || !strings.Contains(err.Error(), "cannot classify b_total") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unregistered", func(t *testing.T) {
		t.Parallel()

		extra := map[string]sourceMetric{
			"a": {Name: "a", Type: "gauge"}, "b_total": {Name: "b_total", Type: "counter"},
			"new_total": {Name: "new_total", Type: "counter", Where: "internal/x/y.go:3"},
		}

		_, err := mergeMetrics(described, extra)
		if err == nil || !strings.Contains(err.Error(), "new_total (internal/x/y.go:3)") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRenderMetrics(t *testing.T) {
	t.Parallel()

	page, err := renderMetrics([]metricDef{
		{Name: "openartifact_http_requests_total", Type: "counter", Labels: []string{"method", "path"}, Help: "a | b <c>"},
		{Name: "openartifact_http_request_duration_seconds", Type: "histogram", Buckets: []float64{.005, 1, 10}, Help: "latency"},
		{Name: "openartifact_zzz_new", Type: "gauge", Help: "new"},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := string(page)
	for _, want := range []string{
		"## HTTP\n",
		"## Zzz\n",
		"| `openartifact_http_requests_total` | counter | `method`, `path` | a \\| b &lt;c&gt; |",
		"| `openartifact_http_request_duration_seconds` | histogram | none | latency |",
		"Buckets of `openartifact_http_request_duration_seconds`: 0.005, 1, 10.",
		"(../operations/monitoring.md#metrics)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q:\n%s", want, out)
		}
	}

	if _, err := renderMetrics(nil); err == nil {
		t.Error("empty input must fail")
	}

	if _, err := renderMetrics([]metricDef{{Name: "openartifact_x_y", Type: "gauge", Help: "{{ x }}"}}); err == nil {
		t.Error("template delimiters must fail")
	}
}

func TestMetricGroup(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"openartifact_http_requests_total": "http",
		"openartifact_db_pool_open":        "db",
		"openartifact_single":              "single",
		"other_name_total":                 "other",
	}

	for in, want := range tests {
		if got := metricGroup(in); got != want {
			t.Errorf("metricGroup(%q) = %q, want %q", in, got, want)
		}
	}
}
