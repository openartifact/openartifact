// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// testTicket has the shape of a Terraform download ticket: base64url payload,
// dot, base64url HMAC-SHA256 signature.
const testTicket = "djF8cmVwby0xfHByb3ZpZGVyfGhhc2hpY29ycC9hd3N8NS4wLjB8bGludXhfYW1kNjR8MTc2MDAwMDAwMA.Kx9Zq3v1T0m7yJc2p4n8R5bWd6hA1sLfE2gU0oYtVxI"

func TestScrubText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"short text untouched", "no such file", "no such file"},
		{
			"userinfo and query",
			`Get "https://user:pw@registry.example.com/v1/x?token=abc&sig=1": EOF`,
			`Get "https://registry.example.com/v1/x": EOF`,
		},
		{"fragment", "see https://example.com/a#frag.", "see https://example.com/a."},
		{
			"ticket in url path",
			"fetch https://oa.example.com/terraform/dl/" + testTicket + "/p.zip failed",
			"fetch https://oa.example.com/terraform/dl/[redacted]/p.zip failed",
		},
		{"bare ticket", "bad ticket " + testTicket, "bad ticket [redacted]"},
		{"quoted path query", "GET /api/v1/x?access_token=abc: 500", "GET /api/v1/x?[redacted] 500"},
		{"unparsable url", "dial http://[::1: bad", "dial [redacted]: bad"},
		{
			"long host names stay",
			"lookup my-long-service-name.my-long-namespace-name.svc: no such host",
			"lookup my-long-service-name.my-long-namespace-name.svc: no such host",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubText(tc.in); got != tc.want {
				t.Errorf("scrubText(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestScrubPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/api/v1/repos", "/api/v1/repos"},
		{"/api/v1/repos?x=1", "/api/v1/repos"},
		{"/terraform/dl/" + testTicket + "/p.zip", "/terraform/dl/[redacted]/p.zip"},
		{"/t/terraform/dl/opaque/p.zip", "/t/terraform/dl/[redacted]/p.zip"},
		{"/x/" + testTicket, "/x/[redacted]"},
		{"/npm/left-pad/-/left-pad-1.3.0.tgz", "/npm/left-pad/-/left-pad-1.3.0.tgz"},
	}

	for _, tc := range tests {
		if got := scrubPath(tc.in); got != tc.want {
			t.Errorf("scrubPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

type stringerURL struct{ u string }

func (s stringerURL) String() string { return s.u }

type panicStringer struct{}

func (*panicStringer) String() string { panic("boom") }

func TestScrubFields(t *testing.T) {
	in := []zapcore.Field{
		zap.String("method", "GET"),
		zap.String("path", "/terraform/dl/"+testTicket+"/p.zip"),
		zap.String("url", "https://u:p@h.example.com/a?k=v"),
		zap.String("query", "token=abc"),
		zap.String("note", "plain"),
		zap.Error(errors.New(`Get "https://h.example.com/a?sig=zzz": EOF`)),
		zap.NamedError("cause", errors.New("x https://u:p@h/b?q=1")),
		zap.Stringer("href", stringerURL{"https://h.example.com/c?q=1"}),
		zap.Stringer("nilish", (*panicStringer)(nil)),
		zap.Int("status", 500),
	}
	orig := append([]zapcore.Field(nil), in...)

	got := map[string]string{}
	for _, f := range scrubFields(in) {
		if f.Type == zapcore.StringType {
			got[f.Key] = f.String
		}
	}

	want := map[string]string{
		"method":            "GET",
		"path":              "/terraform/dl/[redacted]/p.zip",
		"url":               "https://h.example.com/a",
		"query":             redacted,
		"note":              "plain",
		"exception.type":    "*errors.errorString",
		"exception.message": `Get "https://h.example.com/a": EOF`,
		"cause":             "x https://h/b",
		"href":              "https://h.example.com/c",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %s = %q, want %q", k, got[k], v)
		}
	}

	for i := range in {
		if fmt.Sprint(in[i]) != fmt.Sprint(orig[i]) {
			t.Errorf("input field %d modified: %v, was %v", i, in[i], orig[i])
		}
	}

	// Untouched fields are returned as they came, without a copy.
	clean := []zapcore.Field{zap.String("a", "b"), zap.Int("n", 1)}
	if out := scrubFields(clean); &out[0] != &clean[0] {
		t.Error("clean fields were copied")
	}
}

// TestLogCoreScrubsExportedRecords pins the boundary: what the bridge exports
// carries no userinfo, query, ticket or raw error text, in per-call fields, in
// fields bound with With, and in the message.
func TestLogCoreScrubsExportedRecords(t *testing.T) {
	install(t)

	exp := &recordingExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	otel.SetLoggerProvider(lp)
	t.Cleanup(func() {
		otel.SetLoggerProvider(lognoop.NewLoggerProvider())
		_ = lp.Shutdown(context.Background())
	})

	log := zap.New(LogCore()).Sugar()

	secretErr := fmt.Errorf(
		"upstream: %w",
		&url.Error{Op: "Get", URL: "https://user:hunter2@up.example.com/p?X-Amz-Signature=sekrit", Err: errors.New("EOF")},
	)

	log.With("upstream_url", "https://user:hunter2@up.example.com/p?sig=sekrit").
		Errorw("proxy fetch failed https://user:hunter2@up.example.com/m?k=sekrit",
			"path", "/terraform/dl/"+testTicket+"/p.zip",
			"query", "token=sekrit",
			"error", secretErr)

	exp.mu.Lock()
	defer exp.mu.Unlock()

	if len(exp.records) != 1 {
		t.Fatalf("%d records, want 1", len(exp.records))
	}

	r := exp.records[0]
	attrs := map[string]string{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		attrs[string(kv.Key)] = kv.Value.String()

		return true
	})

	parts := []string{r.Body().String()}
	for _, v := range attrs {
		parts = append(parts, v)
	}

	all := strings.Join(parts, "\n")

	for _, leak := range []string{"hunter2", "sekrit", "X-Amz-Signature", testTicket} {
		if strings.Contains(all, leak) {
			t.Errorf("exported record still holds %q:\n%s", leak, all)
		}
	}

	if got := attrs["path"]; got != "/terraform/dl/[redacted]/p.zip" {
		t.Errorf("path = %q", got)
	}

	if got := attrs["upstream_url"]; got != "https://up.example.com/p" {
		t.Errorf("upstream_url = %q", got)
	}

	if got := attrs["exception.type"]; got != "*fmt.wrapError" {
		t.Errorf("exception.type = %q, want the error's type kept", got)
	}

	if !strings.Contains(attrs["exception.message"], "https://up.example.com/p") {
		t.Errorf("exception.message = %q, want the scrubbed URL kept", attrs["exception.message"])
	}
}
