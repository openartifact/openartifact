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
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
)

// ifAbsentInserter is implemented by databases that can insert a raw artifact
// only when its path is free, atomically.
type ifAbsentInserter interface {
	InsertRawArtifactIfAbsent(ctx context.Context, artifact *models.RawArtifact) (bool, error)
}

// Commit records a raw artifact: it stores blob (the content a.SHA256, see
// storage.CommitBlob), sets a.BlobKey to its storage key and upserts the
// artifact row under the per-blob lock, so a concurrent unpublish cannot delete
// the blob in between; then it releases the blob an overwrite left
// unreferenced and publishes artifact.uploaded. It returns the artifact row
// that was overwritten, or nil for a new path. A.ID is the new row's id; an
// overwrite keeps the previous row's id (see StoredID).
//
// It is the single write path shared by the streaming PUT and the presigned
// upload completion, so both behave identically after the bytes are in place,
// and both enforce immutable_releases: overwriting an existing path of such a
// repository fails with trash.ErrImmutableRelease, before the blob is stored,
// unless req carries an admin's ?force=true. Storage quota is reserved after
// those checks and before the blob is stored: a reservation that does not fit fails with
// quota.ErrExceeded, any later failure returns the reservation, and an
// overwrite releases the replaced row's usage. req supplies the principal and the force
// flag; its context is used for every operation, so a caller that must outlive
// the HTTP request passes a request carrying a detached context.
func Commit(req *http.Request, h *base.Handler, repoName string, a *models.RawArtifact, blob storage.Blob) (*models.RawArtifact, error) {
	return commitWith(req, h, repoName, a, func(ctx context.Context, commit func(ctx context.Context, key string) error) error {
		return storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, commit)
	})
}

// CommitPromoted is Commit for content already in the blob store's staging
// area (the presigned upload completion): promote makes the blob a.SHA256
// present under the per-blob lock instead of a stored Blob.
func CommitPromoted(req *http.Request, h *base.Handler, repoName string, a *models.RawArtifact, promote func(ctx context.Context) error) (*models.RawArtifact, error) {
	return commitWith(req, h, repoName, a, func(ctx context.Context, commit func(ctx context.Context, key string) error) error {
		return storage.WithBlobLock(ctx, h.DB, a.SHA256, func(ctx context.Context) error {
			if err := promote(ctx); err != nil {
				return err
			}

			return commit(ctx, a.BlobKey)
		})
	})
}

// stageFunc makes the artifact's blob present and runs commit (the row write)
// with its storage key under the per-blob lock.
type stageFunc func(ctx context.Context, commit func(ctx context.Context, key string) error) error

func commitWith(req *http.Request, h *base.Handler, repoName string, a *models.RawArtifact, stage stageFunc) (*models.RawArtifact, error) {
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

	// Reserve quota before anything is persisted; re-committing the content a
	// path already holds changes no usage. Every failure below returns the
	// reservation. An overwrite gives the replaced row's usage back afterwards.
	reserved := previous == nil || previous.SHA256 != a.SHA256
	if reserved {
		if err := quota.Enforce(ctx, h.DB, a.RepositoryID, a.SizeBytes); err != nil {
			if errors.Is(err, quota.ErrExceeded) {
				return nil, err
			}

			return nil, fmt.Errorf("reserve quota: %w", err)
		}
	}

	var only ifAbsentInserter
	if atomic {
		only = inserter
	}

	if err := persist(ctx, h, a, stage, only); err != nil {
		if reserved {
			releaseQuota(ctx, h, repoName, a.RepositoryID, a.SizeBytes)
		}

		return nil, err
	}

	if reserved && previous != nil {
		releaseQuota(ctx, h, repoName, a.RepositoryID, previous.SizeBytes)
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

// persist stores the blob and writes the artifact row under the per-blob lock
// (bytes first, the lock covering only the row write). With inserter set the row is only inserted when its path is free
// (trash.ErrImmutableRelease otherwise), else it is upserted.
func persist(ctx context.Context, h *base.Handler, a *models.RawArtifact, stage stageFunc, inserter ifAbsentInserter) error {
	return stage(ctx, func(ctx context.Context, key string) error {
		a.BlobKey = key

		if inserter != nil {
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
	})
}

// releaseQuota returns size bytes and one artifact to the repository's quota.
func releaseQuota(ctx context.Context, h *base.Handler, repoName, repoID string, size int64) {
	if err := quota.Release(ctx, h.DB, repoID, size); err != nil {
		h.Log.Warnw("failed to release quota", "repo", repoName, "error", err)
	}
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
