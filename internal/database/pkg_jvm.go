// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// The JVM file formats (maven, ivy, sbt) share two write shapes the generic
// primitives do not cover: an upload to an existing path repoints the live
// file at the new content instead of failing (snapshot overwrite, republish),
// and an unpublish hard-deletes one file by id.

// upsertPackageFile writes w, which has exactly one file whose blob is the
// existing blobs row blobID, in one transaction. A live file already at the
// path is repointed at the blob (its attrs and checksums are replaced, its
// creation time is kept) instead. after runs in the same transaction with the
// id of the file that was inserted or repointed.
func (p *core) upsertPackageFile(
	ctx context.Context,
	w *PackageWrite,
	blobID string,
	after func(ctx context.Context, q *dbsqlc.Queries, fileID string) error,
) (FileWritten, error) {
	f := &w.Files[0]

	pkgAttrs, err := jsonOrEmpty(w.PkgAttrs)
	if err != nil {
		return FileWritten{}, err
	}

	verMeta, err := jsonOrEmpty(w.VersionMeta)
	if err != nil {
		return FileWritten{}, err
	}

	attrs, err := jsonOrEmpty(f.Attrs)
	if err != nil {
		return FileWritten{}, err
	}

	sums, err := jsonOrEmpty(f.Checksums)
	if err != nil {
		return FileWritten{}, err
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return FileWritten{}, fmt.Errorf("begin package file write: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	overwrite := func() (*FileWritten, error) {
		row, err := q.OverwritePackageFileByPath(ctx, dbsqlc.OverwritePackageFileByPathParams{
			BlobID: &blobID, Attrs: attrs, Checksums: sums, RepositoryID: w.RepoID, Path: f.Path,
		})
		if isNoRows(err) {
			return nil, nil //nolint:nilnil // no live file at the path
		}

		if err != nil {
			return nil, fmt.Errorf("overwrite package file: %w", err)
		}

		return &FileWritten{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
	}

	written, err := overwrite()
	if err != nil {
		return FileWritten{}, err
	}

	if written == nil {
		out, err := p.writePackageTx(ctx, q, w, []*string{&blobID}, pkgAttrs, verMeta)

		switch {
		case err == nil:
			written = &out.Files[0]
		case errors.Is(err, ErrDuplicate):
			// A concurrent upload to the same path won the insert; repoint its file.
			if written, err = overwrite(); err != nil {
				return FileWritten{}, err
			}

			if written == nil {
				return FileWritten{}, ErrDuplicate
			}
		default:
			return FileWritten{}, err
		}
	}

	if after != nil {
		if err := after(ctx, q, written.ID); err != nil {
			return FileWritten{}, fmt.Errorf("package file side rows: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return FileWritten{}, fmt.Errorf("commit package file write: %w", err)
	}

	return *written, nil
}

// deleteLiveFile hard-deletes the live file id (a no-op when there is none)
// and the version and package it leaves empty.
func (p *core) deleteLiveFile(ctx context.Context, id string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete package file: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	versionID, err := q.DeleteLivePackageFile(ctx, id)
	if isNoRows(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("delete package file: %w", err)
	}

	if err := pruneEmpty(ctx, q, []string{versionID}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete package file: %w", err)
	}

	return nil
}
