// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Status is the journal state served by GET /api/v1/admin/recovery/status.
type Status struct {
	// Enabled is the journal switch the database triggers consult.
	Enabled bool `json:"enabled"`
	// Backlog is the number of changed rows waiting for their sidecar (capped
	// at BacklogCap); marks in retry backoff are included.
	Backlog    int64 `json:"backlog"`
	BacklogCap int64 `json:"backlog_cap"`
	// OldestDirtyAgeSeconds is the age of the oldest waiting mark (0 when drained).
	OldestDirtyAgeSeconds float64 `json:"oldest_dirty_age_seconds"`
	// RetryingRows is the number of marks that failed and wait for their backoff.
	RetryingRows int64 `json:"retrying_rows"`
	// SidecarsTracked is how many sidecar blobs the journal knows about.
	SidecarsTracked int64 `json:"sidecars_tracked"`
	// LastError is the most recent journal failure ("" when none).
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	// LastDrainAt is when the worker last emptied the queue.
	LastDrainAt *time.Time `json:"last_drain_at,omitempty"`
	// LastReconcileAt is when every table last completed a reconcile pass.
	LastReconcileAt *time.Time `json:"last_reconcile_at,omitempty"`
	// TablesReconciled / TablesTotal show the first backfill's progress.
	TablesReconciled int `json:"tables_reconciled"`
	TablesTotal      int `json:"tables_total"`
	// SchemaVersion is the migration number stamped into new sidecars.
	SchemaVersion int `json:"schema_version"`
}

// Status reads the journal state and refreshes the gauges.
func (j *Journal) Status(ctx context.Context) (Status, error) {
	st := Status{BacklogCap: backlogCountCap, SchemaVersion: j.schema, TablesTotal: len(Tables)}

	backlog, age, err := j.backlog(ctx)
	if err != nil {
		return st, err
	}

	st.Backlog, st.OldestDirtyAgeSeconds = backlog, age.Seconds()

	var lastErr sql.NullString

	var errAt, drainAt, recAt sql.NullTime

	err = j.db.QueryRowContext(ctx, `
		SELECT enabled, last_error, last_error_at, last_drain_at, last_reconcile_at
		FROM recovery_state WHERE id`).Scan(&st.Enabled, &lastErr, &errAt, &drainAt, &recAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, fmt.Errorf("read recovery state: %w", err)
	}

	st.LastError = lastErr.String
	st.LastErrorAt, st.LastDrainAt, st.LastReconcileAt = nullTime(errAt), nullTime(drainAt), nullTime(recAt)

	if err := j.db.QueryRowContext(ctx,
		`SELECT count(*) FROM recovery_dirty WHERE retry_at IS NOT NULL AND retry_at > now()`).Scan(&st.RetryingRows); err != nil {
		return st, fmt.Errorf("count retrying recovery rows: %w", err)
	}

	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM recovery_sidecars`).Scan(&st.SidecarsTracked); err != nil {
		return st, fmt.Errorf("count tracked sidecars: %w", err)
	}

	if err := j.db.QueryRowContext(ctx,
		`SELECT count(*) FROM recovery_reconcile WHERE last_completed_at IS NOT NULL AND table_name = ANY($1::text[])`,
		tableNames()).Scan(&st.TablesReconciled); err != nil {
		return st, fmt.Errorf("count reconciled tables: %w", err)
	}

	if j.m != nil {
		j.m.JournalBacklog.Set(float64(st.Backlog))
		j.m.OldestDirtyAgeSeconds.Set(st.OldestDirtyAgeSeconds)
	}

	return st, nil
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}

	return &t.Time
}
