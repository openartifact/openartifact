// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
)

type cleanupAuditSink struct{ rows chan *models.AuditLog }

func (s *cleanupAuditSink) InsertAuditLog(_ context.Context, e *models.AuditLog) error {
	s.rows <- e
	return nil
}

func recordCleanupAudit(t *testing.T) func() []*models.AuditLog {
	t.Helper()

	sink := &cleanupAuditSink{rows: make(chan *models.AuditLog, 8)}
	l := audit.NewLogger(sink, nil, nil, 8)
	audit.SetDefault(l)

	t.Cleanup(func() { audit.SetDefault(nil) })

	return func() []*models.AuditLog {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = l.Shutdown(ctx)
		close(sink.rows)

		var out []*models.AuditLog
		for r := range sink.rows {
			out = append(out, r)
		}

		return out
	}
}

func TestAuditCleanupRunRecordsCountsAgainstTheRepository(t *testing.T) {
	rows := recordCleanupAudit(t)
	policy := &models.CleanupPolicy{ID: "p1", Name: "keep-5"}
	repo := &models.Repository{ID: "r1", Name: "libs"}

	auditCleanupRun(context.Background(), policy, repo, &cleanupRun{hard: 2}, 5, nil)

	got := rows()
	if len(got) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(got))
	}

	r := got[0]
	if r.Action != audit.ActionCleanupRunCompleted || r.RepositoryID == nil || *r.RepositoryID != "r1" {
		t.Errorf("row = %+v", r)
	}

	for _, want := range []string{"keep-5", "removed 5 versions", "2 permanently", "3 moved to the trash"} {
		if !strings.Contains(string(r.DetailsJSON), want) {
			t.Errorf("details %s lack %q", r.DetailsJSON, want)
		}
	}
}

func TestAuditCleanupRunSkipsEmptyRunsButNotFailures(t *testing.T) {
	rows := recordCleanupAudit(t)
	policy := &models.CleanupPolicy{ID: "p1", Name: "keep-5"}
	repo := &models.Repository{ID: "r1", Name: "libs"}

	auditCleanupRun(context.Background(), policy, repo, newCleanupRun(), 0, nil)
	auditCleanupRun(context.Background(), policy, repo, nil, 0, errors.New("connection refused to 10.0.0.1"))

	got := rows()
	if len(got) != 1 {
		t.Fatalf("audit rows = %d, want only the failed run", len(got))
	}

	if !strings.Contains(string(got[0].DetailsJSON), "failed") || strings.Contains(string(got[0].DetailsJSON), "10.0.0.1") {
		t.Errorf("details = %s, want a failure without the error text", got[0].DetailsJSON)
	}
}
