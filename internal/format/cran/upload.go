// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"context"
	"crypto/md5" //nolint:gosec // the MD5sum PACKAGES field is MD5 by definition
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/cran"
)

// maxUploadBytes bounds one package file. Binary packages of large packages
// (bundled data, compiled libraries) are the biggest on CRAN, in the hundreds
// of MiB.
const maxUploadBytes = 1 << 30

// packageFilePath returns the parsed path of an upload or delete request when
// it names one package file, and the status and message to answer otherwise.
func packageFilePath(rel string) (cran.Path, int, string) {
	p, ok := cran.ParsePath(rel)

	switch {
	case !ok || p.Kind == cran.KindOther:
		return p, http.StatusNotFound, "not found"
	case p.Kind == cran.KindIndex:
		return p, http.StatusMethodNotAllowed, "PACKAGES indexes are generated; upload the package file"
	case p.Kind != cran.KindPackage && p.Kind != cran.KindArchivePackage:
		return p, http.StatusMethodNotAllowed, "only package files can be written"
	}

	return p, 0, ""
}

// handlePut stores a package file at its repository path:
// src/contrib/<pkg>_<ver>.tar.gz for a source package, or the file of a
// binary tree (bin/windows/contrib/<R>/<pkg>_<ver>.zip,
// bin/macosx/.../contrib/<R>/<pkg>_<ver>.tgz). The package name and version
// are the DESCRIPTION's and must match the file name; the index is derived from
// the stored files, so there is nothing else to upload.
func (h *HTTPHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	p, status, msg := packageFilePath(vars["*"])
	if status != 0 {
		http.Error(w, msg, status)
		return
	}

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	// A writer on a full repository must not get to stage the body.
	if err := quota.Precheck(r.Context(), h.DB, repoID, r.ContentLength); err != nil {
		quota.WriteExceeded(w)
		return
	}

	existing, err := h.DB.GetCRANFile(r.Context(), repoID, p.Canonical())
	if err != nil {
		h.Log.Errorw("failed to check existing cran file", "path", p.Canonical(), "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		http.Error(w, "package file already exists", http.StatusConflict)
		return
	}

	defer func() { _ = r.Body.Close() }()

	sum := md5.New() //nolint:gosec // the MD5sum PACKAGES field is MD5 by definition

	spool, err := base.SpoolLimited(r.Body, maxUploadBytes, sum)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "package too large", http.StatusRequestEntityTooLarge)
			return
		}

		h.Log.Errorw("failed to read cran upload", "path", p.Canonical(), "error", err)
		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}
	defer spool.Close()

	dir, desc, err := cran.ReadDescription(spool, spool.Size(), p.Tree.Ext)
	if err != nil {
		http.Error(w, "invalid R package: "+err.Error(), http.StatusBadRequest)
		return
	}

	if msg := checkIdentity(p, dir, desc); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, cran.Licenses(desc.Get("License"))...) {
		return
	}

	file := &models.CRANFile{
		RepositoryID: repoID,
		Package:      p.Package,
		Version:      p.Version,
		Path:         p.Canonical(),
		Filename:     p.File,
		Tree:         p.Tree.Dir,
		Fields:       cran.StoredFields(desc, p.Tree.Binary),
		MD5:          hex.EncodeToString(sum.Sum(nil)),
		Title:        cran.Fold(desc.Get("Title")),
		Description:  cran.Fold(desc.Get("Description")),
		License:      cran.Fold(desc.Get("License")),
		Maintainer:   cran.Fold(desc.Get("Maintainer")),
		URL:          cran.Fold(desc.Get("URL")),
		Digest:       spool.SHA256Hex(),
		SizeBytes:    spool.Size(),
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if !h.ReserveQuotaOrRespond(r.Context(), w, repoID, repoName, file.SizeBytes) {
		return
	}

	if status, msg := h.storeFile(r.Context(), file, spool, !p.Tree.Binary); status != 0 {
		h.ReleaseQuota(r.Context(), repoID, repoName, file.SizeBytes)

		http.Error(w, msg, status)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "cran",
		Name:       file.Package,
		Version:    file.Version,
		Path:       file.Path,
		SHA256:     file.Digest,
		Size:       file.SizeBytes,
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":      file.ID,
		"package": file.Package,
		"version": file.Version,
		"path":    file.Path,
		"md5":     file.MD5,
		"sha256":  file.Digest,
		"size":    file.SizeBytes,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// checkIdentity compares what the path claims with what the archive says: the
// DESCRIPTION's Package and Version must be the file name's, and the archive's
// top-level directory the package's name (R's installer unpacks it by name).
// It returns the message to answer 400 with, or "".
func checkIdentity(p cran.Path, dir string, desc cran.Record) string {
	if got := desc.Get("Package"); got != p.Package {
		return fmt.Sprintf("DESCRIPTION Package %q does not match the file name %q", got, p.Package)
	}

	if got := desc.Get("Version"); got != p.Version {
		return fmt.Sprintf("DESCRIPTION Version %q does not match the file name %q", got, p.Version)
	}

	if dir != p.Package {
		return fmt.Sprintf("archive directory %q is not the package name %q", dir, p.Package)
	}

	return ""
}

// storeFile commits the blob and inserts the row, returning a non-zero status
// and message when the file was not stored.
func (h *HTTPHandler) storeFile(ctx context.Context, file *models.CRANFile, body *base.Spool, replaceMeta bool) (int, string) {
	status, msg := 0, ""

	blob := storage.SeekerBlob(file.Digest, body.Reader(), map[string]string{
		"sha256":   file.Digest,
		"filename": file.Filename,
		"package":  file.Package,
		"version":  file.Version,
	})

	lockErr := storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		file.BlobKey = blobKey
		file.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreateCRANFile(ctx, file, replaceMeta); err != nil {
			if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
				status, msg = http.StatusConflict, "package file already exists"

				return err
			}

			h.Log.Errorw("failed to create cran file record", "path", file.Path, "error", err)

			status, msg = http.StatusInternalServerError, "internal error"

			return err
		}

		return nil
	})
	if lockErr == nil {
		return 0, ""
	}

	if status == 0 {
		storeErr, isStore := errors.AsType[*storage.StoreError](lockErr)

		switch {
		case isStore && storeErr.Op == "check":
			h.Log.Errorw("failed to check cran blob", "error", lockErr)
		case isStore:
			h.Log.Errorw("failed to store cran blob", "error", lockErr)
		default:
			h.Log.Errorw("cran blob lock failed", "path", file.Path, "error", lockErr)
		}

		status, msg = http.StatusInternalServerError, "internal error"
	}

	// The row was not created: drop the blob if nothing else references it.
	if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, file.Digest); err != nil {
		h.Log.Warnw("failed to clean up cran blob", "sha256", file.Digest, "error", err)
	}

	return status, msg
}

// handleDelete deletes one package file (to the trash, like every other
// format). The file leaves its tree's PACKAGES; a package whose last file goes
// leaves the repository.
func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	p, status, msg := packageFilePath(vars["*"])
	if status != 0 {
		http.Error(w, msg, status)
		return
	}

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	file, err := h.DB.GetCRANFile(r.Context(), repoID, p.Canonical())
	if err != nil {
		h.Log.Errorw("failed to get cran file", "path", p.Canonical(), "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if file == nil {
		http.NotFound(w, r)
		return
	}

	// Resolve the size to release before the blob can go away.
	size := file.SizeBytes
	if size <= 0 {
		if n, sizeErr := h.BlobStore.GetSize(r.Context(), file.BlobKey); sizeErr == nil {
			size = n
		}
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repoName, "cran", file.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			http.Error(w, "repository has immutable releases", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "package", file.Package, "error", gErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repoID,
		Kind:     models.TrashKindCRAN,
		IDs:      []string{file.ID},
		Resource: file.Path,
		Package:  file.Package,
		Version:  file.Version,
	}, func() error {
		// Delete the record first, then the blob only if nothing else references it.
		if delErr := h.DB.DeleteCRANFile(r.Context(), repoID, file.ID); delErr != nil {
			return fmt.Errorf("delete cran file record: %w", delErr)
		}

		h.ReleaseQuota(r.Context(), repoID, repoName, size)

		if blobErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, file.Digest); blobErr != nil {
			h.Log.Warnw("failed to delete cran blob", "sha256", file.Digest, "error", blobErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete cran file", "path", file.Path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "cran",
		Name:       file.Package,
		Version:    file.Version,
		Path:       file.Path,
		SHA256:     file.Digest,
		Size:       size,
	}))

	w.WriteHeader(http.StatusNoContent)
}
