// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"context"
	"crypto/sha1" //nolint:gosec // Composer's dist "shasum" is a SHA-1; the blob identity is the SHA-256
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/composer"
)

// maxPublishBytes bounds one uploaded dist zip.
const maxPublishBytes = 500 << 20

// handlePublish stores the dist zip in the request body as one version of the
// package in the URL. The zip's composer.json must name the same package, and
// its "version", when it has one, must be the same version.
func (h *HTTPHandler) handlePublish(w http.ResponseWriter, r *http.Request, repo *models.Repository, rt composer.Path) {
	normalized, err := composer.NormalizePublished(rt.Version)
	if err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"Invalid version "+rt.Version+": it must be a Composer version such as 1.2.3, 1.0.0-RC1, dev-main or 1.x-dev.",
		)
		return
	}

	sha1h := sha1.New() //nolint:gosec // see the import
	r.Body = http.MaxBytesReader(w, r.Body, maxPublishBytes)

	part, err := base.SpoolLimited(r.Body, maxPublishBytes, sha1h)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Package too large.")
			return
		}

		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "Package too large.")
			return
		}

		h.Log.Warnw("failed to read uploaded composer package", "error", err)
		writeError(w, http.StatusBadRequest, "Cannot read the request body.")

		return
	}
	defer part.Close()

	man, msg := checkArchive(part, rt, normalized)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repo.ID, man.Licenses...) {
		return
	}

	existing, err := h.DB.GetComposerPackage(r.Context(), repo.ID, rt.Name, normalized)
	if err != nil {
		h.Log.Errorw("failed to look up composer package", "package", rt.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	if existing != nil {
		writeConflict(w, rt)
		return
	}

	// A branch is always "dev-<branch>" in the metadata, however it was spelled
	// ("master" is the branch dev-master).
	version := rt.Version
	if strings.HasPrefix(normalized, "dev-") {
		version = normalized
	}

	row := &models.ComposerPackage{
		RepositoryID:      repo.ID,
		Name:              rt.Name,
		Version:           version,
		VersionNormalized: normalized,
		SizeBytes:         part.Size(),
		SHA256:            part.SHA256Hex(),
		SHA1:              hex.EncodeToString(sha1h.Sum(nil)),
		ComposerJSON:      man.JSON,
	}

	if !h.store(w, r, repo, row, part) {
		return
	}

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, artifact(repo, row)))

	body, err := json.Marshal(map[string]any{
		"status":  "ok",
		"id":      row.ID,
		"name":    row.Name,
		"version": row.Version,
		"sha1":    row.SHA1,
		"sha256":  row.SHA256,
		"size":    row.SizeBytes,
		"url":     h.distURL(repo.Name, row),
	})
	if err != nil {
		w.WriteHeader(http.StatusCreated)
		return
	}

	base.WriteJSON(w, http.StatusCreated, body)
}

// artifact is the webhook description of a stored version.
func artifact(repo *models.Repository, p *models.ComposerPackage) webhook.Artifact {
	return webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       p.Name,
		Version:    p.Version,
		Path:       distFileName(p),
		SHA256:     p.SHA256,
		Size:       p.SizeBytes,
	}
}

// checkArchive reads and validates the zip. The returned message is empty when
// the upload is acceptable.
func checkArchive(part *base.Spool, rt composer.Path, normalized string) (*composer.Manifest, string) {
	man, err := composer.ReadArchive(part.Reader(), part.Size())
	if err != nil {
		if errors.Is(err, composer.ErrInvalidArchive) {
			return nil, err.Error()
		}

		return nil, "Cannot read the package: " + err.Error()
	}

	if !strings.EqualFold(man.Name, rt.Name) {
		return nil, fmt.Sprintf("The package name %q in composer.json must be %q, the one in the URL.", man.Name, rt.Name)
	}

	if man.Version != "" {
		declared, err := composer.Normalize(man.Version)
		if err != nil || declared != normalized {
			return nil, fmt.Sprintf("The version %q in composer.json is not the version %q in the URL.", man.Version, rt.Version)
		}
	}

	return man, ""
}

func writeConflict(w http.ResponseWriter, rt composer.Path) {
	writeError(w, http.StatusConflict, "Version already exists: "+rt.Name+" "+rt.Version+" is already published.")
}

// store reserves the quota and persists the dist and its row. On failure it
// writes the response and returns false.
func (h *HTTPHandler) store(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	row *models.ComposerPackage,
	part *base.Spool,
) bool {
	if err := quota.Enforce(r.Context(), h.DB, repo.ID, row.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return false
		}

		h.Log.Errorw("failed to reserve quota", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return false
	}

	err := h.commit(r.Context(), row, part)
	if err == nil {
		return true
	}

	if relErr := quota.Release(r.Context(), h.DB, repo.ID, row.SizeBytes); relErr != nil {
		h.Log.Warnw("failed to release quota", "repo", repo.Name, "error", relErr)
	}

	if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
		writeConflict(w, composer.Path{Name: row.Name, Version: row.Version})
		return false
	}

	h.Log.Errorw("failed to store composer package", "package", row.Name, "version", row.Version, "error", err)
	writeError(w, http.StatusInternalServerError, "Internal error.")

	return false
}

// commit stores the blob first, then inserts the referencing row under the
// per-blob lock so a concurrent delete cannot remove the blob in between. When
// no row results the blob is dropped again unless something else references it.
func (h *HTTPHandler) commit(ctx context.Context, row *models.ComposerPackage, part *base.Spool) (err error) {
	defer func() {
		if err != nil {
			h.dropBlob(ctx, row.SHA256, "publish")
		}
	}()

	blob := storage.SeekerBlob(row.SHA256, part.Reader(), map[string]string{"sha256": row.SHA256})

	return storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		row.BlobKey = blobKey
		row.CreatedAt = importmeta.CreatedAt(ctx)

		return h.DB.CreateComposerPackage(ctx, row) //nolint:wrapcheck // the caller reports the failure
	})
}

// dropBlob removes the content-addressed blob once no row references it.
func (h *HTTPHandler) dropBlob(ctx context.Context, sha256, after string) {
	if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, sha256); err != nil {
		h.Log.Warnw("failed to clean up composer blob", "after", after, "sha256", sha256, "error", err)
	}
}

// handleDelete moves one version to the trash.
func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request, repo *models.Repository, rt composer.Path) {
	normalized, err := composer.NormalizePublished(rt.Version)
	if err != nil {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}

	p, err := h.DB.GetComposerPackage(r.Context(), repo.ID, rt.Name, normalized)
	if err != nil {
		h.Log.Errorw("failed to get composer package", "package", rt.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	if p == nil {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repo.Name, FormatName, p.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			writeError(w, http.StatusConflict, "Repository has immutable releases.")
			return
		}

		h.Log.Errorw("failed to check release immutability", "package", p.Name, "error", gErr)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindComposer,
		IDs:      []string{p.ID},
		Resource: distFileName(p),
		Package:  p.Name,
		Version:  p.Version,
	}, func() error { return h.hardDelete(r.Context(), repo, p) })
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			writeError(w, http.StatusNotFound, "Not found.")
			return
		}

		h.Log.Errorw("failed to delete composer package", "id", p.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, artifact(repo, p)))

	w.WriteHeader(http.StatusNoContent)
}

// hardDelete is the permanent delete: the row, the quota it held and, once no
// row references it, the content-addressed blob.
func (h *HTTPHandler) hardDelete(ctx context.Context, repo *models.Repository, p *models.ComposerPackage) error {
	return h.DeleteAccounted(ctx, repo.ID, p.SHA256, p.SizeBytes, func(ctx context.Context) error {
		return h.DB.DeleteComposerPackage(ctx, repo.ID, p.ID) //nolint:wrapcheck // wrapped by DeleteAccounted
	})
}
