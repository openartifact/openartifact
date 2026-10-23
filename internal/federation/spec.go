// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// Row is a table row as exported: column name to its JSON value.
type Row map[string]json.RawMessage

// Str returns the value of a text column ("" for null or absent).
func (r Row) Str(col string) string {
	raw, ok := r[col]
	if !ok || string(raw) == "null" {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return strings.Trim(string(raw), `"`)
	}

	return s
}

// Set stores a text value.
func (r Row) Set(col, val string) {
	raw, _ := json.Marshal(val) //nolint:errchkjson // strings always marshal

	r[col] = raw
}

// IsNull reports whether the column is absent or null.
func (r Row) IsNull(col string) bool {
	raw, ok := r[col]

	return !ok || string(raw) == "null"
}

// tableSpec describes a format whose units are the live rows of one table, each
// a single row plus the blobs it references. The generic codec built from it
// copies the row as JSON: every column of the table is replicated except the
// ones listed in alwaysLocal and exclude (spec_test.go pins the column list of
// each table, so a new column forces a decision).
type tableSpec struct {
	format string
	table  string
	// kind is KindArtifact or KindRef.
	kind string

	// where builds the predicate (table alias t) that locates the live row of
	// a unit key; its placeholders start at $2 ($1 is the repository id).
	where func(key string) (clause string, args []any, ok bool)

	// backfillKey is the SQL expression over alias t producing the unit key and
	// backfillRef the one producing content_ref (see migration 071).
	backfillKey string
	backfillRef string

	// blobCols are the uuid columns referencing blobs.id, in content_ref
	// priority order. They travel as sha256 digests.
	blobCols []string

	// exclude are columns that stay local to each instance (statistics).
	exclude []string

	// deps lists the unit keys that must exist before the row (nil for none).
	deps func(key string, row Row) []string

	// regen lists the metadata refreshes a change of the row triggers.
	regen func(repoID string, row Row) []Regen

	// info describes the row for webhooks; nil when no event is sent.
	info func(row Row) *ArtifactInfo
}

// alwaysLocal are columns no codec replicates: row identity, repository
// scope, trash state, local timestamps.
var alwaysLocal = []string{"id", "repository_id", "deleted_at", "deleted_by", "updated_at"} //nolint:gochecknoglobals // constant set

type tableCodec struct {
	spec tableSpec
	cols sync.Map // table name -> []string, the replicable columns
}

func (c *tableCodec) Format() string { return c.spec.format }

// tableColumns returns the non-generated columns of a table, in order.
func tableColumns(ctx context.Context, q Querier, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = current_schema() AND table_name = $1 AND is_generated = 'NEVER'
		  ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("list columns of %s: %w", table, err)
	}
	defer rows.Close()

	var cols []string

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan column of %s: %w", table, err)
		}

		cols = append(cols, name)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list columns of %s: %w", table, err)
	}

	if len(cols) == 0 {
		return nil, fmt.Errorf("%w: %s", errTableNoColumns, table)
	}

	return cols, nil
}

// replicated returns the columns the codec copies.
func (c *tableCodec) replicated(ctx context.Context, q Querier) ([]string, error) {
	if v, ok := c.cols.Load(c.spec.table); ok {
		cols, _ := v.([]string)

		return cols, nil
	}

	all, err := tableColumns(ctx, q, c.spec.table)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(all))

	for _, col := range all {
		if slices.Contains(alwaysLocal, col) || slices.Contains(c.spec.exclude, col) {
			continue
		}

		out = append(out, col)
	}

	c.cols.Store(c.spec.table, out)

	return out, nil
}

// lockRow loads the live row of key (with FOR UPDATE inside a transaction).
func (c *tableCodec) loadRow(ctx context.Context, q Querier, repoID, key string, lock bool) (Row, string, error) {
	clause, args, ok := c.spec.where(key)
	if !ok {
		return nil, "", fmt.Errorf("%w: malformed unit key %q", ErrInvalid, key)
	}

	query := `SELECT to_jsonb(t), t.id::text FROM ` + c.spec.table + ` t WHERE t.repository_id = $1::uuid AND t.deleted_at IS NULL AND ` + clause
	if lock {
		query += ` FOR UPDATE`
	}

	var (
		raw []byte
		id  string
	)

	err := q.QueryRowContext(ctx, query, append([]any{repoID}, args...)...).Scan(&raw, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}

	if err != nil {
		return nil, "", fmt.Errorf("load %s unit: %w", c.spec.table, err)
	}

	var row Row
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, "", fmt.Errorf("decode %s row: %w", c.spec.table, err)
	}

	return row, id, nil
}

type blobInfo struct {
	id        string
	sha256    string
	size      int64
	mediaType string
}

// blobByID resolves a blobs.id.
func blobByID(ctx context.Context, q Querier, id string) (blobInfo, error) {
	b := blobInfo{id: id}

	err := q.QueryRowContext(ctx,
		`SELECT sha256, size_bytes, media_type FROM blobs WHERE id = $1::uuid`, id,
	).Scan(&b.sha256, &b.size, &b.mediaType)
	if err != nil {
		return blobInfo{}, fmt.Errorf("resolve blob %s: %w", id, err)
	}

	return b, nil
}

func (c *tableCodec) Export(ctx context.Context, q Querier, repoID, key string) (*Content, error) {
	row, _, err := c.loadRow(ctx, q, repoID, key, false)
	if err != nil || row == nil {
		return nil, err
	}

	cols, err := c.replicated(ctx, q)
	if err != nil {
		return nil, err
	}

	content := &Content{Kind: c.spec.kind}

	for _, col := range c.spec.blobCols {
		if row.IsNull(col) {
			continue
		}

		b, err := blobByID(ctx, q, row.Str(col))
		if err != nil {
			return nil, err
		}

		content.Blobs = append(content.Blobs, BlobRef{SHA256: b.sha256, Size: b.size, MediaType: b.mediaType})

		row.Set(col, b.sha256)

		if content.ContentRef == "" {
			content.ContentRef = b.sha256
		}
	}

	for col := range row {
		if !slices.Contains(cols, col) {
			delete(row, col)
		}
	}

	if c.spec.deps != nil {
		content.Deps = c.spec.deps(key, row)
	}

	content.Data, err = json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("encode %s unit: %w", c.spec.table, err)
	}

	return content, nil
}

func (c *tableCodec) Usage(_ context.Context, _ Querier, _, _ string, content *Content) (int64, int, error) {
	if c.spec.kind != KindArtifact {
		return 0, 0, nil
	}

	var size int64
	for _, b := range content.Blobs {
		size += b.Size
	}

	return size, 1, nil
}

// existingBlobs resolves the blobs of a loaded row.
func (c *tableCodec) existingBlobs(ctx context.Context, q Querier, row Row) ([]blobInfo, error) {
	var out []blobInfo

	for _, col := range c.spec.blobCols {
		if row.IsNull(col) {
			continue
		}

		b, err := blobByID(ctx, q, row.Str(col))
		if err != nil {
			return nil, err
		}

		out = append(out, b)
	}

	return out, nil
}

func sumBlobs(bs []blobInfo) int64 {
	var n int64
	for _, b := range bs {
		n += b.size
	}

	return n
}

// describe fills the change's webhook and regeneration fields from a row whose
// blob columns hold digests.
func (c *tableCodec) describe(repoID string, row Row, ch *Change) {
	if c.spec.info != nil {
		ch.Artifact = c.spec.info(row)
	}

	if c.spec.regen != nil {
		ch.Regen = c.spec.regen(repoID, row)
	}
}

func (c *tableCodec) Import(ctx context.Context, tx *sql.Tx, repoID, key string, content *Content, blobIDs map[string]string) (*Change, error) {
	var data Row
	if err := json.Unmarshal(content.Data, &data); err != nil {
		return nil, fmt.Errorf("%w: decode unit data: %v", ErrInvalid, err) //nolint:errorlint // client-safe text only
	}

	replicated, err := c.replicated(ctx, tx)
	if err != nil {
		return nil, err
	}

	incoming := Row{}
	for col, v := range data {
		if slices.Contains(replicated, col) {
			incoming[col] = v
		}
	}

	row := Row{}
	maps.Copy(row, incoming)

	newShas := map[string]bool{}

	for _, col := range c.spec.blobCols {
		if row.IsNull(col) {
			continue
		}

		sha := row.Str(col)

		id, ok := blobIDs[sha]
		if !ok {
			return nil, fmt.Errorf("%w: blob %s of unit %s was not provided", ErrInvalid, sha, key)
		}

		newShas[sha] = true

		incoming.Set(col, id)
	}

	change := &Change{}
	c.describe(repoID, row, change)

	existing, existingID, err := c.loadRow(ctx, tx, repoID, key, true)
	if err != nil {
		return nil, err
	}

	cols := make([]string, 0, len(incoming))
	for col := range incoming {
		cols = append(cols, col)
	}

	slices.Sort(cols)

	payload, err := json.Marshal(incoming)
	if err != nil {
		return nil, fmt.Errorf("encode %s row: %w", c.spec.table, err)
	}

	list := strings.Join(cols, ", ")

	if existing == nil {
		_, err = tx.ExecContext(ctx, //nolint:gosec // table and columns come from the static spec and information_schema
			`INSERT INTO `+c.spec.table+` (repository_id, `+list+`)
			 SELECT $1::uuid, `+list+` FROM jsonb_populate_record(NULL::`+c.spec.table+`, $2::jsonb)`,
			repoID, string(payload))
		if err != nil {
			return nil, fmt.Errorf("insert %s unit: %w", c.spec.table, err)
		}

		return change, nil
	}

	old, err := c.existingBlobs(ctx, tx, existing)
	if err != nil {
		return nil, err
	}

	change.Replaced = true
	change.ReplacedSize = sumBlobs(old)

	if c.spec.kind == KindArtifact {
		change.ReplacedCount = 1
	}

	for _, b := range old {
		if !newShas[b.sha256] {
			change.ReleasedBlobs = append(change.ReleasedBlobs, b.sha256)
		}
	}

	_, err = tx.ExecContext(ctx,
		//nolint:gosec // table and columns come from the static spec and information_schema
		`UPDATE `+c.spec.table+` SET (`+list+`) = (SELECT `+list+` FROM jsonb_populate_record(NULL::`+c.spec.table+`, $2::jsonb))
		  WHERE id = $1::uuid`,
		existingID, string(payload))
	if err != nil {
		return nil, fmt.Errorf("update %s unit: %w", c.spec.table, err)
	}

	return change, nil
}

func (c *tableCodec) Delete(ctx context.Context, tx *sql.Tx, repoID, key string, soft bool, deletedBy string) (*Change, error) {
	row, id, err := c.loadRow(ctx, tx, repoID, key, true)
	if err != nil {
		return nil, err
	}

	if row == nil {
		return &Change{Missing: true}, nil
	}

	blobs, err := c.existingBlobs(ctx, tx, row)
	if err != nil {
		return nil, err
	}

	change := &Change{RemovedSize: sumBlobs(blobs)}
	if c.spec.kind == KindArtifact {
		change.RemovedCount = 1
	}

	for _, col := range c.spec.blobCols {
		if b, ok := blobFor(blobs, row, col); ok {
			row.Set(col, b.sha256)
		}
	}

	c.describe(repoID, row, change)

	if soft {
		_, err = tx.ExecContext(ctx,
			//nolint:gosec // table from the static spec
			`UPDATE `+c.spec.table+` SET deleted_at = now(), deleted_by = $2 WHERE id = $1::uuid`, id, deletedBy)
	} else {
		_, err = tx.ExecContext(ctx,
			//nolint:gosec // table from the static spec
			`DELETE FROM `+c.spec.table+` WHERE id = $1::uuid`, id)

		for _, b := range blobs {
			change.ReleasedBlobs = append(change.ReleasedBlobs, b.sha256)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("delete %s unit: %w", c.spec.table, err)
	}

	return change, nil
}

func blobFor(blobs []blobInfo, row Row, col string) (blobInfo, bool) {
	id := row.Str(col)
	for _, b := range blobs {
		if b.id == id {
			return b, true
		}
	}

	return blobInfo{}, false
}

func (c *tableCodec) Backfill(ctx context.Context, tx *sql.Tx, repoID string) error {
	event := EventArtifactAdded
	if c.spec.kind == KindRef {
		event = EventRefMoved
	}

	_, err := tx.ExecContext(ctx,
		//nolint:gosec // expressions come from the static spec
		`SELECT federation_record($1::uuid, `+c.spec.backfillKey+`, $2, $3, `+c.spec.backfillRef+`, false, t.updated_at)
		   FROM `+c.spec.table+` t WHERE t.repository_id = $1::uuid AND t.deleted_at IS NULL`,
		repoID, c.spec.kind, event)
	if err != nil {
		return fmt.Errorf("backfill %s: %w", c.spec.table, err)
	}

	return nil
}

func newTableCodec(spec tableSpec) *tableCodec { return &tableCodec{spec: spec} }
