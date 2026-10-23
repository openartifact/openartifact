// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrPackageNotFound means no live package row has the id in the repository.
var ErrPackageNotFound = errors.New("package not found")

// DeletedPackage describes what an explicit package delete removed.
type DeletedPackage struct {
	// ID is the id the delete addressed; Kind its trash kind.
	ID   string
	Kind models.TrashKind
	// Name and Version are those of the row the id addressed.
	Name    string
	Version string
	// Rows is the number of rows removed.
	Rows int
	// Trashed reports that the rows went to the trash and not away for good.
	Trashed bool
}

// DeletePackage deletes the package row with the given id (an id of the
// package listing, GET /repositories/{name}/packages) of a repository of any
// format the cleanup supports, the way a cleanup policy does: into the trash
// when the repository keeps one, else for good with its blobs and quota, and
// with the format's generated metadata refreshed afterwards. guard runs on the
// addressed row before anything changes (the immutable-release check, which
// needs the request); its error is returned as is. Unlike a policy run, it
// applies no release-bundle or quarantine protection: an administrator chose
// this row.
func (w *CleanupWorker) DeletePackage(ctx context.Context, repo *models.Repository, id string, guard func(DeletedPackage) error) (*DeletedPackage, error) {
	t, ok := cleanupFormatTable(repo.Format)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errUnsupportedCleanupFormat, repo.Format)
	}

	if uuid.Validate(id) != nil {
		return nil, ErrPackageNotFound
	}

	rows, err := w.loadUnit(ctx, repo.ID, t, id)
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, ErrPackageNotFound
	}

	res := &DeletedPackage{ID: id, Kind: t.kind, Name: rows[0].name, Version: rows[0].version, Rows: len(rows)}

	if guard != nil {
		if err := guard(*res); err != nil {
			return nil, err
		}
	}

	facts, err := w.loadBlobFacts(ctx, rows)
	if err != nil {
		return nil, fmt.Errorf("failed to look up blobs: %w", err)
	}

	run := newCleanupRun()

	outcome, err := w.moveToTrash(ctx, repo.ID, t, rows)

	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		return nil, ErrPackageNotFound
	case err != nil:
		return nil, fmt.Errorf("move to trash: %w", err)
	case outcome == trashMoved:
		res.Trashed = true

		run.touch(rows)
	default:
		if w.hardDelete(ctx, t, rows, facts, run) == 0 {
			return nil, ErrPackageNotFound
		}

		if repo.Format == "npm" {
			w.dropNPMDistTags(ctx, repo.ID, rows)
		}

		w.releaseQuota(ctx, repo.ID, run.released)
	}

	w.refreshMetadata(ctx, repo)
	w.refreshFormatMetadata(ctx, repo, run)

	return res, nil
}

// loadUnit reads the rows an explicit delete of id removes.
func (w *CleanupWorker) loadUnit(ctx context.Context, repoID string, t formatTable, id string) ([]cleanupCandidate, error) {
	blobSelect, _ := t.blobSQL()

	query := fmt.Sprintf(`SELECT id::text, %s, %s%s FROM %s src WHERE repository_id = $1::uuid AND deleted_at IS NULL AND id = $2::uuid`,
		t.nameCol, t.versionColumn(), blobSelect, t.sourceExpr())

	rows, err := w.db.GetDB().QueryContext(ctx, query, repoID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to look up package: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []cleanupCandidate

	for rows.Next() {
		c := cleanupCandidate{blobIDs: make([]string, len(t.blobCols))}
		dest := []any{&c.id, &c.name, &c.version}

		for i := range c.blobIDs {
			dest = append(dest, &c.blobIDs[i])
		}

		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("failed to scan package row: %w", err)
		}

		out = append(out, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating package rows: %w", err)
	}

	return out, nil
}

// dropNPMDistTags removes the dist-tags of versions that were just deleted for
// good (the rows are gone, so the tags are matched by the package and version
// the removed ids had): a tag must not point at a version that no longer
// exists. A live row with the same version, re-published meanwhile, keeps its
// tags.
func (w *CleanupWorker) dropNPMDistTags(ctx context.Context, repoID string, rows []cleanupCandidate) {
	for _, c := range rows {
		_, err := w.db.GetDB().ExecContext(ctx,
			`DELETE FROM npm_dist_tags d USING npm_packages p
			 WHERE p.repository_id = $3::uuid AND p.full_name = $1 AND d.package_id = p.id AND d.version = $2
			   AND NOT EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = p.id AND v.version = $2 AND v.deleted_at IS NULL)`,
			c.name, c.version, repoID)
		if err != nil {
			w.log.Warnw("failed to drop dist-tags of a deleted npm version", "package", c.name, "version", c.version, "error", err)
		}
	}
}
