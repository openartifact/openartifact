// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"context"
	"time"

	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/storage"
)

// CommitBlobRow stores the spool as the content-addressed blob sha256 (with its
// filename in the object metadata), then runs create, which inserts the row
// that references the blob, under the per-blob lock so a concurrent delete
// cannot remove the blob in between. Before create runs, *blobKey is set to the
// blob's storage key and *createdAt to the import-aware creation time: they are
// the row's BlobKey and CreatedAt fields. When create fails the blob is dropped
// again unless something else references it (a failed cleanup is logged under
// the handler's format). The error of create is returned as is.
func (h *Handler) CommitBlobRow(
	ctx context.Context,
	spool *Spool,
	sha256, filename string,
	blobKey *string,
	createdAt *time.Time,
	create func(ctx context.Context) error,
) (err error) {
	defer func() {
		if err != nil {
			if derr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, sha256); derr != nil {
				h.Log.Warnw("failed to clean up "+h.Format+" blob", "after", "publish", "sha256", sha256, "error", derr)
			}
		}
	}()

	blob := storage.SeekerBlob(sha256, spool.Reader(), map[string]string{"sha256": sha256, "filename": filename})

	return storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, key string) error {
		*blobKey = key
		*createdAt = importmeta.CreatedAt(ctx)

		return create(ctx)
	})
}
