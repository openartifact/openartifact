// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reconcile stages of one table.
const (
	stageMissing = "missing" // rows of the table without a tracked sidecar
	stageStale   = "stale"   // tracked sidecars whose row no longer exists
)

// reconcileState is a row of recovery_reconcile.
type reconcileState struct {
	table         string
	stage         string
	cursor        sql.NullString
	full          bool
	passStarted   sql.NullTime
	lastCompleted sql.NullTime
}

// ReconcileResult reports what a reconcile step did.
type ReconcileResult struct {
	// Marked is how many rows were marked dirty.
	Marked int
	// Idle is true when no table needed reconciling.
	Idle bool
	// Paused is true when the step was skipped because the queue is long.
	Paused bool
}

// ReconcileStep advances the reconcile by at most budget keys. A reconcile
// marks dirty (a) every row of a journaled table without a tracked sidecar,
// which backfills existing data and repairs marks lost while triggers were off
// (pg_restore --disable-triggers, TRUNCATE, a disabled journal), and (b) every
// tracked sidecar whose row no longer exists, so the worker deletes it (after
// a point-in-time restore, say). Progress is keyset-resumable per table; a
// table added to the registry by a later release is backfilled automatically
// because it has never completed a pass.
func (j *Journal) ReconcileStep(ctx context.Context, budget int) (ReconcileResult, error) {
	var res ReconcileResult

	backlog, _, err := j.backlog(ctx)
	if err != nil {
		return res, err
	}

	if backlog >= reconcileBacklogLimit {
		res.Paused = true

		return res, nil
	}

	for i := range Tables {
		t := &Tables[i]

		st, due, err := j.reconcileDue(ctx, t)
		if err != nil {
			return res, err
		}

		if !due {
			continue
		}

		marked, err := j.reconcileTable(ctx, t, st, budget)
		res.Marked += marked

		if err != nil {
			return res, err
		}

		return res, nil
	}

	res.Idle = true

	j.finishReconcileIfIdle(ctx)

	return res, nil
}

// finishReconcileIfIdle stamps recovery_state.last_reconcile_at once every
// table has completed at least one pass.
func (j *Journal) finishReconcileIfIdle(ctx context.Context) {
	_, _ = j.db.ExecContext(ctx, `
		UPDATE recovery_state SET last_reconcile_at = (SELECT max(last_completed_at) FROM recovery_reconcile)
		WHERE id AND (SELECT count(*) FROM recovery_reconcile WHERE last_completed_at IS NOT NULL) >= $1
		  AND last_reconcile_at IS DISTINCT FROM (SELECT max(last_completed_at) FROM recovery_reconcile)`, len(Tables))
}

// reconcileDue loads (creating it when missing) the state of t and reports
// whether a pass is running or due to start.
func (j *Journal) reconcileDue(ctx context.Context, t *Table) (reconcileState, bool, error) {
	if _, err := j.db.ExecContext(ctx,
		`INSERT INTO recovery_reconcile (table_name) VALUES ($1) ON CONFLICT DO NOTHING`, t.Name); err != nil {
		return reconcileState{}, false, fmt.Errorf("init reconcile state: %w", err)
	}

	st := reconcileState{table: t.Name}

	err := j.db.QueryRowContext(ctx, `
		SELECT stage, cursor, full_pass, pass_started_at, last_completed_at
		FROM recovery_reconcile WHERE table_name = $1`, t.Name).
		Scan(&st.stage, &st.cursor, &st.full, &st.passStarted, &st.lastCompleted)
	if err != nil {
		return st, false, fmt.Errorf("load reconcile state: %w", err)
	}

	switch {
	case st.passStarted.Valid:
		return st, true, nil
	case !st.lastCompleted.Valid:
		return st, true, nil
	case j.cfg.ReconcileInterval > 0 && j.now().Sub(st.lastCompleted.Time) >= j.cfg.ReconcileInterval:
		return st, true, nil
	}

	return st, false, nil
}

// reconcileTable runs up to budget keys of t's pass.
func (j *Journal) reconcileTable(ctx context.Context, t *Table, st reconcileState, budget int) (int, error) {
	if !st.passStarted.Valid {
		// Start a new pass.
		st.stage, st.cursor = stageMissing, sql.NullString{}
		st.passStarted = sql.NullTime{Time: j.now(), Valid: true}

		if _, err := j.db.ExecContext(ctx, `
			UPDATE recovery_reconcile SET stage = 'missing', cursor = NULL, pass_started_at = now()
			WHERE table_name = $1`, t.Name); err != nil {
			return 0, fmt.Errorf("start reconcile pass: %w", err)
		}
	}

	marked := 0

	for budget > 0 {
		n := min(budget, max(j.cfg.Batch, 1)*5)

		var (
			keys []string
			done bool
			m    int
			err  error
		)

		if st.stage == stageMissing {
			keys, m, err = j.reconcileMissing(ctx, t, st, n)
			done = len(keys) < n
		} else {
			keys, m, err = j.reconcileStale(ctx, t, st, n)
			done = len(keys) < n
		}

		if err != nil {
			return marked, err
		}

		marked += m
		budget -= max(len(keys), 1)

		if j.m != nil {
			j.m.ReconcileMarkedTotal.Add(float64(m))
		}

		switch {
		case !done:
			st.cursor = sql.NullString{String: keys[len(keys)-1], Valid: true}
		case st.stage == stageMissing:
			st.stage, st.cursor = stageStale, sql.NullString{}
		default:
			return marked, j.completeReconcile(ctx, t, marked)
		}

		if _, err := j.db.ExecContext(ctx, `
			UPDATE recovery_reconcile SET stage = $2, cursor = $3, marked_total = marked_total + $4
			WHERE table_name = $1`, t.Name, st.stage, st.cursor, m); err != nil {
			return marked, fmt.Errorf("save reconcile progress: %w", err)
		}
	}

	return marked, nil
}

func (j *Journal) completeReconcile(ctx context.Context, t *Table, marked int) error {
	if _, err := j.db.ExecContext(ctx, `
		UPDATE recovery_reconcile
		SET stage = 'missing', cursor = NULL, full_pass = false, pass_started_at = NULL,
		    last_completed_at = now(), marked_total = marked_total + $2
		WHERE table_name = $1`, t.Name, marked); err != nil {
		return fmt.Errorf("complete reconcile pass: %w", err)
	}

	return nil
}

// reconcileMissing reads the next n row keys of the table after the cursor and
// marks those without tracked sidecar (every one in full mode) dirty.
func (j *Journal) reconcileMissing(ctx context.Context, t *Table, st reconcileState, n int) ([]string, int, error) {
	var (
		keys  []string
		where string
		args  []any
	)

	if st.cursor.Valid {
		parts, ok := t.SplitKey(st.cursor.String)
		if !ok {
			return nil, 0, fmt.Errorf("%w: reconcile cursor of %s", ErrSidecarKey, t.Name)
		}

		ph := make([]string, len(t.Key))
		cols := make([]string, len(t.Key))

		for i, k := range t.Key {
			cast := "::text"
			if k.Type == keyUUID {
				cast = "::uuid"
			}

			cols[i] = "t." + k.Name
			ph[i] = fmt.Sprintf("$%d%s", i+1, cast)
			args = append(args, parts[i])
		}

		where = fmt.Sprintf("WHERE (%s) > (%s)", strings.Join(cols, ", "), strings.Join(ph, ", "))
	}

	q := fmt.Sprintf(`SELECT %s FROM %s t %s ORDER BY %s LIMIT %d`, t.KeyExpr("t"), t.Name, where, t.KeyCols("t"), n) //nolint:gosec // G201: identifiers come from the compiled-in table registry, values are bound

	rows, err := j.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("reconcile %s keys: %w", t.Name, err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, 0, fmt.Errorf("scan reconcile key: %w", err)
		}

		keys = append(keys, k)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reconcile %s keys: %w", t.Name, err)
	}

	if len(keys) == 0 {
		return nil, 0, nil
	}

	res, err := j.db.ExecContext(ctx, `
		INSERT INTO recovery_dirty (table_name, row_key)
		SELECT $1, k FROM unnest($2::text[]) AS k
		WHERE $3 OR NOT EXISTS (SELECT 1 FROM recovery_sidecars s WHERE s.table_name = $1 AND s.row_key = k)
		ON CONFLICT DO NOTHING`, t.Name, keys, st.full)
	if err != nil {
		return nil, 0, fmt.Errorf("mark missing %s sidecars: %w", t.Name, err)
	}

	n64, _ := res.RowsAffected()

	return keys, int(n64), nil
}

// reconcileStale reads the next n tracked keys of the table after the cursor
// and marks those whose row no longer exists dirty.
func (j *Journal) reconcileStale(ctx context.Context, t *Table, st reconcileState, n int) ([]string, int, error) {
	var keys []string

	rows, err := j.db.QueryContext(ctx, `
		SELECT row_key FROM recovery_sidecars
		WHERE table_name = $1 AND ($2::text IS NULL OR row_key > $2)
		ORDER BY row_key LIMIT $3`, t.Name, st.cursor, n)
	if err != nil {
		return nil, 0, fmt.Errorf("reconcile %s tracking: %w", t.Name, err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, 0, fmt.Errorf("scan tracked key: %w", err)
		}

		keys = append(keys, k)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reconcile %s tracking: %w", t.Name, err)
	}

	if len(keys) == 0 {
		return nil, 0, nil
	}

	present, err := j.existingKeys(ctx, t, keys)
	if err != nil {
		return nil, 0, err
	}

	var gone []string

	for _, k := range keys {
		if !present[k] {
			gone = append(gone, k)
		}
	}

	if len(gone) == 0 {
		return keys, 0, nil
	}

	res, err := j.db.ExecContext(ctx, `
		INSERT INTO recovery_dirty (table_name, row_key)
		SELECT $1, k FROM unnest($2::text[]) AS k
		ON CONFLICT DO NOTHING`, t.Name, gone)
	if err != nil {
		return nil, 0, fmt.Errorf("mark stale %s sidecars: %w", t.Name, err)
	}

	n64, _ := res.RowsAffected()

	return keys, int(n64), nil
}

// existingKeys returns which of keys (canonical row keys) exist as rows.
func (j *Journal) existingKeys(ctx context.Context, t *Table, keys []string) (map[string]bool, error) {
	out := map[string]bool{}

	args, ok := keyArrays(t, keys)
	if !ok || len(args[0]) == 0 {
		return out, nil
	}

	q := fmt.Sprintf(`SELECT %s FROM %s t JOIN %s ON %s`, t.KeyExpr("t"), t.Name, t.unnestSQL(), t.joinSQL()) //nolint:gosec // G201: identifiers come from the compiled-in table registry, values are bound

	rows, err := j.db.QueryContext(ctx, q, args2any(args)...)
	if err != nil {
		return nil, fmt.Errorf("check %s keys: %w", t.Name, err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan existing key: %w", err)
		}

		out[k] = true
	}

	return out, rows.Err() //nolint:wrapcheck // iteration error
}

// backlog returns the number of dirty marks (capped) and the age of the oldest.
func (j *Journal) backlog(ctx context.Context) (int64, time.Duration, error) {
	var n int64

	if err := j.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM recovery_dirty LIMIT %d) s`, backlogCountCap)).Scan(&n); err != nil {
		return 0, 0, fmt.Errorf("count recovery backlog: %w", err)
	}

	// The lowest seq is the oldest mark: a re-mark bumps seq and marked_at together.
	var ageSecs sql.NullFloat64

	err := j.db.QueryRowContext(ctx,
		`SELECT extract(epoch FROM now() - marked_at) FROM recovery_dirty ORDER BY seq LIMIT 1`).Scan(&ageSecs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, fmt.Errorf("read oldest recovery mark: %w", err)
	}

	return n, time.Duration(max(ageSecs.Float64, 0) * float64(time.Second)), nil
}
