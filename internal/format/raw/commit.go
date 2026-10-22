// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package raw

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
)

// ifAbsentInserter is implemented by databases that can insert a raw artifact
// only when its path is free, atomically.
type ifAbsentInserter interface {
	InsertRawArtifactIfAbsent(ctx context.Context, artifact *models.RawArtifact) (bool, error)
}

// Commit records a raw artifact: it runs putBlob (which must make the blob
// a.SHA256 present in the blob store and set a.BlobKey) and upserts the
// artifact row under the per-blob lock, so a concurrent unpublish cannot delete
// the blob in between; then it releases the blob an overwrite left
// unreferenced and publishes artifact.uploaded. It returns the artifact row
// that was overwritten, or nil for a new path. A.ID is the new row's id; an
// overwrite keeps the previous row's id (see StoredID).
//
// It is the single write path shared by the streaming PUT and the presigned
// upload completion, so both behave identically after the bytes are in place,
// and both enforce immutable_releases: overwriting an existing path of such a
// repository fails with trash.ErrImmutableRelease, before putBlob runs, unless
// req carries an admin's ?force=true. req supplies the principal and the force
// flag; its context is used for every operation, so a caller that must outlive
// the HTTP request passes a request carrying a detached context.
func Commit(req *http.Request, h *base.Handler, repoName string, a *models.RawArtifact, putBlob func(ctx context.Context) error) (*models.RawArtifact, error) {
	ctx := req.Context()

	previous, err := h.DB.GetRawArtifact(ctx, a.RepositoryID, a.Path)
	if err != nil {
		return nil, fmt.Errorf("look up existing artifact: %w", err)
	}

	// Every raw file is a release: an overwrite of an immutable repository is
	// refused. The check below only fails fast; two concurrent first PUTs both
	// see no previous row, so the write itself must be conditional.
	guardErr := trash.GuardReleaseByName(req, h.DB, repoName, "raw", "")
	if guardErr != nil && !errors.Is(guardErr, trash.ErrImmutableRelease) {
		return nil, guardErr
	}

	if previous != nil && guardErr != nil {
		return nil, guardErr
	}

	inserter, atomic := h.DB.(ifAbsentInserter)
	atomic = atomic && guardErr != nil

	if err := storage.WithBlobLock(ctx, h.DB, a.SHA256, func(ctx context.Context) error {
		if err := putBlob(ctx); err != nil {
			return err
		}

		if atomic {
			inserted, err := inserter.InsertRawArtifactIfAbsent(ctx, a)
			if err != nil {
				return fmt.Errorf("insert artifact: %w", err)
			}

			if !inserted {
				return trash.ErrImmutableRelease
			}

			return nil
		}

		if err := h.DB.UpsertRawArtifact(ctx, a); err != nil {
			return fmt.Errorf("upsert artifact: %w", err)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// An overwrite leaves the previous content unreferenced.
	if previous != nil && previous.SHA256 != "" && previous.SHA256 != a.SHA256 {
		if delErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, previous.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up replaced blob", "sha256", previous.SHA256, "error", delErr)
		}
	}

	h.Emit(ctx, a.RepositoryID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "raw",
		Name:       a.Path,
		Path:       a.Path,
		SHA256:     a.SHA256,
		Size:       a.SizeBytes,
	}))

	return previous, nil
}

// StoredID is the id of the row an upload ended up in: an overwrite keeps the
// existing row (and its id).
func StoredID(a, previous *models.RawArtifact) string {
	if previous != nil {
		return previous.ID
	}

	return a.ID
}

// MaxUploadBytes bounds a single raw upload.
const MaxUploadBytes = maxRawUploadBytes
