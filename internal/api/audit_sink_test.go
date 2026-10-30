// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
)

// auditSink is an audit store that keeps the rows the logger writes.
type auditSink struct {
	mu   sync.Mutex
	rows []*models.AuditLog
}

func (s *auditSink) InsertAuditLog(_ context.Context, event *models.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rows = append(s.rows, event)

	return nil
}

// recordAudit installs an audit logger writing to a fresh sink as the process
// default (tests using it must not run in parallel). The returned function
// drains the logger and returns every row written so far, in order.
func recordAudit(t *testing.T) func() []*models.AuditLog {
	t.Helper()

	sink := &auditSink{}
	l := audit.NewLogger(sink, newFixTestLogger(t), nil, 64)
	audit.SetDefault(l)

	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = l.Shutdown(ctx)
	}

	t.Cleanup(func() {
		audit.SetDefault(nil)
		stop()
	})

	return func() []*models.AuditLog {
		stop()

		sink.mu.Lock()
		defer sink.mu.Unlock()

		return slices.Clone(sink.rows)
	}
}

// auditActions lists the action of each row.
func auditActions(rows []*models.AuditLog) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action)
	}

	return out
}

// auditMessage returns the free-text details of a row.
func auditMessage(t *testing.T, row *models.AuditLog) string {
	t.Helper()

	var d struct {
		Message string `json:"message"`
	}

	if err := json.Unmarshal(row.DetailsJSON, &d); err != nil {
		t.Fatalf("audit details %s: %v", row.DetailsJSON, err)
	}

	return d.Message
}
