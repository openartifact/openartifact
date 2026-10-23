// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// loadChunk bounds how many rows of one table are loaded (and held in
	// memory) at once; npm manifests can be megabytes each.
	loadChunk = 50

	// retryBase and retryMax bound the per-row backoff after a failure.
	retryBase = 5 * time.Second
	retryMax  = 15 * time.Minute

	// reconcileBacklogLimit pauses the reconcile while the dirty queue holds
	// at least this many marks, so a backfill never outruns the worker.
	reconcileBacklogLimit = 20000

	// backlogCountCap bounds the backlog count query.
	backlogCountCap = 1_000_000

	maxErrorText = 500
)

// JournalConfig tunes a Journal.
type JournalConfig struct {
	// Batch is how many dirty rows one drain step claims.
	Batch int
	// Concurrency is how many sidecar blobs are written or deleted at once.
	Concurrency int
	// ReconcileInterval is how often a completed reconcile of a table starts
	// over; zero means only the first (backfill) pass ever runs.
	ReconcileInterval time.Duration
}

// Journal mirrors changed package rows as sidecar blobs. Capture is done by the
// database triggers of migration 068; the Journal drains their queue
// (recovery_dirty), reconciles tables against the tracking table and reports
// its status. At most one process may drain at a time: the Worker serialises
// replicas with an advisory lock.
//
// Sidecars are stored through the blob store directly and deleted with
// BlobStore.Delete, not storage.SafeDeleteBlob: they have no blobs row and are
// referenced by nothing but recovery_sidecars, so there is no reference count
// to consult and nothing deduplicates them (the content includes written_at).
type Journal struct {
	db    *sql.DB
	store storage.BlobStore
	log   *logger.Logger
	m     *metrics.RecoveryMetrics
	cfg   JournalConfig

	schema int

	// now is replaceable by tests.
	now func() time.Time
}

// NewJournal creates a Journal. m may be nil.
func NewJournal(db *sql.DB, store storage.BlobStore, log *logger.Logger, m *metrics.RecoveryMetrics, cfg JournalConfig) *Journal {
	cfg.Batch = max(cfg.Batch, 1)
	cfg.Concurrency = max(cfg.Concurrency, 1)

	return &Journal{db: db, store: store, log: log, m: m, cfg: cfg, schema: SchemaVersion(), now: time.Now}
}

// SetEnabled stores the journal switch the database triggers consult. When the
// journal is switched on after having been off, changes made meanwhile are
// unknown, so every table is queued for a full re-hash (a reconcile in full
// mode) the first time it is enabled again.
func (j *Journal) SetEnabled(ctx context.Context, enabled bool) error {
	var was bool

	err := j.db.QueryRowContext(ctx, `
		WITH old AS (SELECT enabled FROM recovery_state WHERE id FOR UPDATE),
		     upd AS (UPDATE recovery_state SET enabled = $1 WHERE id RETURNING 1)
		SELECT enabled FROM old`, enabled).Scan(&was)
	if err != nil {
		return fmt.Errorf("set recovery journal enabled: %w", err)
	}

	if enabled && !was {
		// Every table's next reconcile pass marks all of its rows, not just the
		// missing ones. (First installs have no tracking rows and are unaffected.)
		if _, err := j.db.ExecContext(ctx, `
			INSERT INTO recovery_reconcile (table_name, full_pass, pass_started_at)
			SELECT unnest($1::text[]), true, now()
			ON CONFLICT (table_name) DO UPDATE
			SET full_pass = true, stage = 'missing', cursor = NULL, pass_started_at = now()`, tableNames()); err != nil {
			return fmt.Errorf("queue full recovery resync: %w", err)
		}
	}

	return nil
}

func tableNames() []string {
	names := make([]string, len(Tables))
	for i := range Tables {
		names[i] = Tables[i].Name
	}

	return names
}

// --- drain -----------------------------------------------------------------

type dirtyMark struct {
	table string
	key   string
	seq   int64
}

// Drain processes dirty marks until the queue holds nothing claimable (marks
// in backoff stay queued). It returns how many marks were processed and
// whether any failed.
func (j *Journal) Drain(ctx context.Context) (int, int, error) {
	var processed, failed int

	for ctx.Err() == nil {
		n, f, err := j.DrainOnce(ctx)
		processed += n
		failed += f

		if err != nil {
			return processed, failed, err
		}

		if n == 0 {
			break
		}
	}

	return processed, failed, ctx.Err()
}

// DrainOnce claims one batch of dirty marks in seq order and processes them.
// It returns how many marks were claimed and how many of those failed.
func (j *Journal) DrainOnce(ctx context.Context) (int, int, error) {
	var failed int

	marks, err := j.claim(ctx)
	if err != nil {
		return 0, 0, err
	}

	if len(marks) == 0 {
		return 0, 0, nil
	}

	byTable := map[string][]dirtyMark{}

	var order []string

	for _, m := range marks {
		if _, ok := byTable[m.table]; !ok {
			order = append(order, m.table)
		}

		byTable[m.table] = append(byTable[m.table], m)
	}

	for _, name := range order {
		ms := byTable[name]

		for len(ms) > 0 {
			n := min(loadChunk, len(ms))

			f, err := j.processChunk(ctx, name, ms[:n])
			failed += f

			if err != nil {
				return 0, failed, err
			}

			ms = ms[n:]
		}
	}

	return len(marks), failed, nil
}

func (j *Journal) claim(ctx context.Context) ([]dirtyMark, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT table_name, row_key, seq FROM recovery_dirty
		WHERE retry_at IS NULL OR retry_at <= now()
		ORDER BY seq LIMIT $1`, j.cfg.Batch)
	if err != nil {
		return nil, fmt.Errorf("claim dirty rows: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []dirtyMark

	for rows.Next() {
		var m dirtyMark
		if err := rows.Scan(&m.table, &m.key, &m.seq); err != nil {
			return nil, fmt.Errorf("scan dirty row: %w", err)
		}

		out = append(out, m)
	}

	return out, rows.Err() //nolint:wrapcheck // iteration error
}

// tracked is a row of recovery_sidecars.
type tracked struct {
	rowHash string
	sidecar string
	pending string
}

// work is the decision for one mark of a chunk.
type work struct {
	mark   dirtyMark
	track  tracked
	hasRow bool
	row    []byte // compact row image
	hash   string
	data   []byte // encoded sidecar
	sha    string // sidecar blob id
	err    error
	done   bool
}

// processChunk handles up to loadChunk marks of one table: load the rows,
// decide per mark, do the storage I/O (never inside a DB transaction), then
// record the outcome and drop the marks that were not re-marked meanwhile.
// It returns how many marks failed (kept queued with backoff).
func (j *Journal) processChunk(ctx context.Context, table string, marks []dirtyMark) (int, error) {
	t, ok := Lookup(table)
	if !ok {
		// The table left the registry: nothing to journal any more.
		return 0, j.dropMarks(ctx, table, marks)
	}

	items, err := j.prepare(ctx, t, marks)
	if err != nil {
		return 0, err
	}

	// Stale pending blobs first: a crashed earlier write or a failed delete of a
	// superseded sidecar left a blob only this row knows about.
	j.cleanPending(ctx, t, items)

	var writes, gones []*work

	for _, w := range items {
		switch {
		case w.err != nil:
		case !w.hasRow:
			gones = append(gones, w)
		case w.track.rowHash == w.hash && w.track.sidecar != "":
			// Unchanged since the sidecar was written.
		default:
			writes = append(writes, w)
		}
	}

	j.encodeAll(t, writes)

	j.runWrites(ctx, t, writes)
	j.runGones(ctx, t, gones)

	return j.finish(ctx, t, items)
}

// prepare loads the rows and tracking state of marks.
func (j *Journal) prepare(ctx context.Context, t *Table, marks []dirtyMark) ([]*work, error) {
	items := make([]*work, len(marks))
	keys := make([]string, len(marks))

	for i, m := range marks {
		items[i] = &work{mark: m}
		keys[i] = m.key
	}

	rows, err := j.loadRows(ctx, t, keys)
	if err != nil {
		return nil, err
	}

	tr, err := j.loadTracking(ctx, t.Name, keys)
	if err != nil {
		return nil, err
	}

	for _, w := range items {
		w.track = tr[w.mark.key]

		if _, ok := t.SplitKey(w.mark.key); !ok {
			// A key of the wrong shape never has a row; any sidecar it tracked goes.
			continue
		}

		if raw, ok := rows[w.mark.key]; ok {
			var buf bytes.Buffer
			if err := json.Compact(&buf, []byte(raw)); err != nil {
				w.err = fmt.Errorf("compact row image: %w", err)

				continue
			}

			w.hasRow = true
			w.row = buf.Bytes()
			sum := sha256.Sum256(w.row)
			w.hash = hex.EncodeToString(sum[:])
		}
	}

	return items, nil
}

// loadRows returns the compact-able jsonb images (excluded columns removed) of
// the rows with the given keys, indexed by key. Keys of the wrong shape are
// simply absent.
func (j *Journal) loadRows(ctx context.Context, t *Table, keys []string) (map[string]string, error) {
	args, ok := keyArrays(t, keys)
	if !ok || len(args) == 0 || len(args[0]) == 0 {
		return map[string]string{}, nil
	}

	q := fmt.Sprintf(`SELECT %s, (to_jsonb(t) - %s)::text FROM %s t JOIN %s ON %s`, //nolint:gosec // G201: identifiers come from the compiled-in table registry, values are bound
		t.KeyExpr("t"), t.ExcludeSQL(), t.Name, t.unnestSQL(), t.joinSQL())

	// to_jsonb renders timestamps in the session time zone; pin UTC so the row
	// image (and its hash) is the same whichever replica or session computes it.
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin %s row load: %w", t.Name, err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SET LOCAL TIME ZONE 'UTC'"); err != nil {
		return nil, fmt.Errorf("set row image time zone: %w", err)
	}

	rows, err := tx.QueryContext(ctx, q, args2any(args)...)
	if err != nil {
		return nil, fmt.Errorf("load %s rows: %w", t.Name, err)
	}

	defer func() { _ = rows.Close() }()

	out := make(map[string]string, len(keys))

	for rows.Next() {
		var k, img string
		if err := rows.Scan(&k, &img); err != nil {
			return nil, fmt.Errorf("scan %s row: %w", t.Name, err)
		}

		out[k] = img
	}

	return out, rows.Err() //nolint:wrapcheck // iteration error
}

// keyArrays splits keys into one text array per key column (the arguments of
// unnestSQL). Keys that do not parse are skipped.
func keyArrays(t *Table, keys []string) ([][]string, bool) {
	args := make([][]string, len(t.Key))

	for _, k := range keys {
		parts, ok := t.SplitKey(k)
		if !ok {
			continue
		}

		for i := range t.Key {
			args[i] = append(args[i], parts[i])
		}
	}

	return args, len(args) > 0
}

func args2any(args [][]string) []any {
	out := make([]any, len(args))
	for i := range args {
		out[i] = args[i]
	}

	return out
}

// unnestSQL is the "unnest($1::text[], $2::text[]) AS u(c0, c1)" source whose
// columns are the typed key columns of the keys to look up.
func (t *Table) unnestSQL() string {
	arrs := make([]string, len(t.Key))
	cols := make([]string, len(t.Key))

	for i := range t.Key {
		arrs[i] = "$" + strconv.Itoa(i+1) + "::text[]"
		cols[i] = "c" + strconv.Itoa(i)
	}

	return "unnest(" + strings.Join(arrs, ", ") + ") AS u(" + strings.Join(cols, ", ") + ")"
}

// joinSQL joins t to the unnest source on every key column.
func (t *Table) joinSQL() string {
	conds := make([]string, len(t.Key))

	for i, k := range t.Key {
		cast := "::text"
		if k.Type == keyUUID {
			cast = "::uuid"
		}

		conds[i] = fmt.Sprintf("t.%s = u.c%d%s", k.Name, i, cast)
	}

	return strings.Join(conds, " AND ")
}

func (j *Journal) loadTracking(ctx context.Context, table string, keys []string) (map[string]tracked, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT row_key, row_hash, sidecar_sha256, COALESCE(pending_sha256, '')
		FROM recovery_sidecars WHERE table_name = $1 AND row_key = ANY($2::text[])`, table, keys)
	if err != nil {
		return nil, fmt.Errorf("load sidecar tracking: %w", err)
	}

	defer func() { _ = rows.Close() }()

	out := make(map[string]tracked, len(keys))

	for rows.Next() {
		var k string

		var tr tracked
		if err := rows.Scan(&k, &tr.rowHash, &tr.sidecar, &tr.pending); err != nil {
			return nil, fmt.Errorf("scan sidecar tracking: %w", err)
		}

		out[k] = tr
	}

	return out, rows.Err() //nolint:wrapcheck // iteration error
}

// cleanPending deletes blobs the tracking row lists as pending (not current)
// and clears the field. A failure is recorded on the item and retried later.
func (j *Journal) cleanPending(ctx context.Context, t *Table, items []*work) {
	var stale []*work

	for _, w := range items {
		if w.track.pending != "" && w.track.pending != w.track.sidecar {
			stale = append(stale, w)
		}
	}

	if len(stale) == 0 {
		return
	}

	j.parallel(ctx, len(stale), func(i int) {
		w := stale[i]

		if err := j.deleteBlob(ctx, w.track.pending); err != nil {
			w.err = fmt.Errorf("delete superseded sidecar: %w", err)
		}
	})

	var keys []string

	for _, w := range stale {
		if w.err == nil {
			keys = append(keys, w.mark.key)
			w.track.pending = ""
		}
	}

	if len(keys) > 0 {
		if _, err := j.db.ExecContext(ctx, `
			UPDATE recovery_sidecars SET pending_sha256 = NULL
			WHERE table_name = $1 AND row_key = ANY($2::text[])`, t.Name, keys); err != nil {
			for _, w := range stale {
				if w.err == nil {
					w.err = fmt.Errorf("clear pending sidecar: %w", err)
				}
			}
		}
	}
}

// deleteBlob deletes a sidecar blob; an already missing blob is fine.
func (j *Journal) deleteBlob(ctx context.Context, sha string) error {
	if sha == "" {
		return nil
	}

	if err := storage.ValidateBlobID(sha); err != nil {
		return fmt.Errorf("tracked sidecar id: %w", err)
	}

	if err := j.store.Delete(ctx, sha); err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
		return fmt.Errorf("delete sidecar blob: %w", err)
	}

	if j.m != nil {
		j.m.SidecarsDeletedTotal.Inc()
	}

	return nil
}

// parallel runs fn(0..n-1) with at most Concurrency in flight.
func (j *Journal) parallel(ctx context.Context, n int, fn func(i int)) {
	sem := make(chan struct{}, j.cfg.Concurrency)

	var wg sync.WaitGroup

	for i := range n {
		if ctx.Err() != nil {
			break
		}

		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			fn(i)
		})
	}

	wg.Wait()
}

func (j *Journal) encodeAll(t *Table, writes []*work) {
	at := j.now()

	for _, w := range writes {
		data, sha, err := EncodeSidecar(t, w.mark.key, j.schema, at, w.row)
		if err != nil {
			// Too large to journal: report on the item, keep retrying later.
			w.err = err

			continue
		}

		w.data, w.sha = data, sha
	}
}

// recordIntent stores the pending marker of each write before its Put, so a
// crash between the Put and the result leaves a blob the tracking row knows
// about. It fails every write and reports false when the marker cannot be stored.
func (j *Journal) recordIntent(ctx context.Context, t *Table, live []*work) bool {
	keys := make([]string, len(live))
	shas := make([]string, len(live))

	for i, w := range live {
		keys[i], shas[i] = w.mark.key, w.sha
	}

	// Intent first: a crash between the Put and the result leaves a blob that the
	// tracking row knows about (pending), so it is deleted rather than leaked.
	if _, err := j.db.ExecContext(ctx, `
		INSERT INTO recovery_sidecars (table_name, row_key, row_hash, sidecar_sha256, pending_sha256)
		SELECT $1, k, '', '', p FROM unnest($2::text[], $3::text[]) AS x(k, p)
		ON CONFLICT (table_name, row_key) DO UPDATE SET pending_sha256 = EXCLUDED.pending_sha256`,
		t.Name, keys, shas); err != nil {
		for _, w := range live {
			w.err = fmt.Errorf("record sidecar intent: %w", err)
		}

		return false
	}

	return true
}

// runWrites records the intent (pending), stores the sidecars and records the
// result. Superseded blobs are queued in pending_sha256 and deleted last.
func (j *Journal) runWrites(ctx context.Context, t *Table, writes []*work) {
	var live []*work

	for _, w := range writes {
		if w.err == nil {
			live = append(live, w)
		}
	}

	if len(live) == 0 {
		return
	}

	if !j.recordIntent(ctx, t, live) {
		return
	}

	j.parallel(ctx, len(live), func(i int) {
		w := live[i]

		if _, err := j.store.Put(ctx, bytes.NewReader(w.data), SidecarMetadata(t.Name, w.sha)); err != nil {
			w.err = fmt.Errorf("store sidecar: %w", err)

			return
		}

		w.done = true

		if j.m != nil {
			j.m.SidecarsWrittenTotal.Inc()
		}
	})

	var (
		okKeys, okHash, okSha, okOld []string
	)

	for _, w := range live {
		if !w.done {
			continue
		}

		okKeys = append(okKeys, w.mark.key)
		okHash = append(okHash, w.hash)
		okSha = append(okSha, w.sha)
		okOld = append(okOld, supersededOf(w))
	}

	if len(okKeys) == 0 {
		return
	}

	if _, err := j.db.ExecContext(ctx, `
		UPDATE recovery_sidecars r
		SET row_hash = x.h, sidecar_sha256 = x.s, pending_sha256 = NULLIF(x.o, ''), written_at = now()
		FROM unnest($2::text[], $3::text[], $4::text[], $5::text[]) AS x(k, h, s, o)
		WHERE r.table_name = $1 AND r.row_key = x.k`, t.Name, okKeys, okHash, okSha, okOld); err != nil {
		for _, w := range live {
			if w.done && w.err == nil {
				w.err = fmt.Errorf("record sidecar: %w", err)
			}
		}

		return
	}

	j.deleteSuperseded(ctx, t, live)
}

// deleteSuperseded removes the sidecars the writes replaced and clears their
// pending markers.
func (j *Journal) deleteSuperseded(ctx context.Context, t *Table, live []*work) {
	// A failure leaves them in pending_sha256 and the mark queued, so the next
	// pass retries.
	var olds []*work

	for _, w := range live {
		if w.done && w.err == nil && supersededOf(w) != "" {
			olds = append(olds, w)
		}
	}

	j.parallel(ctx, len(olds), func(i int) {
		w := olds[i]
		if err := j.deleteBlob(ctx, supersededOf(w)); err != nil {
			w.err = err
		}
	})

	var cleared []string

	for _, w := range olds {
		if w.err == nil {
			cleared = append(cleared, w.mark.key)
		}
	}

	if len(cleared) > 0 {
		if _, err := j.db.ExecContext(ctx, `
			UPDATE recovery_sidecars SET pending_sha256 = NULL
			WHERE table_name = $1 AND row_key = ANY($2::text[])`, t.Name, cleared); err != nil {
			j.log.Warnw("clear superseded sidecar marker", "table", t.Name, "error", err)
		}
	}
}

// supersededOf is the sidecar blob the write replaces ("" when none or equal).
func supersededOf(w *work) string {
	if w.track.sidecar != "" && w.track.sidecar != w.sha {
		return w.track.sidecar
	}

	return ""
}

// runGones deletes the sidecars of rows that no longer exist and their
// tracking rows.
func (j *Journal) runGones(ctx context.Context, t *Table, gones []*work) {
	if len(gones) == 0 {
		return
	}

	j.parallel(ctx, len(gones), func(i int) {
		w := gones[i]

		for _, sha := range []string{w.track.sidecar, w.track.pending} {
			if err := j.deleteBlob(ctx, sha); err != nil {
				w.err = err

				return
			}
		}

		w.done = true
	})

	var keys []string

	for _, w := range gones {
		if w.done && w.err == nil {
			keys = append(keys, w.mark.key)
		}
	}

	if len(keys) == 0 {
		return
	}

	if _, err := j.db.ExecContext(ctx,
		`DELETE FROM recovery_sidecars WHERE table_name = $1 AND row_key = ANY($2::text[])`, t.Name, keys); err != nil {
		for _, w := range gones {
			if w.done && w.err == nil {
				w.err = fmt.Errorf("drop sidecar tracking: %w", err)
			}
		}
	}
}

// finish deletes the marks that were handled (unless re-marked meanwhile) and
// backs off the failed ones.
func (j *Journal) finish(ctx context.Context, t *Table, items []*work) (int, error) {
	var (
		doneT, doneK []string
		doneS        []int64
		failed       []*work
	)

	for _, w := range items {
		if w.err != nil {
			failed = append(failed, w)

			continue
		}

		doneT, doneK, doneS = append(doneT, t.Name), append(doneK, w.mark.key), append(doneS, w.mark.seq)
	}

	if len(doneK) > 0 {
		if err := j.deleteMarks(ctx, doneT, doneK, doneS); err != nil {
			return len(failed), err
		}
	}

	for _, w := range failed {
		if ctx.Err() != nil {
			break
		}

		j.recordFailure(ctx, t.Name, w)
	}

	return len(failed), ctx.Err()
}

func (j *Journal) deleteMarks(ctx context.Context, tables, keys []string, seqs []int64) error {
	// seq is passed as text: unnest of a []int64 needs a driver array encoder
	// per element type; the text round-trip keeps every argument a []string.
	ss := make([]string, len(seqs))
	for i, s := range seqs {
		ss[i] = strconv.FormatInt(s, 10)
	}

	if _, err := j.db.ExecContext(ctx, `
		DELETE FROM recovery_dirty d USING unnest($1::text[], $2::text[], $3::text[]) AS x(t, k, s)
		WHERE d.table_name = x.t AND d.row_key = x.k AND d.seq = x.s::bigint`, tables, keys, ss); err != nil {
		return fmt.Errorf("delete dirty marks: %w", err)
	}

	return nil
}

func (j *Journal) dropMarks(ctx context.Context, table string, marks []dirtyMark) error {
	tables := make([]string, len(marks))
	keys := make([]string, len(marks))
	seqs := make([]int64, len(marks))

	for i, m := range marks {
		tables[i], keys[i], seqs[i] = table, m.key, m.seq
	}

	return j.deleteMarks(ctx, tables, keys, seqs)
}

func (j *Journal) recordFailure(ctx context.Context, table string, w *work) {
	msg := w.err.Error()
	if len(msg) > maxErrorText {
		msg = msg[:maxErrorText]
	}

	j.log.Warnw("recovery journal: row failed, will retry", "table", table, "key", w.mark.key, "error", w.err)

	if j.m != nil {
		j.m.FailuresTotal.Inc()
	}

	if _, err := j.db.ExecContext(ctx, `
		UPDATE recovery_dirty
		SET attempts = attempts + 1, last_error = $4,
		    retry_at = now() + LEAST($5::double precision, $6::double precision * power(2, LEAST(attempts, 20))) * interval '1 second'
		WHERE table_name = $1 AND row_key = $2 AND seq = $3`,
		table, w.mark.key, w.mark.seq, msg, retryMax.Seconds(), retryBase.Seconds()); err != nil {
		j.log.Warnw("recovery journal: record failure", "error", err)
	}

	_, _ = j.db.ExecContext(ctx,
		`UPDATE recovery_state SET last_error = $1, last_error_at = now() WHERE id`, table+": "+msg)
}

// MarkDrained records a completed drain in the status row.
func (j *Journal) markDrained(ctx context.Context) {
	_, _ = j.db.ExecContext(ctx, `UPDATE recovery_state SET last_drain_at = now() WHERE id`)
}
