// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"context"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
)

// DeleteAccounted is the permanent delete of a row that owns a
// content-addressed blob: deleteRow removes the row, then the quota the row held
// is given back and the blob is dropped once no row references it any more.
// Failing to give the quota back or to drop the blob is logged, not returned:
// the row is gone either way.
func (h *Handler) DeleteAccounted(
	ctx context.Context,
	repoID, sha256 string,
	size int64,
	deleteRow func(context.Context) error,
) error {
	if err := deleteRow(ctx); err != nil {
		return fmt.Errorf("delete row: %w", err)
	}

	defer func() {
		if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, sha256); err != nil {
			h.Log.Warnw("failed to clean up blob after delete", "sha256", sha256, "error", err)
		}
	}()

	h.ReleaseQuota(ctx, repoID, repoID, size)

	return nil
}

// DeleteRelease deletes the row id of kind in repo, the release art describes
// (its Name, Version, Path, SHA256 and Size; Repository and Format are filled
// in here): into the trash when the repository's retention allows, through
// hard otherwise (see trash.Delete). On success it emits artifact.deleted. found
// is false when the row is no longer live in the repository (answer 404); err
// is the failure of the delete otherwise.
func (h *Handler) DeleteRelease(
	ctx context.Context,
	repo *models.Repository,
	kind models.TrashKind,
	id string,
	art webhook.Artifact,
	hard func() error,
) (bool, error) {
	err := trash.Delete(ctx, h.DB, trash.Request{
		RepoID:   repo.ID,
		Kind:     kind,
		IDs:      []string{id},
		Resource: art.Path,
		Package:  art.Name,
		Version:  art.Version,
	}, hard)
	if errors.Is(err, database.ErrTrashNotFound) {
		return false, nil
	}

	if err != nil {
		return true, err //nolint:wrapcheck // the caller reports the failure
	}

	art.Repository, art.Format = repo.Name, h.Format
	h.Emit(ctx, repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, art))

	return true, nil
}
