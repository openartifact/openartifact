// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
)

// handleListFiles lists the files of a published recipe or package revision as
// {"files": {path: {size}}}; the conan client reads only the paths.
func (h *HTTPHandler) handleListFiles(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	files, ok := h.recipeFiles(w, r, s)
	if !ok {
		return
	}

	revFiles := revisionFiles(files, s.rrev, s.pid, s.prev)

	// A revision exists once its manifest is stored.
	if !hasFile(revFiles, manifestFile) {
		http.NotFound(w, r)
		return
	}

	type fileInfo struct {
		Size int64 `json:"size"`
	}

	listing := make(map[string]fileInfo, len(revFiles))
	for _, f := range revFiles {
		listing[f.Filename] = fileInfo{Size: f.SizeBytes}
	}

	h.writeJSON(w, map[string]any{"files": listing})
}

// hasFile reports whether files holds filename.
func hasFile(files []*models.ConanPackage, filename string) bool {
	for _, f := range files {
		if f.Filename == filename {
			return true
		}
	}

	return false
}

// handleDownload serves one stored file (GET), or just its headers (HEAD).
func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	s, ok := h.fileScopeOf(w, r)
	if !ok {
		return
	}

	file, err := h.DB.GetConanPackage(r.Context(), s.repoID, s.key(s.filename))
	if err != nil {
		h.fail(w, "failed to get conan file", err, "path", s.resource()+"/"+s.filename)
		return
	}

	if file == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, s.repoID, quarantine.PathKeys(
		s.filename, conanPath(s.recipe.Name, s.recipe.Version, s.recipe.UserChannel, s.filename),
		s.resource()+"/"+s.filename, r.URL.Path)...) {
		return
	}

	leaf := path.Base(s.filename)

	if r.Method != http.MethodHead && h.TryPresign(w, r, presign.Blob{
		SHA256: file.BlobKey, Size: file.SizeBytes, Filename: leaf, ContentType: "application/octet-stream",
	}) {
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": leaf}))

	w.Header().Set("Content-Length", strconv.FormatInt(file.SizeBytes, 10))

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), file.BlobKey)
	if err != nil {
		h.fail(w, "failed to get conan blob", err, "key", file.BlobKey)
		return
	}
	defer base.CloseLog(blob, h.Log)

	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, blob)
}

// handleUpload stores one file of a recipe or package revision. Re-uploading
// the same bytes is a no-op; replacing a file with different bytes is refused
// in a repository with immutable releases. A request with
// `X-Checksum-Deploy: true` and no body stores a file the repository already
// holds, by its SHA-1 (see checksum.go).
func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	s, ok := h.fileScopeOf(w, r)
	if !ok {
		return
	}

	// conan uploads a package only after its recipe revision.
	if s.pid != "" {
		ok, err := h.recipePublished(r.Context(), s)
		if err != nil {
			h.fail(w, "failed to look up conan recipe revision", err, "recipe", s.resource())
			return
		}

		if !ok {
			http.Error(w, "recipe revision not found", http.StatusNotFound)
			return
		}
	}

	if isChecksumDeploy(r) {
		h.deployByChecksum(w, r, s)
		return
	}

	data, err := base.ReadAllLimited(r.Body, maxConanUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}

		h.Log.Errorw("failed to read upload", "error", err)
		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()

	sum := sha256.Sum256(data)
	md5Sum, sha1Sum := wireDigests(data)
	file := s.newFile()
	file.SHA256 = hex.EncodeToString(sum[:])
	file.MD5, file.SHA1 = md5Sum, sha1Sum
	file.SizeBytes = int64(len(data))

	h.commit(w, r, s, file, func(ctx context.Context) error { return h.store(ctx, file, data) })
}

// commit makes file the content of the scope's file name: same bytes already
// there is a no-op, different bytes replace them unless the repository has
// immutable releases, and the quota is reserved before persist runs. persist
// writes the blob and the row; it returns errChecksumMiss when a deploy finds
// its source gone, which is a 404 (the client then uploads the bytes).
func (h *HTTPHandler) commit(w http.ResponseWriter, r *http.Request, s *scope, file *models.ConanPackage, persist func(ctx context.Context) error) {
	previous, err := h.DB.GetConanPackage(r.Context(), s.repoID, s.key(s.filename))
	if err != nil {
		h.fail(w, "failed to look up conan file", err, "path", s.resource()+"/"+s.filename)
		return
	}

	if previous != nil {
		if previous.SHA256 == file.SHA256 {
			h.keepDigests(r.Context(), previous, file)
			h.writeStored(w, previous)

			return
		}

		if gErr := trash.GuardReleaseByName(r, h.DB, s.repoName, "conan", s.recipe.Version); gErr != nil {
			h.rejectImmutable(w, s, gErr)
			return
		}
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if err := quota.Enforce(r.Context(), h.DB, s.repoID, file.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.fail(w, "failed to reserve conan quota", err, "path", s.resource()+"/"+s.filename)

		return
	}

	if err := persist(r.Context()); err != nil {
		if relErr := quota.Release(r.Context(), h.DB, s.repoID, file.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", s.repoName, "error", relErr)
		}

		if errors.Is(err, errChecksumMiss) {
			http.NotFound(w, r)
			return
		}

		h.fail(w, "failed to store conan file", err, "path", s.resource()+"/"+s.filename)

		return
	}

	// The replaced row's usage is gone once the new content is in place.
	if previous != nil {
		if relErr := quota.Release(r.Context(), h.DB, s.repoID, previous.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", s.repoName, "error", relErr)
		}
	}

	// An overwrite leaves the previous content unreferenced.
	if previous != nil && previous.SHA256 != "" {
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, previous.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up replaced conan blob", "sha256", previous.SHA256, "error", delErr)
		}
	}

	h.Emit(r.Context(), s.repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: s.repoName,
		Format:     "conan",
		Name:       s.recipe.Name,
		Version:    s.recipe.Version,
		Path:       conanPath(s.recipe.Name, s.recipe.Version, s.recipe.UserChannel, s.filename),
		SHA256:     file.SHA256,
		Size:       file.SizeBytes,
	}))

	h.writeStored(w, file)
}

// store writes the blob and the referencing row under the per-blob lock, so a
// concurrent delete cannot remove the blob in between.
func (h *HTTPHandler) store(ctx context.Context, file *models.ConanPackage, data []byte) error {
	return storage.WithBlobLock(ctx, h.DB, file.SHA256, func(ctx context.Context) error {
		blobKey, err := h.BlobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"sha256":   file.SHA256,
			"filename": file.Filename,
			"package":  file.Name,
		})
		if err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		file.BlobKey = blobKey

		if err := h.DB.CreateConanPackage(ctx, file); err != nil {
			return fmt.Errorf("create conan package: %w", err)
		}

		return nil
	})
}

// recipePublished reports whether the recipe revision of s has its manifest.
func (h *HTTPHandler) recipePublished(ctx context.Context, s *scope) (bool, error) {
	manifest, err := h.DB.GetConanPackage(ctx, s.repoID, models.ConanFileKey{
		Name:           s.recipe.Name,
		Version:        s.recipe.Version,
		UserChannel:    s.recipe.UserChannel,
		RecipeRevision: s.rrev,
		Filename:       manifestFile,
	})
	if err != nil {
		return false, fmt.Errorf("get recipe manifest: %w", err)
	}

	return manifest != nil, nil
}

// rejectImmutable answers a refused overwrite or delete: 409 under immutable
// releases.
func (h *HTTPHandler) rejectImmutable(w http.ResponseWriter, s *scope, err error) {
	if errors.Is(err, trash.ErrImmutableRelease) {
		http.Error(w, "repository has immutable releases", http.StatusConflict)
		return
	}

	h.fail(w, "failed to check release immutability", err, "path", s.resource()+"/"+s.filename)
}

// writeStored answers a stored upload.
func (h *HTTPHandler) writeStored(w http.ResponseWriter, file *models.ConanPackage) {
	h.writeJSONStatus(w, http.StatusCreated, map[string]string{"id": file.ID, "name": file.Name, "sha256": file.SHA256})
}
