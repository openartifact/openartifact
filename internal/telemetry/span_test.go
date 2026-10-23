// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

func TestStartSpan(t *testing.T) {
	rec := install(t)

	// Without a request span (a background worker) nothing is recorded.
	_, end := StartSpan(t.Context(), "storage.put")
	end(nil)

	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("%d spans without a parent request span", n)
	}

	parentCtx, parent := otel.Tracer(scope).Start(t.Context(), "request")

	_, end = StartSpan(parentCtx, "storage.exists", attribute.String("blob.operation", "exists"))
	end(nil)

	_, end = StartSpan(parentCtx, "storage.put")
	end(errors.New("dial tcp 10.0.0.1:9000: https://secret.example/bucket?token=abc"))

	parent.End()

	spans := rec.Ended()
	if len(spans) != 3 {
		t.Fatalf("%d spans, want 3", len(spans))
	}

	if spans[0].Name() != "storage.exists" || spans[0].Status().Code != codes.Unset {
		t.Errorf("first span = %q status %v", spans[0].Name(), spans[0].Status().Code)
	}

	failed := spans[1]
	if failed.Status().Code != codes.Error || failed.Status().Description != "" {
		t.Errorf("failed span status = %+v, want Error without description", failed.Status())
	}

	for _, kv := range failed.Attributes() {
		if kv.Key == "error.type" && kv.Value.AsString() != "*errors.errorString" {
			t.Errorf("error.type = %q", kv.Value.AsString())
		}

		if kv.Value.Type() == attribute.STRING && kv.Key != "error.type" {
			t.Errorf("unexpected string attribute %s=%q", kv.Key, kv.Value.AsString())
		}
	}
}

func TestStartSpanDisabledIsNoOp(t *testing.T) {
	ctx, end := StartSpan(t.Context(), "storage.put")
	end(errors.New("boom"))

	if ctx != t.Context() {
		t.Error("disabled StartSpan changed the context")
	}
}
