// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	applyChunk = 100

	pgForeignKeyViolation = "23503"
)

// applyState carries what later tables need to know about earlier ones.
type applyState struct {
	ids          idSet
	missingBlob  map[string]string // blobs.id -> sha256 whose bytes are gone
	touchedRepos map[string]struct{}
	npmPackages  map[string]struct{}
	recovered    []string
	columns      map[string][]string
	stmts        map[string]string
}

// cand is a sidecar row prepared for insertion.
type cand struct {
	e   *entry
	row []byte
	m   map[string]json.RawMessage
}

func (r *Rebuilder) apply(ctx context.Context, rep *Report, ids idSet, winners map[string]*entry) error {
	st := &applyState{
		ids: ids, missingBlob: map[string]string{}, touchedRepos: map[string]struct{}{}, npmPackages: map[string]struct{}{},
		columns: map[string][]string{}, stmts: map[string]string{},
	}

	byTable := map[string][]*entry{}
	for _, e := range winners {
		byTable[e.table.Name] = append(byTable[e.table.Name], e)
	}

	presentBlobs := 0

	for i := range Tables {
		t := &Tables[i]

		es := byTable[t.Name]
		if len(es) == 0 {
			continue
		}

		sort.Slice(es, func(a, b int) bool { return es[a].key < es[b].key })

		rep.table(t.Name).Found = len(es)

		if err := r.applyTable(ctx, rep, st, t, es); err != nil {
			return err
		}

		if t.Name == "blobs" {
			presentBlobs = len(es) - len(st.missingBlob)
		}
	}

	rep.UnreferencedBlobs = max(st.ids.len()-rep.SidecarCandidate-presentBlobs, 0)

	sort.Strings(st.recovered)
	rep.ReposRecovered = append(rep.ReposRecovered, st.recovered...)

	return r.postSteps(ctx, rep, st)
}

func (r *Rebuilder) postSteps(ctx context.Context, rep *Report, st *applyState) error {
	if r.opts.DryRun {
		return nil
	}

	if r.hooks.ReconcileUsage != nil {
		if err := r.hooks.ReconcileUsage(ctx); err != nil {
			rep.addFailure(Item{Reason: "reconcile storage usage: " + err.Error()})
		} else {
			rep.PostSteps = append(rep.PostSteps, "storage usage")
		}
	}

	if r.hooks.Regenerate != nil && (len(st.touchedRepos) > 0 || len(st.npmPackages) > 0) {
		if err := r.hooks.Regenerate(ctx, keys(st.touchedRepos), keys(st.npmPackages)); err != nil {
			rep.addFailure(Item{Reason: "regenerate metadata: " + err.Error()})
		} else {
			rep.PostSteps = append(rep.PostSteps, "metadata regeneration")
		}
	}

	return nil
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func (r *Rebuilder) applyTable(ctx context.Context, rep *Report, st *applyState, t *Table, es []*entry) error {
	tr := rep.table(t.Name)

	for len(es) > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("rebuild interrupted: %w", err)
		}

		n := min(applyChunk, len(es))
		chunk := es[:n]
		es = es[n:]

		cs := r.prepareChunk(ctx, rep, t, chunk)

		present, err := r.existingKeys(ctx, t, entryKeys(cs))
		if err != nil {
			return err
		}

		var fresh, have []*cand

		for _, c := range cs {
			if present[c.e.key] {
				have = append(have, c)
			} else {
				fresh = append(fresh, c)
			}
		}

		tr.AlreadyPresent += len(have)

		fresh = r.checkBlobs(rep, st, t, fresh)

		inserted := fresh
		if !r.opts.DryRun {
			inserted, err = r.insertChunk(ctx, rep, st, t, fresh)
			if err != nil {
				return err
			}
		}

		tr.Inserted += len(inserted)

		r.note(st, t, inserted)

		if !r.opts.DryRun {
			if err := r.adopt(ctx, t, inserted, have); err != nil {
				return err
			}
		}
	}

	return nil
}

func entryKeys(cs []*cand) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.e.key
	}

	return out
}

// prepareChunk loads (when not retained) and parses the row images.
func (r *Rebuilder) prepareChunk(ctx context.Context, rep *Report, t *Table, chunk []*entry) []*cand {
	out := make([]*cand, 0, len(chunk))

	for _, e := range chunk {
		row := e.row
		if row == nil {
			sc, _, err := r.readSidecar(ctx, e.id)
			if err != nil {
				rep.addFailure(Item{Table: t.Name, Key: e.key, Blob: e.id, Reason: "re-read sidecar: " + err.Error()})

				continue
			}

			row = sc.Row
		}

		var m map[string]json.RawMessage
		if err := json.Unmarshal(row, &m); err != nil {
			rep.addFailure(Item{Table: t.Name, Key: e.key, Blob: e.id, Reason: "parse row image: " + err.Error()})

			continue
		}

		out = append(out, &cand{e: e, row: row, m: m})
	}

	return out
}

func strField(m map[string]json.RawMessage, col string) string {
	var s string
	if err := json.Unmarshal(m[col], &s); err != nil {
		return ""
	}

	return s
}

// checkBlobs drops rows whose blob bytes are gone from storage and records
// the blobs of the blobs table that are missing.
func (r *Rebuilder) checkBlobs(rep *Report, st *applyState, t *Table, cs []*cand) []*cand {
	out := cs[:0:0]

	for _, c := range cs {
		if t.Name == "blobs" {
			sha := strField(c.m, "sha256")
			if !st.ids.has(sha) {
				st.missingBlob[c.e.key] = sha
				rep.addMissingBlob(Item{Table: t.Name, Key: c.e.key, Blob: sha, Reason: "bytes not found in the blob store"})
				rep.addUnrecoverable(Item{Table: t.Name, Key: c.e.key, Blob: sha, Reason: "blob bytes missing from storage"})

				continue
			}
		}

		missing := ""

		for _, col := range t.BlobRefs {
			if sha, ok := st.missingBlob[strField(c.m, col)]; ok {
				missing = sha

				break
			}
		}

		if missing != "" {
			rep.addUnrecoverable(Item{Table: t.Name, Key: c.e.key, Blob: missing, Reason: "references a blob whose bytes are missing from storage"})

			continue
		}

		out = append(out, c)
	}

	return out
}

// note records which repositories and npm packages received rows.
func (r *Rebuilder) note(st *applyState, t *Table, inserted []*cand) {
	for _, c := range inserted {
		switch t.Name {
		case "repositories":
			st.touchedRepos[c.e.key] = struct{}{}
			st.recovered = append(st.recovered, strField(c.m, "name"))
		case "npm_packages":
			st.npmPackages[c.e.key] = struct{}{}
		case "npm_versions", "npm_dist_tags":
			if id := strField(c.m, "package_id"); id != "" {
				st.npmPackages[id] = struct{}{}
			}
		}

		if id := strField(c.m, "repository_id"); id != "" && t.Name != "repositories" {
			st.touchedRepos[id] = struct{}{}
		}
	}
}

// liveColumns returns the insertable (non-generated) columns of a table in
// ordinal order.
func (r *Rebuilder) liveColumns(ctx context.Context, st *applyState, t *Table) ([]string, error) {
	if cols, ok := st.columns[t.Name]; ok {
		return cols, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND is_generated = 'NEVER'
		ORDER BY ordinal_position`, t.Name)
	if err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", t.Name, err)
	}

	defer func() { _ = rows.Close() }()

	var cols []string

	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}

		if !t.isExcluded(c) {
			cols = append(cols, c)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", t.Name, err)
	}

	st.columns[t.Name] = cols

	return cols, nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// insertSQL builds (and caches) the insert for the columns a row carries. The
// table is in the registry and the columns are live columns of it, so no
// identifier comes from storage content. Columns the sidecar lacks keep their
// default; columns it carries that no longer exist are ignored.
func (r *Rebuilder) insertSQL(ctx context.Context, st *applyState, t *Table, c *cand) (string, error) {
	live, err := r.liveColumns(ctx, st, t)
	if err != nil {
		return "", err
	}

	var cols []string

	for _, col := range live {
		if _, ok := c.m[col]; ok {
			cols = append(cols, quoteIdent(col))
		}
	}

	ck := t.Name + "|" + strings.Join(cols, ",")
	if q, ok := st.stmts[ck]; ok {
		return q, nil
	}

	list := strings.Join(cols, ", ")
	q := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM jsonb_populate_record(NULL::%s, $1::jsonb) ON CONFLICT DO NOTHING`,
		t.Name, list, list, t.Name)
	st.stmts[ck] = q

	return q, nil
}

// insertChunk inserts the rows in one transaction; if the database rejects any
// of them the chunk is redone row by row so each failure is attributed.
func (r *Rebuilder) insertChunk(ctx context.Context, rep *Report, st *applyState, t *Table, cs []*cand) ([]*cand, error) {
	if len(cs) == 0 {
		return nil, nil
	}

	got, err := r.insertTx(ctx, st, t, cs)
	if err == nil {
		return r.settle(rep, t, cs, got), nil
	}

	if ctx.Err() != nil {
		return nil, fmt.Errorf("rebuild interrupted: %w", ctx.Err())
	}

	var inserted []*cand

	for _, c := range cs {
		one := []*cand{c}

		got, err := r.insertTx(ctx, st, t, one)
		if err != nil {
			r.rowError(rep, t, c, err)

			continue
		}

		inserted = append(inserted, r.settle(rep, t, one, got)...)
	}

	return inserted, nil
}

func (r *Rebuilder) insertTx(ctx context.Context, st *applyState, t *Table, cs []*cand) ([]bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin rebuild insert: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	got := make([]bool, len(cs))

	for i, c := range cs {
		q, err := r.insertSQL(ctx, st, t, c)
		if err != nil {
			return nil, err
		}

		res, err := tx.ExecContext(ctx, q, string(c.row))
		if err != nil {
			return nil, fmt.Errorf("insert %s %s: %w", t.Name, c.e.key, err)
		}

		n, _ := res.RowsAffected()
		got[i] = n == 1
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit rebuild insert: %w", err)
	}

	return got, nil
}

// settle turns the per-row insert counts into the inserted list; a row that
// was not inserted although it did not exist collides with another row.
func (r *Rebuilder) settle(rep *Report, t *Table, cs []*cand, got []bool) []*cand {
	var inserted []*cand

	for i, c := range cs {
		if got[i] {
			inserted = append(inserted, c)

			continue
		}

		rep.addUnrecoverable(Item{Table: t.Name, Key: c.e.key, Reason: "conflicts with a different existing row (unique constraint)"})
	}

	return inserted
}

func (r *Rebuilder) rowError(rep *Report, t *Table, c *cand, err error) {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == pgForeignKeyViolation {
		rep.addUnrecoverable(Item{Table: t.Name, Key: c.e.key, Reason: "parent row not recoverable (" + pgErr.ConstraintName + ")"})

		return
	}

	rep.addFailure(Item{Table: t.Name, Key: c.e.key, Blob: c.e.id, Reason: err.Error()})
}

// adopt makes the journal track the restored sidecars: rows inserted from a
// sidecar are in step with it (hash known); rows that already existed are
// tracked with an unknown hash and queued, so the worker compares them with
// the database and rewrites the sidecar when it differs. Without this the
// worker would later write a second sidecar for a row, and deleting the row
// would leave the restored one behind.
func (r *Rebuilder) adopt(ctx context.Context, t *Table, inserted, have []*cand) error {
	if err := r.track(ctx, t, inserted, false); err != nil {
		return err
	}

	return r.track(ctx, t, have, true)
}

func (r *Rebuilder) track(ctx context.Context, t *Table, cs []*cand, unknownHash bool) error {
	if len(cs) == 0 {
		return nil
	}

	keys := make([]string, len(cs))
	hashes := make([]string, len(cs))
	shas := make([]string, len(cs))
	ats := make([]string, len(cs))

	for i, c := range cs {
		keys[i], shas[i], ats[i] = c.e.key, c.e.id, c.e.at.Format(time.RFC3339Nano)

		if !unknownHash {
			sum := sha256.Sum256(c.row)
			hashes[i] = hex.EncodeToString(sum[:])
		}
	}

	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO recovery_sidecars (table_name, row_key, row_hash, sidecar_sha256, written_at)
		SELECT $1, x.k, x.h, x.s, x.w::timestamptz FROM unnest($2::text[], $3::text[], $4::text[], $5::text[]) AS x(k, h, s, w)
		ON CONFLICT (table_name, row_key) DO NOTHING`, t.Name, keys, hashes, shas, ats); err != nil {
		return fmt.Errorf("adopt sidecars of %s: %w", t.Name, err)
	}

	if unknownHash {
		if _, err := r.db.ExecContext(ctx, `
			INSERT INTO recovery_dirty (table_name, row_key) SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`,
			t.Name, keys); err != nil {
			return fmt.Errorf("queue adopted rows of %s: %w", t.Name, err)
		}
	}

	return nil
}

// existingKeys returns which of keys already exist as rows of t.
func (r *Rebuilder) existingKeys(ctx context.Context, t *Table, keys []string) (map[string]bool, error) {
	j := &Journal{db: r.db}

	return j.existingKeys(ctx, t, keys)
}
