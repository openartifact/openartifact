// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gitlfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/gitlfs"
)

// handlePut stores one object (the upload action of the basic transfer). The
// body must hash to the oid of the URL and, when the client declared one, have
// the declared length: the object is content addressed, so a body that is not
// the object is refused and nothing is stored. Uploading an object the
// repository already holds is a no-op that answers 200.
func (h *HTTPHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName, oid := vars["name"], vars["oid"]

	if !gitlfs.ValidOID(oid) {
		writeError(w, http.StatusUnprocessableEntity, "invalid object id: want a lower-case hex sha256")
		return
	}

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	existing, err := h.lookupOne(r.Context(), repoID, oid)
	if err != nil {
		h.Log.Errorw("failed to look up git lfs object", "oid", oid, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	if existing != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)) // let a small body finish; the connection closes otherwise

		w.WriteHeader(http.StatusOK)

		return
	}

	// A writer on a full repository must not get to stage the body.
	if err := quota.Precheck(r.Context(), h.DB, repoID, r.ContentLength); err != nil {
		quota.WriteExceeded(w)
		return
	}

	defer func() { _ = r.Body.Close() }()

	spool, err := base.SpoolLimited(r.Body, maxObjectBytes)
	if err != nil {
		switch {
		case errors.Is(err, base.ErrContentTooLarge) || isTooLarge(err):
			writeError(w, http.StatusRequestEntityTooLarge, "object too large")
		default:
			h.Log.Errorw("failed to read git lfs upload", "oid", oid, "error", err)
			writeError(w, http.StatusBadRequest, "failed to read the upload")
		}

		return
	}

	defer spool.Close()

	if spool.SHA256Hex() != oid {
		writeError(w, http.StatusUnprocessableEntity, "the body does not hash to the object id")
		return
	}

	if r.ContentLength >= 0 && r.ContentLength != spool.Size() {
		writeError(
			w,
			http.StatusUnprocessableEntity,
			fmt.Sprintf("the body has %d bytes, not the declared %d", spool.Size(), r.ContentLength),
		)
		return
	}

	if err := h.commit(r, repoID, repoName, oid, spool); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to store git lfs object", "oid", oid, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	w.WriteHeader(http.StatusOK)
}

// commit stores the object's blob and row. Quota is reserved before anything is
// stored and returned on every failure; an object stored by a concurrent upload
// of the same content is success, with this upload's reservation returned.
func (h *HTTPHandler) commit(r *http.Request, repoID, repoName, oid string, spool *base.Spool) error {
	ctx := r.Context()
	size := spool.Size()

	if err := quota.Enforce(ctx, h.DB, repoID, size); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			return err //nolint:wrapcheck // the sentinel the caller answers 507
		}

		return fmt.Errorf("reserve quota: %w", err)
	}

	obj := &models.GitLFSObject{RepositoryID: repoID, OID: oid, SizeBytes: size, CreatedAt: importmeta.CreatedAt(ctx)}

	err := storage.CommitBlob(ctx, h.DB, h.BlobStore, storage.ReaderBlob(oid, map[string]string{
		"sha256": oid,
		"format": "gitlfs",
	}, func() io.Reader { return spool.Reader() }), func(ctx context.Context, key string) error {
		obj.BlobKey = key

		return h.DB.PutGitLFSObject(ctx, obj)
	})
	if err != nil {
		h.ReleaseQuota(ctx, repoID, repoName, size)

		if errors.Is(err, database.ErrDuplicate) {
			return nil
		}

		return fmt.Errorf("commit object: %w", err)
	}

	h.Emit(ctx, repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "gitlfs",
		Name:       oid,
		Path:       gitlfs.ObjectPath(oid),
		SHA256:     oid,
		Size:       size,
	}))

	return nil
}

// handleGet serves an object (the download action, and HEAD): with Range,
// If-None-Match and, for large objects of a repository with redirects enabled,
// a 307 to the object store.
func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName, oid := vars["name"], vars["oid"]

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	var obj *models.GitLFSObject

	if gitlfs.ValidOID(oid) {
		var err error

		if obj, err = h.lookupOne(r.Context(), repoID, oid); err != nil {
			h.Log.Errorw("failed to look up git lfs object", "oid", oid, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")

			return
		}
	}

	if obj == nil {
		writeError(w, http.StatusNotFound, "object not found")
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(obj.ID, gitlfs.ObjectPath(oid), oid)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{
		SHA256:      obj.BlobKey,
		Size:        obj.SizeBytes,
		Filename:    oid,
		ContentType: "application/octet-stream",
	}) {
		return
	}

	if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		h.DB.CountFileDownload(obj.ID)
	}

	w.Header().Set("X-Checksum-Sha256", oid)

	err := base.ServeBlob(w, r, h.BlobStore, obj.BlobKey, base.ServeBlobOptions{
		ContentType: "application/octet-stream",
		Size:        obj.SizeBytes,
		ETag:        `"` + oid + `"`,
		ModTime:     obj.UpdatedAt,
	})
	if err != nil {
		h.Log.Errorw("failed to serve git lfs object", "key", obj.BlobKey, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
