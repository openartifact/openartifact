// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// The generic package tables (packages, package_versions, package_files; see
// docs/design/format-plugins.md) hold every format but docker. The per-format
// stores (pkg_<format>.go) keep their typed methods and models and compose
// the helpers below: one transaction per upload through writePackage, hard
// deletes through deleteFiles. Nothing here knows a format: the format's store
// computes name_key, version_key, path and the JSON columns and the generic
// subsystems (listing, trash, quota, search, ...) read the result.

// Roles of a package_files row. Only a primary file counts as an artifact for
// quota and listing statistics.
const (
	FileRolePrimary = "primary"
)

// PackageWrite is one upload: a package, one version of it and the files of
// that version, written together.
type PackageWrite struct {
	RepoID string
	// Format is the repository format; it is stored on the package.
	Format string
	// Name is the display (listing) name, NameKey the identity inside the
	// repository (the normalised name; equal to Name when the format does not
	// normalise).
	Name, NameKey string
	// PkgAttrs is merged into packages.attrs.
	PkgAttrs map[string]any
	// Version is the display version, VersionKey its identity inside the
	// package (the version plus the coordinates the format keys on).
	Version, VersionKey string
	// VersionMeta is stored as package_versions.metadata of a new version; for
	// an existing live version it replaces the stored value only when
	// ReplaceVersionMeta is set.
	VersionMeta        any
	ReplaceVersionMeta bool
	VersionUnitKey     string
	// Exclusive makes the write fail with ErrDuplicate when the live version
	// already has a live file (a format whose upload is one file per version).
	Exclusive bool
	// CreatedAt overrides the creation time of the rows (restores and imports).
	CreatedAt time.Time
	Files     []FileWrite
}

// FileWrite is one package_files row of a PackageWrite.
type FileWrite struct {
	Path, Filename string
	// Blob is stored (or found) by its digest before the transaction starts.
	Blob BlobWrite
	// SizeBytes is the declared size that wins over the blob's; nil when the
	// blob's is the size.
	SizeBytes *int64
	// Role defaults to FileRolePrimary.
	Role string
	// Unlisted keeps the file out of the repository listing (go mod and info).
	Unlisted bool
	Arch     string
	Part     string
	Attrs    any
	// Checksums are the non-sha256 digests the checksum search matches
	// ("sha1", "md5", "sha512", "shasum", "integrity").
	Checksums     map[string]string
	UnitKey       string
	DownloadCount int64
}

// BlobWrite names the content of a file: its sha256 digest, storage key,
// media type and size. A zero BlobWrite (no digest) means a file without a blob.
type BlobWrite struct {
	SHA256, Key, MediaType string
	Size                   int64
}

// PackageWritten is the result of writePackage: the ids of the rows written,
// in the order of PackageWrite.Files for the files.
type PackageWritten struct {
	PackageID      string
	VersionID      string
	VersionCreated bool
	Files          []FileWritten
}

// FileWritten is the generated part of one written file.
type FileWritten struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// jsonOrEmpty marshals v as a JSON object ("{}" for nil).
func jsonOrEmpty(v any) (json.RawMessage, error) {
	switch t := v.(type) {
	case nil:
		return json.RawMessage(`{}`), nil
	case json.RawMessage:
		if len(t) == 0 {
			return json.RawMessage(`{}`), nil
		}

		return t, nil
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal json column: %w", err)
	}

	if string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}

	return raw, nil
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return &t
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// writePackage inserts a package (or finds it), the live version and its
// files in one transaction. A live file with the same path in the repository,
// or an Exclusive version that already has live files, fails with ErrDuplicate
// and nothing is written.
func (p *core) writePackage(ctx context.Context, w *PackageWrite) (*PackageWritten, error) {
	blobIDs := make([]*string, len(w.Files))

	for i := range w.Files {
		b := w.Files[i].Blob
		if b.SHA256 == "" {
			continue
		}

		id, err := p.upsertBlobWithMediaType(ctx, b.SHA256, b.Key, b.MediaType, b.Size)
		if err != nil {
			return nil, fmt.Errorf("blob of %s: %w", w.Files[i].Path, err)
		}

		blobIDs[i] = &id
	}

	pkgAttrs, err := jsonOrEmpty(w.PkgAttrs)
	if err != nil {
		return nil, err
	}

	verMeta, err := jsonOrEmpty(w.VersionMeta)
	if err != nil {
		return nil, err
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin package write: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	out, err := p.writePackageTx(ctx, p.q.WithTx(tx), w, blobIDs, pkgAttrs, verMeta)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit package write: %w", err)
	}

	return out, nil
}

func (p *core) writePackageTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	w *PackageWrite,
	blobIDs []*string,
	pkgAttrs, verMeta json.RawMessage,
) (*PackageWritten, error) {
	pkgID, err := q.UpsertPackage(ctx, dbsqlc.UpsertPackageParams{
		RepositoryID: w.RepoID,
		Format:       w.Format,
		Name:         w.Name,
		NameKey:      w.NameKey,
		Attrs:        pkgAttrs,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert package: %w", err)
	}

	out := &PackageWritten{PackageID: pkgID, Files: make([]FileWritten, 0, len(w.Files))}

	out.VersionID, out.VersionCreated, err = p.liveVersion(ctx, q, w, pkgID, verMeta)
	if err != nil {
		return nil, err
	}

	if !out.VersionCreated && w.Exclusive {
		live, err := q.VersionHasLiveFiles(ctx, out.VersionID)
		if err != nil {
			return nil, fmt.Errorf("check live files: %w", err)
		}

		if live {
			return nil, ErrDuplicate
		}
	}

	if !out.VersionCreated && w.ReplaceVersionMeta {
		if err := q.SetVersionMetadata(ctx, dbsqlc.SetVersionMetadataParams{
			ID:       out.VersionID,
			Metadata: verMeta,
			UnitKey:  strOrNil(w.VersionUnitKey),
		}); err != nil {
			return nil, fmt.Errorf("set version metadata: %w", err)
		}
	}

	for i := range w.Files {
		f := &w.Files[i]

		attrs, err := jsonOrEmpty(f.Attrs)
		if err != nil {
			return nil, err
		}

		sums, err := jsonOrEmpty(f.Checksums)
		if err != nil {
			return nil, err
		}

		role := f.Role
		if role == "" {
			role = FileRolePrimary
		}

		row, err := q.InsertPackageFile(ctx, dbsqlc.InsertPackageFileParams{
			VersionID:     out.VersionID,
			RepositoryID:  w.RepoID,
			Path:          f.Path,
			Filename:      f.Filename,
			BlobID:        blobIDs[i],
			SizeBytes:     f.SizeBytes,
			Role:          role,
			Listed:        !f.Unlisted,
			Arch:          f.Arch,
			Part:          f.Part,
			Attrs:         attrs,
			Checksums:     sums,
			UnitKey:       strOrNil(f.UnitKey),
			DownloadCount: f.DownloadCount,
			CreatedAt:     timePtr(w.CreatedAt),
		})
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDuplicate
		}

		if err != nil {
			return nil, fmt.Errorf("insert package file %s: %w", f.Path, err)
		}

		out.Files = append(out.Files, FileWritten{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}

	return out, nil
}

// liveVersion finds the live version of the package or creates it; a writer
// that loses the race for a new version reads the winner's.
func (p *core) liveVersion(
	ctx context.Context,
	q *dbsqlc.Queries,
	w *PackageWrite,
	pkgID string,
	verMeta json.RawMessage,
) (id string, created bool, err error) {
	id, err = q.GetLiveVersionID(ctx, dbsqlc.GetLiveVersionIDParams{PackageID: pkgID, VersionKey: w.VersionKey})
	if err == nil {
		return id, false, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("get live version: %w", err)
	}

	id, err = q.InsertVersion(ctx, dbsqlc.InsertVersionParams{
		PackageID:    pkgID,
		RepositoryID: w.RepoID,
		Version:      w.Version,
		VersionKey:   w.VersionKey,
		Metadata:     verMeta,
		UnitKey:      strOrNil(w.VersionUnitKey),
		CreatedAt:    timePtr(w.CreatedAt),
	})
	if err == nil {
		return id, true, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("insert version: %w", err)
	}

	id, err = q.GetLiveVersionID(ctx, dbsqlc.GetLiveVersionIDParams{PackageID: pkgID, VersionKey: w.VersionKey})
	if err != nil {
		return "", false, fmt.Errorf("get live version after conflict: %w", err)
	}

	return id, false, nil
}

// deleteFiles hard-deletes files of a repository (the quota triggers release
// their usage) together with the versions and packages they leave empty. It
// returns the number of files removed.
func (p *core) deleteFiles(ctx context.Context, repoID string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin delete package files: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	rows, err := q.DeletePackageFilesByID(ctx, dbsqlc.DeletePackageFilesByIDParams{RepositoryID: repoID, Ids: ids})
	if err != nil {
		return 0, fmt.Errorf("delete package files: %w", err)
	}

	if err := pruneEmpty(ctx, q, versionIDsOf(rows)); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit delete package files: %w", err)
	}

	return len(rows), nil
}

func versionIDsOf(rows []dbsqlc.DeletePackageFilesByIDRow) []string {
	seen := make(map[string]struct{}, len(rows))
	ids := make([]string, 0, len(rows))

	for _, r := range rows {
		if _, ok := seen[r.VersionID]; ok {
			continue
		}

		seen[r.VersionID] = struct{}{}
		ids = append(ids, r.VersionID)
	}

	return ids
}

// pruneEmpty removes the given versions when they have no file left, and then
// the packages that have no version left.
func pruneEmpty(ctx context.Context, q *dbsqlc.Queries, versionIDs []string) error {
	if len(versionIDs) == 0 {
		return nil
	}

	pkgIDs, err := q.PruneEmptyVersions(ctx, versionIDs)
	if err != nil {
		return fmt.Errorf("prune empty versions: %w", err)
	}

	if len(pkgIDs) == 0 {
		return nil
	}

	if err := q.PruneEmptyPackages(ctx, pkgIDs); err != nil {
		return fmt.Errorf("prune empty packages: %w", err)
	}

	return nil
}
