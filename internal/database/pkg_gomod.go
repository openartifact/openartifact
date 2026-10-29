// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Queries behind the Go module zip repair (internal/format/gomod/repair.go).
// OpenArtifact <= 0.4.1 stored every Go module zip under ONE blobs row whose
// sha256 was ''; these methods find the rows that still point at such a row,
// relink them to a row keyed by the zip's real digest, and drop the shared row.

// GoModStore owns the Go modules and the repair queries of the gomod repair task.
//
// Go modules live in the generic package tables (repository format "go"; the
// mapping is the maintainers' reference, derived from migrations/001_baseline.sql):
//
//	packages          one per module path: name = name_key = module path (case-encoded
//	                  as the handler stores it), attrs {}
//	package_versions  one per module version incarnation: version = version_key =
//	                  "v1.2.3" (pseudo-versions included), metadata {}; created_at is
//	                  what .info reports as Time
//	package_files     two per version, trashed as a unit (the trash engine works on
//	                  the version scope, so trashing the listed zip id stamps the mod file too):
//	                    zip  path "<module>/@v/<version>.zip", filename "<version>.zip",
//	                         role primary, listed, unit_key "<module>@<version>" (the
//	                         federation unit is the version, content ref = the zip blob)
//	                    mod  path "<module>/@v/<version>.mod", filename "<version>.mod",
//	                         role mod, unlisted, attrs {"go_mod_content": "<go.mod text>"}:
//	                         .mod is served from this attribute, never from the blob
//
// The ID of a GoModule is the zip file id (the listing, scan and blob-component
// id). Quota: the baseline trigger counts one artifact per primary file and the
// bytes of every blob, so an upload reserves the zip as (size, 1) and the go.mod
// as (size, 0); a hard delete releases both. There is no .info file: it is
// synthesised from the version and created_at.
type GoModStore struct{ *core }

const (
	gomodFormat = "go"
	// gomodModRole is the role of the go.mod file (only a primary file counts as an artifact).
	gomodModRole = "mod"
)

// gomodRepairLockKey names the session advisory lock that keeps a single
// replica running the repair.
const gomodRepairLockKey = "openartifact:gomod-zip-repair"

// invalidSHA256Cond is true for a blobs row (alias zb) whose sha256 is not a
// lowercase 64-character hex digest.
const invalidSHA256Cond = `zb.sha256 !~ '^[0-9a-f]{64}$'`

// GomodAffectedRow is a Go module version (trashed ones included) whose zip
// blob row has an invalid sha256. ID is the id of the zip file.
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
func (p *GoModStore) TryGomodRepairLock(ctx context.Context) (func(), bool, error) {
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

// ListGomodAffectedRows returns every Go module version whose zip blob row has
// a sha256 that is not 64 lowercase hex characters.
func (p *GoModStore) ListGomodAffectedRows(ctx context.Context) ([]GomodAffectedRow, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT zf.id, zf.repository_id, r.name, p.name, v.version, COALESCE(mf.attrs->>'go_mod_content', ''),
		       zf.deleted_at IS NOT NULL, zb.id, zb.blob_key
		FROM package_files zf
		JOIN blobs zb ON zb.id = zf.blob_id
		JOIN package_versions v ON v.id = zf.version_id
		JOIN packages p ON p.id = v.package_id AND p.format = $1
		JOIN repositories r ON r.id = zf.repository_id
		LEFT JOIN package_files mf ON mf.version_id = zf.version_id AND mf.role = $3
		WHERE zf.role = $2 AND `+invalidSHA256Cond+`
		ORDER BY r.name, p.name, v.version`, gomodFormat, FileRolePrimary, gomodModRole)
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
func (p *GoModStore) EmptySHA256BlobID(ctx context.Context) (string, error) {
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
// Go module versions that share module path and version with an affected row
// (the same release published in another repository after the upgrade).
func (p *GoModStore) ListGomodSiblingZipKeys(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT sb.sha256
		FROM package_files s
		JOIN package_versions sv ON sv.id = s.version_id
		JOIN packages sp ON sp.id = sv.package_id AND sp.format = $1
		JOIN blobs sb ON sb.id = s.blob_id AND sb.sha256 ~ '^[0-9a-f]{64}$'
		WHERE s.role = $2 AND EXISTS (
			SELECT 1 FROM package_files a
			JOIN package_versions av ON av.id = a.version_id
			JOIN packages ap ON ap.id = av.package_id AND ap.format = $1
			JOIN blobs zb ON zb.id = a.blob_id
			WHERE a.role = $2 AND ap.name = sp.name AND av.version = sv.version AND `+invalidSHA256Cond+`)`,
		gomodFormat, FileRolePrimary)
	if err != nil {
		return nil, fmt.Errorf("list sibling go zip blobs: %w", err)
	}

	return ScanStrings(rows)
}

// UnreferencedBlobKeys returns the subset of ids (blob store keys) that no
// blobs row carries as its sha256.
func (p *GoModStore) UnreferencedBlobKeys(ctx context.Context, ids []string) ([]string, error) {
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
// points the given zip files at it, but only rows that still
// reference badBlobID (so a concurrent change is never overwritten). It runs
// under the same per-blob advisory lock as uploads and SafeDeleteBlob and
// returns how many rows were relinked.
func (p *GoModStore) RelinkGomodZips(ctx context.Context, sha256 string, size int64, rowIDs []string) (int64, error) {
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
		`UPDATE package_files zf SET blob_id = $1
		 FROM blobs zb
		 WHERE zf.id::text = ANY($2::text[]) AND zf.role = $3 AND zb.id = zf.blob_id AND `+invalidSHA256Cond,
		blobID, rowIDs, FileRolePrimary)
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
func (p *GoModStore) DeleteUnreferencedInvalidBlobRow(ctx context.Context, id string) (bool, error) {
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

// moduleFilePath is the path of one file of a module version.
func moduleFilePath(modulePath, version, ext string) string {
	return modulePath + "/@v/" + version + ext
}

// CreateGoModule creates a new Go module version: the package, the version and
// its zip and go.mod files in one transaction. The zip blob row is keyed by
// ZipHash; the mod blob has no digest field on models.GoModule, so its own blob
// key serves as the blobs.sha256 value (self-referential, mirrors the docker blob
// pattern; it never collides since blob keys are unique per upload). A module
// whose go.mod blob key is empty gets a go.mod file without a blob: the go.mod
// text lives in the file's attrs. A live version of the same module is
// ErrDuplicate.
func (p *GoModStore) CreateGoModule(ctx context.Context, mod *models.GoModule) error {
	files := []FileWrite{
		{
			Path:      moduleFilePath(mod.ModulePath, mod.Version, ".zip"),
			Filename:  mod.Version + ".zip",
			Blob:      BlobWrite{SHA256: mod.ZipHash, Key: mod.ZipBlobKey, MediaType: "application/zip", Size: mod.ZipSizeBytes},
			Role:      FileRolePrimary,
			UnitKey:   mod.ModulePath + "@" + mod.Version,
			Checksums: map[string]string{},
		},
		{
			Path:     moduleFilePath(mod.ModulePath, mod.Version, ".mod"),
			Filename: mod.Version + ".mod",
			Role:     gomodModRole,
			Unlisted: true,
			Attrs:    map[string]string{"go_mod_content": mod.GoModContent},
		},
	}

	if mod.ModBlobKey != "" {
		files[1].Blob = BlobWrite{
			SHA256: mod.ModBlobKey, Key: mod.ModBlobKey, MediaType: "text/plain", Size: int64(len(mod.GoModContent)),
		}
	}

	written, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     mod.RepositoryID,
		Format:     gomodFormat,
		Name:       mod.ModulePath,
		NameKey:    mod.ModulePath,
		Version:    mod.Version,
		VersionKey: mod.Version,
		Exclusive:  true,
		CreatedAt:  mod.CreatedAt,
		Files:      files,
	})
	if err != nil {
		return fmt.Errorf("failed to create go module: %w", err)
	}

	mod.ID = written.Files[0].ID
	mod.CreatedAt = written.Files[0].CreatedAt
	mod.UpdatedAt = written.Files[0].UpdatedAt

	return nil
}

// GetGoModule retrieves a Go module by repo, path, and version. A version
// without a live zip file is not found.
func (p *GoModStore) GetGoModule(ctx context.Context, repoID, modulePath, version string) (*models.GoModule, error) {
	row, err := p.q.GetGoModule(ctx, dbsqlc.GetGoModuleParams{
		RepositoryID: repoID,
		ModulePath:   modulePath,
		Version:      version,
	})

	return mapRow(row, err, "get go module", goModuleFromRow)
}

// goModuleRow lists the sqlc row types goModuleFromRow converts.
type goModuleRow interface {
	dbsqlc.GetGoModuleRow | dbsqlc.ListGoModuleVersionsRow
}

func goModuleFromRow[R goModuleRow](in R) *models.GoModule {
	row := dbsqlc.GetGoModuleRow(in)

	return &models.GoModule{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		ModulePath:   row.ModulePath,
		Version:      row.Version,
		GoModContent: row.GoModContent,
		ZipBlobKey:   row.ZipBlobKey,
		ModBlobKey:   row.ModBlobKey,
		ZipSizeBytes: row.ZipSizeBytes,
		ZipHash:      row.ZipHash,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// ListGoModuleVersions lists all versions of a module, oldest first.
func (p *GoModStore) ListGoModuleVersions(ctx context.Context, repoID, modulePath string) ([]*models.GoModule, error) {
	rows, err := p.q.ListGoModuleVersions(ctx, dbsqlc.ListGoModuleVersionsParams{
		RepositoryID: repoID,
		ModulePath:   modulePath,
	})

	return mapRowsOrNil(rows, err, "list go module versions", goModuleFromRow)
}
