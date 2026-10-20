// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package audit

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNewLogger_NotNil(t *testing.T) {
	t.Run("logger creation", func(t *testing.T) {
		l := NewLogger(nil, nil, nil, 100)
		if l == nil {
			t.Fatal("expected non-nil logger")
		}
	})
}

func TestNewLogger_BufferSize(t *testing.T) {
	t.Run("default buffer size", func(t *testing.T) {
		l := NewLogger(nil, nil, nil, 0)
		if l == nil {
			t.Fatal("expected non-nil logger")
		}

		if cap(l.ch) != 1000 {
			t.Errorf("expected default buffer size=1000, got %d", cap(l.ch))
		}
	})

	t.Run("custom buffer size", func(t *testing.T) {
		l := NewLogger(nil, nil, nil, 500)
		if cap(l.ch) != 500 {
			t.Errorf("expected buffer size=500, got %d", cap(l.ch))
		}
	})
}

func TestSetDefault_And_Default(t *testing.T) {
	t.Run("set and get default logger", func(t *testing.T) {
		l := &Logger{}
		SetDefault(l)

		if Default() != l {
			t.Error("Default() should return the logger set via SetDefault()")
		}
	})

	t.Run("default is nil initially", func(t *testing.T) {
		// Create a new logger and set it
		l := &Logger{}
		SetDefault(l)

		retrieved := Default()
		if retrieved == nil {
			t.Error("Default() should not be nil after SetDefault()")
		}
	})
}

func TestAuditEvent_Fields(t *testing.T) {
	t.Run("audit event structure", func(t *testing.T) {
		artifactID := "artifact-123"
		e := AuditEvent{
			ResourceID: &artifactID,
			Action:     "test.action",
			Actor:      "testuser",
			Details:    "some details",
		}

		if e.Action != "test.action" {
			t.Errorf("expected action=test.action, got %s", e.Action)
		}

		if e.Actor != "testuser" {
			t.Errorf("expected actor=testuser, got %s", e.Actor)
		}

		if e.Details != "some details" {
			t.Errorf("expected details=some details, got %s", e.Details)
		}

		if e.ResourceID == nil || *e.ResourceID != "artifact-123" {
			t.Errorf("expected resourceID=artifact-123, got %v", e.ResourceID)
		}
	})

	t.Run("audit event without artifact id", func(t *testing.T) {
		e := AuditEvent{
			ResourceID: nil,
			Action:     "system.action",
			Actor:      "system",
			Details:    "system event",
		}

		if e.ResourceID != nil {
			t.Errorf("expected resourceID=nil, got %v", e.ResourceID)
		}

		if e.Action != "system.action" {
			t.Errorf("expected action=system.action, got %s", e.Action)
		}
	})
}

func TestLogger_Shutdown(t *testing.T) {
	t.Run("shutdown closes channel", func(t *testing.T) {
		l := NewLogger(nil, nil, nil, 100)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := l.Shutdown(ctx)
		if err != nil {
			t.Errorf("Shutdown() should not fail: %v", err)
		}
	})

	t.Run("shutdown timeout", func(t *testing.T) {
		l := NewLogger(nil, nil, nil, 100)

		// Create a context that times out immediately
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()

		// Give it a moment to timeout
		time.Sleep(10 * time.Millisecond)

		err := l.Shutdown(ctx)
		if err == nil {
			t.Error("Shutdown() should fail with timeout context")
		}
	})
}

func TestLogger_Log_Channel(t *testing.T) {
	t.Run("log event to channel", func(t *testing.T) {
		// Construct directly so no goroutine is started and no DB/metrics needed.
		// Log() only touches l.reg on the drop path (channel full); a buffered
		// channel avoids that branch entirely.
		l := &Logger{
			ch:   make(chan AuditEvent, 10),
			done: make(chan struct{}),
		}

		event := AuditEvent{
			Action:  "pkg.uploaded",
			Actor:   "ci-bot",
			Details: "pushed v1.2.3",
		}

		l.Log(context.Background(), event)

		if len(l.ch) != 1 {
			t.Fatalf("expected 1 event in channel, got %d", len(l.ch))
		}

		got := <-l.ch
		if got.Action != event.Action {
			t.Errorf("expected action=%q, got %q", event.Action, got.Action)
		}
		if got.Actor != event.Actor {
			t.Errorf("expected actor=%q, got %q", event.Actor, got.Actor)
		}
	})
}

func TestAuditEvent_WithNilArtifactID(t *testing.T) {
	t.Run("event with nil artifact id", func(t *testing.T) {
		e := AuditEvent{
			ResourceID: nil,
			Action:     "repo.created",
			Actor:      "admin",
			Details:    "created new repository",
		}

		if e.ResourceID != nil {
			t.Error("expected ResourceID to be nil")
		}

		if e.Action != "repo.created" {
			t.Errorf("expected action=repo.created, got %s", e.Action)
		}
	})
}

func TestAuditEvent_WithArtifactID(t *testing.T) {
	t.Run("event with artifact id", func(t *testing.T) {
		id := "abc-123-def"
		e := AuditEvent{
			ResourceID: &id,
			Action:     "artifact.deleted",
			Actor:      "user@example.com",
			Details:    "deleted artifact",
		}

		if e.ResourceID == nil {
			t.Error("expected ResourceID to be non-nil")
		}

		if *e.ResourceID != "abc-123-def" {
			t.Errorf("expected ResourceID=abc-123-def, got %s", *e.ResourceID)
		}
	})
}

func TestLogAfterShutdownDoesNotPanic(t *testing.T) {
	l := NewLogger(nil, nil, nil, 1)
	// db is nil, so drain by closing without events.
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.Log(context.Background(), AuditEvent{Action: "x"})
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDetailsJSONValidWithControlChars(t *testing.T) {
	out := buildDetailsJSON("bad\x00\x01\"\\ msg", "us\ber\u2028")
	if !json.Valid([]byte(out)) {
		t.Fatalf("invalid JSON: %q", out)
	}
}
