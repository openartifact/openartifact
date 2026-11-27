// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/webhook"
	pkgpub "github.com/openartifact/openartifact/pkg/pub"
)

// formOverhead is what the multipart envelope may add to the archive.
const formOverhead = 1 << 20

// handleNew answers the first step of "dart pub publish": where to upload the
// archive to and which form fields to send with it. The upload goes to this
// repository, so no fields are needed.
func (h *HTTPHandler) handleNew(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	body, err := json.Marshal(map[string]any{
		"url":    repoRoot(h.PublicBase(w, r), repo.Name) + "/" + pkgpub.UploadPath,
		"fields": map[string]string{},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")
		return
	}

	w.Header().Set("Content-Type", pkgpub.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// readUpload parses the multipart form of the archive upload: the archive is the
// "file" part. On failure it writes the response and returns nil; the caller
// closes the result.
func (h *HTTPHandler) readUpload(w http.ResponseWriter, r *http.Request) *base.Spool {
	r.Body = http.MaxBytesReader(w, r.Body, pkgpub.MaxArchiveBytes+formOverhead)

	form, err := base.ReadForm(r, base.FormSpec{FileField: "file", MaxFile: pkgpub.MaxArchiveBytes, MaxFields: formOverhead})
	if err != nil {
		switch {
		case base.IsTooLarge(err):
			writeError(w, http.StatusRequestEntityTooLarge, "RequestTooLarge", "The package archive is too large.")
		case errors.Is(err, base.ErrNoFormFile):
			writeError(w, http.StatusBadRequest, "InvalidInput", "The upload has no file part.")
		default:
			writeError(w, http.StatusBadRequest, "InvalidInput", "The upload is not a valid multipart form.")
		}

		return nil
	}

	return form.File
}

func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	part := h.readUpload(w, r)
	if part == nil {
		return
	}
	defer part.Close()

	pkg, err := pkgpub.ReadArchive(part.Reader())
	if err != nil {
		if errors.Is(err, pkgpub.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "PackageRejected", err.Error())
			return
		}

		writeError(w, http.StatusBadRequest, "PackageRejected", "Cannot read the package archive: "+err.Error())

		return
	}

	existing, err := h.DB.GetPubPackage(r.Context(), repo.ID, pkg.Name, pkg.Version)
	if err != nil {
		h.Log.Errorw("failed to look up pub package", "package", pkg.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return
	}

	if existing != nil {
		writeConflict(w, pkg)
		return
	}

	row := &models.PubPackage{
		RepositoryID: repo.ID,
		Name:         pkg.Name,
		Version:      pkg.Version,
		Filename:     pkgpub.ArchiveName(pkg.Name, pkg.Version),
		SizeBytes:    part.Size(),
		SHA256:       part.SHA256Hex(),
		Pubspec:      pkg.Pubspec,
	}

	if !h.store(w, r, repo, row, part) {
		return
	}

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       row.Name,
		Version:    row.Version,
		Path:       row.Filename,
		SHA256:     row.SHA256,
		Size:       row.SizeBytes,
	}))

	finish := url.Values{"package": {row.Name}, "version": {row.Version}}

	w.Header().Set("Location", repoRoot(h.PublicBase(w, r), repo.Name)+"/"+pkgpub.FinishPath+"?"+finish.Encode())
	w.WriteHeader(http.StatusNoContent)
}

// handleFinish answers the last step of "dart pub publish": the outcome of the
// upload the client has just made, which the upload step already stored.
func (h *HTTPHandler) handleFinish(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, version := r.URL.Query().Get("package"), r.URL.Query().Get("version")

	if h.lookup(w, r, repo, name, version) == nil {
		return
	}

	body, err := json.Marshal(map[string]any{"success": map[string]string{
		"message": "Successfully uploaded " + name + " version " + version + ".",
	}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")
		return
	}

	w.Header().Set("Content-Type", pkgpub.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeConflict(w http.ResponseWriter, pkg *pkgpub.Package) {
	writeError(w, http.StatusConflict, "PackageRejected",
		"Version "+pkg.Version+" of package "+strconv.Quote(pkg.Name)+" is already published.")
}

// store reserves the quota and persists the archive and its row. On failure it
// writes the response and returns false.
func (h *HTTPHandler) store(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	row *models.PubPackage,
	part *base.Spool,
) bool {
	if err := quota.Enforce(r.Context(), h.DB, repo.ID, row.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			writeError(w, http.StatusInsufficientStorage, "RequestRejected", quota.Message)
			return false
		}

		h.Log.Errorw("failed to reserve quota", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return false
	}

	err := h.commit(r.Context(), row, part)
	if err == nil {
		return true
	}

	h.ReleaseQuota(r.Context(), repo.ID, repo.Name, row.SizeBytes)

	if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
		writeConflict(w, &pkgpub.Package{Name: row.Name, Version: row.Version})
		return false
	}

	h.Log.Errorw("failed to store pub package", "filename", row.Filename, "error", err)
	writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

	return false
}

// commit stores the blob and inserts the row that references it; see
// base.Handler.CommitBlobRow.
func (h *HTTPHandler) commit(ctx context.Context, row *models.PubPackage, part *base.Spool) error {
	return h.CommitBlobRow(ctx, part, row.SHA256, row.Filename, &row.BlobKey, &row.CreatedAt, func(ctx context.Context) error {
		return h.DB.CreatePubPackage(ctx, row) //nolint:wrapcheck // the caller reports the failure
	})
}
