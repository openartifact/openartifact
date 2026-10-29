// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

type auditCaptureDB struct {
	databasetest.BaseMockDB

	events chan *models.AuditLog
}

func (db *auditCaptureDB) InsertAuditLog(_ context.Context, event *models.AuditLog) error {
	db.events <- event
	return nil
}

func TestAuditMiddlewareClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trusted    *TrustedProxies
		remoteAddr string
		xff        string
		want       string
	}{
		{
			name:       "direct peer",
			remoteAddr: "203.0.113.10:12345",
			want:       "203.0.113.10",
		},
		{
			name:       "untrusted peer ignores spoofed XFF",
			trusted:    NewTrustedProxies([]string{"10.0.0.0/8"}),
			remoteAddr: "203.0.113.10:12345",
			xff:        "198.51.100.20",
			want:       "203.0.113.10",
		},
		{
			name:       "trusted proxy chain uses first client",
			trusted:    NewTrustedProxies([]string{"10.0.0.0/8"}),
			remoteAddr: "10.0.0.2:443",
			xff:        "198.51.100.20, 10.0.0.1",
			want:       "198.51.100.20",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &auditCaptureDB{events: make(chan *models.AuditLog, 1)}
			logger := audit.NewLogger(db, nil, nil, 1)
			defer func() { _ = logger.Shutdown(context.Background()) }()

			handler := AuditMiddleware(logger, tt.trusted)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			handler.ServeHTTP(httptest.NewRecorder(), req)

			select {
			case event := <-db.events:
				if event.IPAddress == nil || *event.IPAddress != tt.want {
					t.Errorf("IPAddress = %v, want %q", event.IPAddress, tt.want)
				}
			case <-time.After(time.Second):
				t.Fatal("audit event was not recorded")
			}
		})
	}
}

// TestAuditMiddlewareSkipsTelemetryRelay: browser telemetry exports are not
// audited. Events are written in order, so the first one recorded after the
// relay request must be the following request's.
func TestAuditMiddlewareSkipsTelemetryRelay(t *testing.T) {
	db := &auditCaptureDB{events: make(chan *models.AuditLog, 2)}
	logger := audit.NewLogger(db, nil, nil, 1)
	defer func() { _ = logger.Shutdown(context.Background()) }()

	r := chi.NewRouter()
	r.Use(AuditMiddleware(logger))
	noop := func(http.ResponseWriter, *http.Request) {}
	r.Post("/api/v1/telemetry/{signal}", noop)
	r.Post("/api/v1/repositories", noop)

	for _, path := range []string{"/api/v1/telemetry/traces", "/api/v1/repositories"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, nil))
	}

	select {
	case event := <-db.events:
		if strings.Contains(string(event.DetailsJSON), "telemetry") {
			t.Fatalf("telemetry relay request audited: %s", event.DetailsJSON)
		}
	case <-time.After(time.Second):
		t.Fatal("audit event of the following request was not recorded")
	}
}

// The middleware records the request id, status, username and token of its own
// event and attaches them to the context, so a handler's event carries them.
func TestAuditMiddlewareRecordsRequest(t *testing.T) {
	db := &auditCaptureDB{events: make(chan *models.AuditLog, 2)}
	logger := audit.NewLogger(db, nil, nil, 2)

	userID := "u-1"
	p := &auth.Principal{UserID: &userID, Username: "alice", TokenKind: auth.TokenKindAccess, TokenID: "tok-1"}

	handler := AuditMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Log(r.Context(), audit.AuditEvent{Action: audit.ActionUserCreated})
		w.WriteHeader(http.StatusForbidden)
	}))

	req := httptest.NewRequestWithContext(WithPrincipal(context.Background(), p), http.MethodPost, "/", nil)
	req.Header.Set("X-Request-ID", "req-9")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if err := logger.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := map[string]map[string]any{
		audit.ActionUserCreated: {"outcome": "success", "username": "alice", "token_id": "tok-1", "request_id": "req-9"},
		audit.ActionHTTPRequest: {
			"outcome": "failure", "username": "alice", "token_id": "tok-1", "request_id": "req-9", "status": float64(403),
		},
	}

	if len(db.events) != len(want) {
		t.Fatalf("recorded %d events, want %d", len(db.events), len(want))
	}

	for range want {
		event := <-db.events

		var details map[string]any
		if err := json.Unmarshal(event.DetailsJSON, &details); err != nil {
			t.Fatal(err)
		}

		for k, v := range want[event.Action] {
			if details[k] != v {
				t.Errorf("%s: details[%s] = %v, want %v", event.Action, k, details[k], v)
			}
		}
	}
}
