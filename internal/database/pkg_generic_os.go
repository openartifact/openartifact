// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// writeOSPackage is writePackage for a format with a side table (deb_files,
// rpm_files, pacman_files): the package rows, the files and the side rows are
// written in one transaction, so a side row never exists without its file and
// the other way round. blobIDs holds the blobs.id of each file of w.Files (the
// caller stored the blobs); side runs inside the transaction once the files
// have their ids.
func (p *core) writeOSPackage(
	ctx context.Context,
	w *PackageWrite,
	blobIDs []*string,
	side func(q *dbsqlc.Queries, files []FileWritten) error,
) (*PackageWritten, error) {
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

	q := p.q.WithTx(tx)

	out, err := p.writePackageTx(ctx, q, w, blobIDs, pkgAttrs, verMeta)
	if err != nil {
		return nil, err
	}

	if err := side(q, out.Files); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit package write: %w", err)
	}

	return out, nil
}

// deleteLiveFiles hard-deletes live files (trashed ones are left alone) and
// the versions and packages they leave empty. repoID "" matches any
// repository; formats, when not empty, restricts the files to packages of
// those formats. It returns the number of files removed.
func (p *core) deleteLiveFiles(ctx context.Context, repoID string, formats, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin delete package files: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	q := p.q.WithTx(tx)

	if formats == nil {
		formats = []string{}
	}

	rows, err := q.DeleteLivePackageFiles(ctx, dbsqlc.DeleteLivePackageFilesParams{
		Ids:          ids,
		RepositoryID: strOrNil(repoID),
		Formats:      formats,
	})
	if err != nil {
		return 0, fmt.Errorf("delete package files: %w", err)
	}

	seen := make(map[string]struct{}, len(rows))
	versionIDs := make([]string, 0, len(rows))

	for _, r := range rows {
		if _, ok := seen[r.VersionID]; ok {
			continue
		}

		seen[r.VersionID] = struct{}{}
		versionIDs = append(versionIDs, r.VersionID)
	}

	if err := pruneEmpty(ctx, q, versionIDs); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit delete package files: %w", err)
	}

	return len(rows), nil
}

// nonEmptyStrings drops the empty values of m: an absent metadata key reads
// back as "" (the typed models never tell the two apart).
func nonEmptyStrings(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))

	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}

	return out
}

// putNonZero sets key in an attrs map only when v is not zero: an absent key
// reads back as 0.
func putNonZero(m map[string]any, key string, v int64) {
	if v != 0 {
		m[key] = v
	}
}

// mapRowErr is mapRow for a conversion that can fail (it decodes a JSON column).
func mapRowErr[R, T any](row R, err error, action string, mapf func(R) (*T, error)) (*T, error) {
	v, err := orNotFound(&row, err, action)
	if v == nil || err != nil {
		return nil, err
	}

	return mapf(*v)
}
