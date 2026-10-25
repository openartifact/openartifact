// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
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
