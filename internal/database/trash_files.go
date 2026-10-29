// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// The generic trash engine of every package format except docker.
//
// A trash item is one LISTED package_files row. Trashing stamps the file
// (deleted_at, deleted_by) and, in the same transaction and with the same
// timestamp, the version when its last live file was stamped, so a version is
// live exactly while it has a live file. With storage.ScopeVersion the item is
// the whole version: the files of the version trashed with it share the stamp.
// Restoring reverses that, re-attaching the file to the live version with the
// same version_key when the version was republished meanwhile. Purging deletes
// the files for good and prunes the versions and packages left empty; the
// DELETE fires the quota_pending_package_files trigger, which is what the quota
// give-back of the purge worker (DecrementStorageUsage) takes.

// sqlTx is the slice of *sql.Tx the engine needs, so a caller can hand its own
// open transaction in.
type sqlTx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TrashFilesInTx soft-deletes package files of repoID inside the caller's
// transaction, whatever the repository's retention: a purge removes them for
// good once the retention window has passed. A caller that replaces live
// content (a forced bundle promotion) uses it so the replaced files stay
// restorable. ids are listed package_files ids; the repository's format decides
// the scope and hooks. ErrTrashNotFound means none of the files is live in
// repoID.
func TrashFilesInTx(ctx context.Context, tx sqlTx, repoID string, fileIDs []string, actor string) error {
	return trashFilesExec(ctx, tx, repoID, fileIDs, actor)
}

// repoFormatDescriptor returns the format of repoID and its trash descriptor.
func repoFormatDescriptor(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, repoID string,
) (storage.Trash, error) {
	var format string

	err := q.QueryRowContext(ctx, `SELECT format FROM repositories WHERE id = $1::uuid`, repoID).Scan(&format)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Trash{}, ErrTrashNotFound
	}

	if err != nil {
		return storage.Trash{}, fmt.Errorf("resolve repository format: %w", err)
	}

	return trashDescriptor(format)
}

// runHooks runs the descriptor statements with $1 = id.
func runHooks(ctx context.Context, tx sqlTx, stmts []string, id string) error {
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return fmt.Errorf("trash hook: %w", err)
		}
	}

	return nil
}

// idPair is a file id with the id of its version.
type idPair struct{ id, versionID string }

// readPairs drains rows of (id, version_id) and closes them, so the
// transaction is free for the next statement.
func readPairs(rows *sql.Rows) ([]idPair, error) {
	defer func() { _ = rows.Close() }()

	var out []idPair

	for rows.Next() {
		var p idPair
		if err := rows.Scan(&p.id, &p.versionID); err != nil {
			return nil, fmt.Errorf("scan trashed file: %w", err)
		}

		out = append(out, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trashed files: %w", err)
	}

	return out, nil
}

// trashFilesExec stamps the files, their companions and their versions.
func trashFilesExec(ctx context.Context, tx sqlTx, repoID string, ids []string, deletedBy string) error {
	if len(ids) == 0 {
		return ErrTrashNotFound
	}

	desc, err := repoFormatDescriptor(ctx, tx, repoID)
	if err != nil {
		return err
	}

	// One transaction, one now(): the files and the version carry the same stamp,
	// which is how a restore recognises what was trashed together.
	rows, err := tx.QueryContext(ctx,
		`UPDATE package_files SET deleted_at = now(), deleted_by = $3
		 WHERE id = ANY($1::uuid[]) AND repository_id = $2::uuid AND deleted_at IS NULL
		 RETURNING id::text, version_id::text`,
		uuidArray(ids), repoID, deletedBy)
	if err != nil {
		return fmt.Errorf("trash package files: %w", err)
	}

	stamped, err := readPairs(rows)
	if err != nil {
		return err
	}

	if len(stamped) == 0 {
		return ErrTrashNotFound
	}

	versionIDs := make([]string, 0, len(stamped))
	for _, s := range stamped {
		versionIDs = append(versionIDs, s.versionID)
	}

	slices.Sort(versionIDs)
	versionIDs = slices.Compact(versionIDs)

	if desc.Scope == storage.ScopeVersion {
		if _, err := tx.ExecContext(ctx,
			`UPDATE package_files SET deleted_at = now(), deleted_by = $2
			 WHERE version_id = ANY($1::uuid[]) AND deleted_at IS NULL`,
			uuidArray(versionIDs), deletedBy); err != nil {
			return fmt.Errorf("trash version companions: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE package_versions v SET deleted_at = now(), deleted_by = $2
		 WHERE v.id = ANY($1::uuid[]) AND v.deleted_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM package_files f WHERE f.version_id = v.id AND f.deleted_at IS NULL)`,
		uuidArray(versionIDs), deletedBy); err != nil {
		return fmt.Errorf("trash empty versions: %w", err)
	}

	for _, s := range stamped {
		if err := runHooks(ctx, tx, desc.OnTrash, s.id); err != nil {
			return err
		}
	}

	return nil
}

// unitFile is one file of a trash unit with what the quota and blob cleanup need.
type unitFile struct {
	id        string
	versionID string
	primary   bool
	size      int64
	sha256    string
}

// loadUnit locks and returns the files that go with the item anchored at id: the
// file itself, and with ScopeVersion every file of its version in the same state
// (trashed with the same stamp, or live).
func loadUnit(ctx context.Context, tx sqlTx, scope storage.TrashScope, repoID, id string) ([]unitFile, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT f.id::text, f.version_id::text, f.role = 'primary',
		        COALESCE(b.size_bytes, 0)::bigint, COALESCE(b.sha256, '')
		 FROM package_files a
		 JOIN package_files f ON f.id = a.id
		   OR ($3::bool AND f.version_id = a.version_id AND f.deleted_at IS NOT DISTINCT FROM a.deleted_at)
		 LEFT JOIN blobs b ON b.id = f.blob_id
		 WHERE a.id = $1::uuid AND a.repository_id = $2::uuid
		 ORDER BY f.id
		 FOR UPDATE OF f`,
		id, repoID, scope == storage.ScopeVersion)
	if err != nil {
		return nil, fmt.Errorf("load trash unit: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []unitFile

	for rows.Next() {
		var u unitFile
		if err := rows.Scan(&u.id, &u.versionID, &u.primary, &u.size, &u.sha256); err != nil {
			return nil, fmt.Errorf("scan trash unit: %w", err)
		}

		out = append(out, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trash unit: %w", err)
	}

	return out, nil
}

func unitIDs(unit []unitFile) []string {
	ids := make([]string, len(unit))
	for i, u := range unit {
		ids[i] = u.id
	}

	return ids
}

// restoreFilesExec un-stamps the item's unit. A version the unit emptied of
// live files is revived, or - when the version was republished while the item
// sat in the trash - the files move into the live version of the same key.
func restoreFilesExec(ctx context.Context, tx sqlTx, item *models.TrashItem) error {
	desc, err := trashDescriptor(item.Format)
	if err != nil {
		return err
	}

	unit, err := loadUnit(ctx, tx, desc.Scope, item.RepositoryID, item.ID)
	if err != nil {
		return err
	}

	if len(unit) == 0 {
		return ErrTrashNotFound
	}

	var (
		versionID, packageID, versionKey string
		versionTrashed                   bool
	)

	if err := tx.QueryRowContext(ctx,
		`SELECT v.id::text, v.package_id::text, v.version_key, v.deleted_at IS NOT NULL
		 FROM package_files f JOIN package_versions v ON v.id = f.version_id
		 WHERE f.id = $1::uuid`, item.ID).Scan(&versionID, &packageID, &versionKey, &versionTrashed); err != nil {
		return fmt.Errorf("load version of trashed file: %w", err)
	}

	// The live version of the same key that took over, when there is one.
	var liveID string

	err = tx.QueryRowContext(ctx,
		`SELECT id::text FROM package_versions
		 WHERE package_id = $1::uuid AND version_key = $2 AND deleted_at IS NULL AND id <> $3::uuid`,
		packageID, versionKey, versionID).Scan(&liveID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look up live version: %w", err)
	}

	switch {
	case liveID != "":
		_, err = tx.ExecContext(ctx,
			`UPDATE package_files SET version_id = $2::uuid, deleted_at = NULL, deleted_by = NULL
			 WHERE id = ANY($1::uuid[])`, uuidArray(unitIDs(unit)), liveID)
	case versionTrashed:
		_, err = tx.ExecContext(ctx,
			`UPDATE package_versions SET deleted_at = NULL, deleted_by = NULL
			 WHERE id = $1::uuid AND deleted_at IS NOT NULL`, versionID)
		if err == nil {
			_, err = tx.ExecContext(ctx,
				`UPDATE package_files SET deleted_at = NULL, deleted_by = NULL
				 WHERE id = ANY($1::uuid[])`, uuidArray(unitIDs(unit)))
		}
	default:
		_, err = tx.ExecContext(ctx,
			`UPDATE package_files SET deleted_at = NULL, deleted_by = NULL
			 WHERE id = ANY($1::uuid[])`, uuidArray(unitIDs(unit)))
	}

	if err != nil {
		if IsUniqueViolation(err) {
			return ErrTrashConflict
		}

		return fmt.Errorf("restore %s: %w", item.Kind, err)
	}

	if liveID != "" {
		// The old version only exists to hold what is still trashed.
		if err := pruneEmptyTx(ctx, tx, []string{versionID}); err != nil {
			return err
		}
	}

	return runHooks(ctx, tx, desc.OnRestore, item.ID)
}

// pruneEmptyTx deletes the given versions that hold no file any more, then the
// packages of those versions that hold no version any more.
func pruneEmptyTx(ctx context.Context, tx sqlTx, versionIDs []string) error {
	if len(versionIDs) == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`DELETE FROM package_versions v WHERE v.id = ANY($1::uuid[])
		   AND NOT EXISTS (SELECT 1 FROM package_files f WHERE f.version_id = v.id)
		 RETURNING v.package_id::text`, uuidArray(versionIDs))
	if err != nil {
		return fmt.Errorf("prune empty versions: %w", err)
	}

	pkgIDs, err := readStrings(rows)
	if err != nil {
		return err
	}

	if len(pkgIDs) == 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM packages p WHERE p.id = ANY($1::uuid[])
		   AND NOT EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id)`,
		uuidArray(pkgIDs)); err != nil {
		return fmt.Errorf("prune empty packages: %w", err)
	}

	return nil
}

// readStrings drains rows of one text column and closes them.
func readStrings(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		out = append(out, s)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}

	return out, nil
}

// deleteUnit deletes the unit of id for good (trashed or live, as it is), runs
// the purge hooks first, and returns what the caller has to give back. The
// emptied versions and packages are left for the caller's pruneEmptyTx.
func deleteUnit(ctx context.Context, tx sqlTx, desc storage.Trash, repoID, id string) ([]unitFile, error) {
	unit, err := loadUnit(ctx, tx, desc.Scope, repoID, id)
	if err != nil || len(unit) == 0 {
		return nil, err
	}

	if err := runHooks(ctx, tx, desc.OnPurge, id); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM package_files WHERE id = ANY($1::uuid[])`, uuidArray(unitIDs(unit))); err != nil {
		return nil, fmt.Errorf("delete package files: %w", err)
	}

	return unit, nil
}

// totals folds a unit into what the purge result reports: the blobs to free and
// the quota the unit held (one artifact per primary file, every blob's bytes).
func totals(unit []unitFile) models.FileDeletion {
	var out models.FileDeletion

	for _, u := range unit {
		if u.primary {
			out.QuotaArtifacts++
		}

		out.QuotaBytes += u.size

		if u.sha256 != "" {
			out.BlobSHA256 = append(out.BlobSHA256, u.sha256)
		}
	}

	slices.Sort(out.BlobSHA256)
	out.BlobSHA256 = slices.Compact(out.BlobSHA256)

	return out
}

func versionsOf(unit []unitFile) []string {
	ids := make([]string, len(unit))
	for i, u := range unit {
		ids[i] = u.versionID
	}

	slices.Sort(ids)

	return slices.Compact(ids)
}

// purgeFilesExec deletes a trashed item for good and fills res.
func purgeFilesExec(ctx context.Context, tx sqlTx, item *models.TrashItem, res *models.TrashPurgeResult) error {
	desc, err := trashDescriptor(item.Format)
	if err != nil {
		return err
	}

	unit, err := deleteUnit(ctx, tx, desc, item.RepositoryID, item.ID)
	if err != nil {
		return err
	}

	if len(unit) == 0 {
		return ErrTrashNotFound
	}

	if err := pruneEmptyTx(ctx, tx, versionsOf(unit)); err != nil {
		return err
	}

	sum := totals(unit)
	res.BlobSHA256, res.QuotaArtifacts, res.QuotaBytes = sum.BlobSHA256, sum.QuotaArtifacts, sum.QuotaBytes

	return nil
}

// HardDeleteFiles deletes the live units of the given listed files of repoID
// for good, in one transaction, bypassing the trash (the retention worker's
// hard-delete path, and the delete of a repository that keeps no trash). The
// hooks of the format's OnPurge run for each unit, then the files go and the
// versions and packages they leave empty are pruned. The result has one entry
// per id that was live; the caller frees the blobs and gives the quota back.
func (p *TrashStore) HardDeleteFiles(ctx context.Context, repoID string, ids []string) ([]models.FileDeletion, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin delete transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	desc, err := repoFormatDescriptor(ctx, tx, repoID)
	if err != nil {
		return nil, err
	}

	var (
		out      []models.FileDeletion
		versions []string
	)

	for _, id := range ids {
		var live bool

		err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM package_files WHERE id = $1::uuid AND repository_id = $2::uuid AND deleted_at IS NULL)`,
			id, repoID).Scan(&live)
		if err != nil {
			return nil, fmt.Errorf("check file: %w", err)
		}

		if !live {
			continue
		}

		unit, err := deleteUnit(ctx, tx, desc, repoID, id)
		if err != nil {
			return nil, err
		}

		sum := totals(unit)
		sum.ID = id
		out = append(out, sum)
		versions = append(versions, versionsOf(unit)...)
	}

	if err := pruneEmptyTx(ctx, tx, versions); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit delete transaction: %w", err)
	}

	return out, nil
}
