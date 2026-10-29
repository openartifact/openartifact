// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Queries behind the Go module zip repair (internal/format/gomod/repair.go).
// OpenArtifact <= 0.4.1 stored every Go module zip under ONE blobs row whose
// sha256 was ''; these methods find the rows that still point at such a row,
// relink them to a row keyed by the zip's real digest, and drop the shared row.

// gomodRepairLockKey names the session advisory lock that keeps a single
// replica running the repair.
const gomodRepairLockKey = "openartifact:gomod-zip-repair"

// invalidSHA256Cond is true for a blobs row (alias zb) whose sha256 is not a
// lowercase 64-character hex digest.
const invalidSHA256Cond = `zb.sha256 !~ '^[0-9a-f]{64}$'`

// GomodAffectedRow is a gomod_modules row (trashed ones included) whose zip
// blob row has an invalid sha256.
type GomodAffectedRow struct {
	ID           string
	RepositoryID string
	RepoName     string
	ModulePath   string
	Version      string
	GoModContent string
	Trashed      bool
	// BadBlobID and BadBlobKey identify the invalid blobs row the zip points at.
	BadBlobID  string
	BadBlobKey string
}

// TryGomodRepairLock takes the repair's session advisory lock on a pinned
// connection. ok is false when another replica holds it. release must be
// called when ok is true; it unlocks and returns the connection to the pool.
func (p *PostgresDB) TryGomodRepairLock(ctx context.Context) (func(), bool, error) {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("pin connection for gomod repair lock: %w", err)
	}

	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, gomodRepairLockKey).Scan(&locked); err != nil {
		_ = conn.Close()

		return nil, false, fmt.Errorf("try gomod repair lock: %w", err)
	}

	if !locked {
		_ = conn.Close()

		return nil, false, nil
	}

	return func() {
		// The caller's context may be cancelled (shutdown); unlock regardless.
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext($1))`, gomodRepairLockKey)
		_ = conn.Close()
	}, true, nil
}

// ListGomodAffectedRows returns every gomod_modules row whose zip blob row has
// a sha256 that is not 64 lowercase hex characters.
func (p *PostgresDB) ListGomodAffectedRows(ctx context.Context) ([]GomodAffectedRow, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT gm.id, gm.repository_id, r.name, gm.module_path, gm.version, gm.go_mod_content,
		       gm.deleted_at IS NOT NULL, zb.id, zb.blob_key
		FROM gomod_modules gm
		JOIN blobs zb ON zb.id = gm.zip_blob_id
		JOIN repositories r ON r.id = gm.repository_id
		WHERE `+invalidSHA256Cond+`
		ORDER BY r.name, gm.module_path, gm.version`)
	if err != nil {
		return nil, fmt.Errorf("list affected go module rows: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []GomodAffectedRow

	for rows.Next() {
		var a GomodAffectedRow
		if err := rows.Scan(&a.ID, &a.RepositoryID, &a.RepoName, &a.ModulePath, &a.Version,
			&a.GoModContent, &a.Trashed, &a.BadBlobID, &a.BadBlobKey); err != nil {
			return nil, fmt.Errorf("scan affected go module row: %w", err)
		}

		out = append(out, a)
	}

	return out, rows.Err()
}

// EmptySHA256BlobID returns the id of the shared blobs row with an empty
// sha256 left behind by OpenArtifact <= 0.4.1, or "" when it does not exist (blobs.sha256 is unique and indexed, so
// this is a point lookup).
func (p *PostgresDB) EmptySHA256BlobID(ctx context.Context) (string, error) {
	var id string

	err := p.db.QueryRowContext(ctx, `SELECT id FROM blobs WHERE sha256 = ''`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("look up empty sha256 blob: %w", err)
	}

	return id, nil
}

// ListGomodSiblingZipKeys returns the digests of valid zip blobs of other
// gomod_modules rows that share module path and version with an affected row
// (the same release published in another repository after the upgrade).
func (p *PostgresDB) ListGomodSiblingZipKeys(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT sb.sha256
		FROM gomod_modules s
		JOIN blobs sb ON sb.id = s.zip_blob_id AND sb.sha256 ~ '^[0-9a-f]{64}$'
		WHERE EXISTS (
			SELECT 1 FROM gomod_modules a
			JOIN blobs zb ON zb.id = a.zip_blob_id
			WHERE a.module_path = s.module_path AND a.version = s.version AND `+invalidSHA256Cond+`)`)
	if err != nil {
		return nil, fmt.Errorf("list sibling go zip blobs: %w", err)
	}

	return ScanStrings(rows)
}

// UnreferencedBlobKeys returns the subset of ids (blob store keys) that no
// blobs row carries as its sha256.
func (p *PostgresDB) UnreferencedBlobKeys(ctx context.Context, ids []string) ([]string, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT k FROM unnest($1::text[]) AS k WHERE NOT EXISTS (SELECT 1 FROM blobs WHERE sha256 = k)`,
		ids)
	if err != nil {
		return nil, fmt.Errorf("filter unreferenced blob keys: %w", err)
	}

	return ScanStrings(rows)
}

// ScanStrings drains a single-text-column result set into a slice and closes rows.
func ScanStrings(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan string: %w", err)
		}

		out = append(out, s)
	}

	return out, rows.Err()
}

// RelinkGomodZips upserts the blobs row for the zip with digest sha256 and
// points the given gomod_modules rows at it, but only rows that still
// reference badBlobID (so a concurrent change is never overwritten). It runs
// under the same per-blob advisory lock as uploads and SafeDeleteBlob and
// returns how many rows were relinked.
func (p *PostgresDB) RelinkGomodZips(ctx context.Context, sha256 string, size int64, rowIDs []string) (int64, error) {
	if err := validateBlobSHA256(sha256); err != nil {
		return 0, err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin gomod relink tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('blob:' || $1, 0))`, sha256); err != nil {
		return 0, fmt.Errorf("acquire blob lock: %w", err)
	}

	var blobID string
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO blobs (sha256, size_bytes, media_type, blob_key)
		 VALUES ($1::text, $2, 'application/zip', $1::text)
		 ON CONFLICT (sha256) DO UPDATE SET media_type = EXCLUDED.media_type
		 RETURNING id`, sha256, size).Scan(&blobID); err != nil {
		return 0, fmt.Errorf("upsert relinked zip blob: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE gomod_modules gm SET zip_blob_id = $1
		 FROM blobs zb
		 WHERE gm.id::text = ANY($2::text[]) AND zb.id = gm.zip_blob_id AND `+invalidSHA256Cond,
		blobID, rowIDs)
	if err != nil {
		return 0, fmt.Errorf("relink go module rows: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("relinked row count: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit gomod relink: %w", err)
	}

	return n, nil
}

// DeleteUnreferencedInvalidBlobRow deletes the blobs row id (a row with an
// invalid sha256) from the database only, when nothing references it any more.
// It never touches the storage object: for the shared "" row that object is a
// real module's zip, now relinked under its true digest. Returns true when the
// row was deleted.
func (p *PostgresDB) DeleteUnreferencedInvalidBlobRow(ctx context.Context, id string) (bool, error) {
	res, err := p.db.ExecContext(ctx,
		`DELETE FROM blobs b
		 WHERE b.id = $1 AND b.sha256 !~ '^[0-9a-f]{64}$' AND `+blobUnreferencedExpr,
		id)
	if err != nil {
		if IsForeignKeyViolation(err) {
			return false, nil
		}

		return false, fmt.Errorf("delete invalid blob row: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("deleted blob row count: %w", err)
	}

	return n > 0, nil
}
