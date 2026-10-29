// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"crypto/md5" //nolint:gosec // derives ids deterministically, not a security primitive
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Backup and restore of the generic package tables (packages, package_versions,
// package_files, package_tags and the side and companion tables a format's
// storage descriptor names) is done in bulk as JSON rows. Nothing here knows a
// format: the backup engine passes the table names from the descriptor.
//
// Export reads rows as jsonb straight from the snapshot. Restore inserts rows
// into a repository the restore just created, so every id of the archive is
// replaced by an id derived from it and the new repository id
// (md5(repository || old id)::uuid): rows that point at each other still do,
// and the restored rows can never collide with rows of the repository the
// archive was written from, even when that one still exists or sits in the
// trash. The one reference that leaves the repository, a file's blob, travels
// as the blob's sha256 and size and is resolved against the blobs table.

// PackageRowKind selects which table of the package model a PackageRowSet is.
type PackageRowKind string

// The kinds of row sets of a repository's package data, in restore order
// (parents first).
const (
	// PackageRowsCompanion is a repository-scoped table side rows reference.
	PackageRowsCompanion PackageRowKind = "companion"
	// PackageRowsPackages is the packages table.
	PackageRowsPackages PackageRowKind = "packages"
	// PackageRowsVersions is the package_versions table.
	PackageRowsVersions PackageRowKind = "package_versions"
	// PackageRowsFiles is the package_files table.
	PackageRowsFiles PackageRowKind = "package_files"
	// PackageRowsSide is a side table of package_files.
	PackageRowsSide PackageRowKind = "side"
	// PackageRowsTags is the package_tags table.
	PackageRowsTags PackageRowKind = "package_tags"
)

// PackageRowSet names one table of a repository's package data to export or restore.
type PackageRowSet struct {
	Kind PackageRowKind
	// Table is the table of a PackageRowsSide or PackageRowsCompanion set (a
	// plain identifier from a storage descriptor).
	Table string
	// Scope is the SQL condition over the alias t selecting a companion's rows of
	// repository $1 (export only).
	Scope string
	// RepoColumn is the companion column set to the new repository on restore.
	RepoColumn string
	// Refs are the uuid columns that reference companion ids (restore only).
	Refs []string
}

// Name is the table the set reads and writes.
func (s PackageRowSet) Name() string {
	switch s.Kind {
	case PackageRowsPackages, PackageRowsVersions, PackageRowsFiles, PackageRowsTags:
		return string(s.Kind)
	case PackageRowsSide, PackageRowsCompanion:
		return s.Table
	}

	return ""
}

var (
	// ErrPackageRowSet reports a row set that names no valid table.
	ErrPackageRowSet = errors.New("invalid package row set")

	tableIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// table returns the table name of the set, checked to be a plain identifier
// before it is spliced into a statement.
func (s PackageRowSet) table() (string, error) {
	name := s.Name()
	if !tableIdentifier.MatchString(name) {
		return "", fmt.Errorf("%w: table %q", ErrPackageRowSet, name)
	}

	return name, nil
}

// A package is archived when it has a live version, a version when it is live,
// a file when it and its version are live: trashed data is not part of a backup.
const (
	exportPackages = `SELECT to_jsonb(p) FROM packages p
		WHERE p.repository_id = $1::uuid
		  AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id AND v.deleted_at IS NULL)
		ORDER BY p.id`
	exportVersions = `SELECT to_jsonb(v) FROM package_versions v
		WHERE v.repository_id = $1::uuid AND v.deleted_at IS NULL
		ORDER BY v.id`
	// A file's blob travels as its sha256 and size.
	exportFiles = `SELECT (to_jsonb(f) - 'blob_id') || jsonb_build_object('blob_sha256', b.sha256, 'blob_size', b.size_bytes)
		FROM package_files f
		JOIN package_versions v ON v.id = f.version_id AND v.deleted_at IS NULL
		LEFT JOIN blobs b ON b.id = f.blob_id
		WHERE f.repository_id = $1::uuid AND f.deleted_at IS NULL
		ORDER BY f.id`
	exportTags = `SELECT to_jsonb(t) FROM package_tags t
		JOIN packages p ON p.id = t.package_id
		WHERE p.repository_id = $1::uuid
		  AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id AND v.deleted_at IS NULL)
		ORDER BY t.id`
	exportSideFmt = `SELECT to_jsonb(t) FROM %s t
		JOIN package_files f ON f.id = t.id AND f.deleted_at IS NULL
		JOIN package_versions v ON v.id = f.version_id AND v.deleted_at IS NULL
		WHERE f.repository_id = $1::uuid
		ORDER BY t.id`
	exportCompanionFmt = `SELECT to_jsonb(t) FROM %s t WHERE %s ORDER BY t.id`
)

func (s PackageRowSet) exportSQL() (string, error) {
	switch s.Kind {
	case PackageRowsPackages:
		return exportPackages, nil
	case PackageRowsVersions:
		return exportVersions, nil
	case PackageRowsFiles:
		return exportFiles, nil
	case PackageRowsTags:
		return exportTags, nil
	case PackageRowsSide:
		table, err := s.table()
		if err != nil {
			return "", err
		}

		return fmt.Sprintf(exportSideFmt, table), nil
	case PackageRowsCompanion:
		table, err := s.table()
		if err != nil {
			return "", err
		}

		if s.Scope == "" {
			return "", fmt.Errorf("%w: companion %s has no scope", ErrPackageRowSet, table)
		}

		return fmt.Sprintf(exportCompanionFmt, table, s.Scope), nil
	}

	return "", fmt.Errorf("%w: kind %q", ErrPackageRowSet, s.Kind)
}

// ExportPackageRows streams the rows of set that belong to repoID, as of the
// snapshot, to fn as JSON objects (one jsonb value per call; fn owns the bytes).
func (p *BackupStore) ExportPackageRows(ctx context.Context, set PackageRowSet, repoID string, fn func(row []byte) error) error {
	query, err := set.exportSQL()
	if err != nil {
		return err
	}

	rows, err := p.db.QueryContext(ctx, query, repoID)
	if err != nil {
		return fmt.Errorf("export %s rows: %w", set.Name(), err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("scan %s row: %w", set.Name(), err)
		}

		if err := fn(raw); err != nil {
			return err
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s rows: %w", set.Name(), err)
	}

	return nil
}

// RestoredRowID is the id restore gives the archived row oldID of a repository
// restored as repoID: the Go twin of the SQL expression newID builds, for
// callers that must rewrite the ids a row embeds in other columns (the deb
// version key and partition carry companion ids).
func RestoredRowID(repoID, oldID string) string {
	sum := md5.Sum([]byte(repoID + oldID)) //nolint:gosec // see the import
	h := hex.EncodeToString(sum[:])

	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// newID is the SQL expression of the restored id of the archived id held in
// the jsonb field key of j: an id derived from the new repository ($1) so the
// same archived id always maps to the same new id inside one restored
// repository and never to one of another.
func newID(key string) string {
	return "md5($1::text || (j->>'" + key + "'))::uuid"
}

// restoreRowsFmt inserts a batch of archived rows ($2, a json array) into a
// table. Every column comes from the row; override replaces the ids and
// repository the restore re-derives. jsonb_populate_record turns the object
// into a row of the table, so the archive carries every column without a list
// of them.
const restoreRowsFmt = `INSERT INTO %[1]s
	SELECT (jsonb_populate_record(NULL::%[1]s, %[2]s)).*
	FROM jsonb_array_elements($2::jsonb) AS e(j)`

// restoreFilesSQL additionally resolves the blob each file row names: it
// upserts the blobs row (keyed by its sha256, like UpsertBlob) and links the
// file to it.
const restoreFilesSQL = `WITH rows AS (SELECT j FROM jsonb_array_elements($2::jsonb) AS e(j)),
	blob_rows AS (
		INSERT INTO blobs (sha256, size_bytes, blob_key)
		SELECT DISTINCT ON (j->>'blob_sha256') j->>'blob_sha256', COALESCE((j->>'blob_size')::bigint, 0), j->>'blob_sha256'
		FROM rows WHERE j->>'blob_sha256' IS NOT NULL
		ON CONFLICT (sha256) DO UPDATE SET
			size_bytes = CASE WHEN blobs.size_bytes = 0 THEN EXCLUDED.size_bytes ELSE blobs.size_bytes END
		RETURNING id, sha256
	)
	INSERT INTO package_files
	SELECT (jsonb_populate_record(NULL::package_files,
		(j - 'blob_sha256' - 'blob_size') || jsonb_build_object(
			'id', md5($1::text || (j->>'id'))::uuid,
			'version_id', md5($1::text || (j->>'version_id'))::uuid,
			'repository_id', $1::text,
			'blob_id', b.id))).*
	FROM rows LEFT JOIN blob_rows b ON b.sha256 = j->>'blob_sha256'`

// refsOverride is the jsonb object of the derived ids of the columns $3
// (text[]) of the row j.
const refsOverride = `COALESCE((SELECT jsonb_object_agg(k, md5($1::text || (j->>k))::uuid)
	FROM unnest($3::text[]) AS k WHERE j->>k IS NOT NULL), '{}'::jsonb)`

func (s PackageRowSet) restoreSQL() (string, error) {
	switch s.Kind {
	case PackageRowsPackages:
		return fmt.Sprintf(restoreRowsFmt, "packages",
			"j || jsonb_build_object('id', "+newID("id")+", 'repository_id', $1::text)"), nil
	case PackageRowsVersions:
		return fmt.Sprintf(restoreRowsFmt, "package_versions",
			"j || jsonb_build_object('id', "+newID("id")+", 'package_id', "+newID("package_id")+", 'repository_id', $1::text)"), nil
	case PackageRowsFiles:
		return restoreFilesSQL, nil
	case PackageRowsTags:
		return fmt.Sprintf(restoreRowsFmt, "package_tags",
			"j || jsonb_build_object('id', "+newID("id")+", 'package_id', "+newID("package_id")+")"), nil
	case PackageRowsSide:
		table, err := s.table()
		if err != nil {
			return "", err
		}

		return fmt.Sprintf(restoreRowsFmt, table,
			"j || jsonb_build_object('id', "+newID("id")+") || "+refsOverride), nil
	case PackageRowsCompanion:
		table, err := s.table()
		if err != nil {
			return "", err
		}

		return fmt.Sprintf(restoreRowsFmt, table,
			"j || jsonb_build_object('id', "+newID("id")+") || "+refsOverride+
				" || CASE WHEN $4::text <> '' THEN jsonb_build_object($4::text, $1::text) ELSE '{}'::jsonb END"), nil
	}

	return "", fmt.Errorf("%w: kind %q", ErrPackageRowSet, s.Kind)
}

// PackageRestoreBatch is the most rows one RestorePackageRows call takes.
const PackageRestoreBatch = 500

// RestorePackageRows inserts up to PackageRestoreBatch archived rows of set into
// repoID, a repository the restore created, in one statement. If the batch
// fails it retries the rows one by one so one bad row costs one row, not the
// batch: it returns how many rows were not restored and the first error. Rows
// are autocommitted, like every other restore step.
func (p *BackupStore) RestorePackageRows(ctx context.Context, set PackageRowSet, repoID string, rows []json.RawMessage) (int, error) {
	query, err := set.restoreSQL()
	if err != nil {
		return len(rows), err
	}

	err = p.restoreRows(ctx, set, query, repoID, rows)
	if err == nil || len(rows) == 1 {
		if err != nil {
			return 1, fmt.Errorf("restore %s rows: %w", set.Name(), err)
		}

		return 0, nil
	}

	var (
		failed   int
		firstErr error
	)

	for i := range rows {
		if ctx.Err() != nil {
			return failed + len(rows) - i, fmt.Errorf("restore %s rows: %w", set.Name(), ctx.Err())
		}

		if err := p.restoreRows(ctx, set, query, repoID, rows[i:i+1:i+1]); err != nil {
			failed++

			if firstErr == nil {
				firstErr = fmt.Errorf("restore %s rows: %w", set.Name(), err)
			}
		}
	}

	return failed, firstErr
}

type blobRef struct {
	SHA256 string `json:"blob_sha256"`
}

func (p *BackupStore) restoreRows(ctx context.Context, set PackageRowSet, query, repoID string, rows []json.RawMessage) error {
	if set.Kind == PackageRowsFiles {
		for _, row := range rows {
			var ref blobRef
			if err := json.Unmarshal(row, &ref); err != nil {
				return fmt.Errorf("decode file row: %w", err)
			}

			if ref.SHA256 == "" {
				continue
			}

			if err := validateBlobSHA256(ref.SHA256); err != nil {
				return err
			}
		}
	}

	payload, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("encode %s rows: %w", set.Name(), err)
	}

	args := []any{repoID, string(payload)}

	switch set.Kind {
	case PackageRowsSide:
		args = append(args, set.Refs)
	case PackageRowsCompanion:
		args = append(args, set.Refs, set.RepoColumn)
	}

	if _, err := p.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("insert %s rows: %w", set.Name(), err)
	}

	return nil
}
