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
// this row. actor is the username recorded as the trash entry's deleted_by (the
// requester, never the cleanup policy).
func (w *CleanupWorker) DeletePackage(
	ctx context.Context,
	repo *models.Repository,
	id string,
	guard func(DeletedPackage) error,
	actor string,
) (*DeletedPackage, error) {
	t, ok := cleanupTableFor(repo.Format)
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

	run := newCleanupRun()

	outcome, err := w.moveToTrash(ctx, repo.ID, t, rows, actor)

	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		return nil, ErrPackageNotFound
	case err != nil:
		return nil, fmt.Errorf("move to trash: %w", err)
	case outcome == trashMoved:
		res.Trashed = true

		run.touch(rows)
	default:
		if w.hardDelete(ctx, repo.ID, t, rows, run) == 0 {
			return nil, ErrPackageNotFound
		}

		w.releaseQuota(ctx, repo.ID, run.released)
	}

	w.refreshFormatMetadata(ctx, repo, run)

	return res, nil
}

// loadUnit reads the candidate an explicit delete of id removes: the live
// listed file (or docker tag) with that id, as a one-element slice. A row of
// another repository or one that is not live yields none.
func (w *CleanupWorker) loadUnit(ctx context.Context, repoID string, t cleanupTable, id string) ([]cleanupCandidate, error) {
	c := t.sql()

	// The same select-list as the candidate query, restricted to the one row.
	query := fmt.Sprintf(`SELECT %s, %s, %s, %s, %s, %s %s AND %s = $2::uuid`,
		c.id, c.name, c.version, c.pkg, c.blobs, c.artifacts, c.from, t.idColumn())

	rows, err := w.db.GetDB().QueryContext(ctx, query, repoID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to look up package: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []cleanupCandidate

	for rows.Next() {
		cand, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, cand)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating package rows: %w", err)
	}

	return out, nil
}
