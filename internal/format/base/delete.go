// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
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

	if err := quota.Release(ctx, h.DB, repoID, size); err != nil {
		h.Log.Warnw("failed to release quota", "repo", repoID, "error", err)
	}

	return nil
}
