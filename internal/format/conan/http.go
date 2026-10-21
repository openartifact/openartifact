// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const maxConanUploadBytes = 200 << 20

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers Conan-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/v2/conans/{pkg}/{version}/{user}/{channel}/revisions", h.handleListRevisions)
	r.Get("/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rev}/files", h.handleListFiles)
	r.Get("/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rev}/files/{filename}", h.handleDownload)
	r.Put("/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rev}/files/{filename}", h.handleUpload)
	r.Delete("/v2/conans/{pkg}/{version}/{user}/{channel}", h.handleDelete)
	r.Get("/v2/conans/search", h.handleSearch)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("conan handler registered", "prefix", prefix)
}

// conanPath is the repository-relative identifier of a recipe (or one of its
// files when filename is set).
func conanPath(pkg, version, userChannel, filename string) string {
	p := pkg + "/" + version + "/" + userChannel
	if filename != "" {
		p += "/" + filename
	}

	return p
}

func (h *HTTPHandler) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkg := vars["pkg"]
	version := vars["version"]
	user := vars["user"]
	channel := vars["channel"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	userChannel := fmt.Sprintf("%s/%s", user, channel)

	conanPkg, err := h.DB.GetConanPackage(r.Context(), repoID, pkg, version, userChannel, "0")
	if err != nil {
		h.Log.Errorw("failed to get conan package", "pkg", pkg, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if conanPkg == nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"revisions": map[string]any{
			"0": map[string]any{
				"timestamp": conanPkg.CreatedAt.Unix(),
			},
		},
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleListFiles(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"files": map[string]any{
			"conanfile.py": map[string]any{
				"size": 0,
			},
			"conaninfo.txt": map[string]any{
				"size": 0,
			},
		},
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkg := vars["pkg"]
	version := vars["version"]
	user := vars["user"]
	channel := vars["channel"]
	filename := vars["filename"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	userChannel := fmt.Sprintf("%s/%s", user, channel)

	conanPkg, err := h.DB.GetConanPackage(r.Context(), repoID, pkg, version, userChannel, "0")
	if err != nil {
		h.Log.Errorw("failed to get conan package", "pkg", pkg, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if conanPkg == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(
		filename, conanPath(pkg, version, userChannel, filename), r.URL.Path)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: conanPkg.BlobKey, Filename: filename, ContentType: "application/octet-stream"}) {
		return
	}

	// Download blob
	blob, err := h.BlobStore.Get(r.Context(), conanPkg.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", conanPkg.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = blob.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, blob)
}

func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkg := vars["pkg"]
	version := vars["version"]
	user := vars["user"]
	channel := vars["channel"]
	filename := vars["filename"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	// Read file data
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

	sha256Hash := sha256.Sum256(data)

	sha256Hex := hex.EncodeToString(sha256Hash[:])
	userChannel := fmt.Sprintf("%s/%s", user, channel)
	conanPkg := &models.ConanPackage{
		RepositoryID:   repoID,
		Name:           pkg,
		Version:        version,
		UserChannel:    userChannel,
		RecipeRevision: "0",
		ConanFile:      "",
		ConanInfo:      "",
	}

	// Store the blob and insert the referencing row under the per-blob lock so
	// a concurrent unpublish cannot delete the blob in between.
	if err := storage.WithBlobLock(r.Context(), h.DB, sha256Hex, func(ctx context.Context) error {
		blobKey, err := h.BlobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"sha256":   sha256Hex,
			"filename": filename,
			"package":  pkg,
		})
		if err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		conanPkg.BlobKey = blobKey

		if err := h.DB.CreateConanPackage(ctx, conanPkg); err != nil {
			return fmt.Errorf("create conan package: %w", err)
		}

		return nil
	}); err != nil {
		h.Log.Errorw("failed to store conan package", "pkg", pkg, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "conan",
		Name:       pkg,
		Version:    version,
		Path:       conanPath(pkg, version, userChannel, filename),
		SHA256:     sha256Hex,
		Size:       int64(len(data)),
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":     conanPkg.ID,
		"name":   conanPkg.Name,
		"sha256": sha256Hex,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkg := vars["pkg"]
	version := vars["version"]
	user := vars["user"]
	channel := vars["channel"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	userChannel := fmt.Sprintf("%s/%s", user, channel)

	conanPkg, err := h.DB.GetConanPackage(r.Context(), repoID, pkg, version, userChannel, "0")
	if err != nil {
		h.Log.Errorw("failed to get conan package", "pkg", pkg, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if conanPkg == nil {
		http.NotFound(w, r)
		return
	}

	// Delete package record
	err = h.DB.DeleteConanPackage(r.Context(), conanPkg.ID)
	if err != nil {
		h.Log.Errorw("failed to delete conan package", "id", conanPkg.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The blob is content-addressed and may be shared; remove it only once no
	// row references it any more.
	if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, conanPkg.BlobKey); delErr != nil {
		h.Log.Warnw("failed to clean up blob after conan delete", "sha256", conanPkg.BlobKey, "error", delErr)
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "conan",
		Name:       pkg,
		Version:    version,
		Path:       conanPath(pkg, version, userChannel, ""),
		SHA256:     conanPkg.BlobKey,
	}))

	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) handleSearch(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	query := r.URL.Query().Get("q")

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	packages, err := h.DB.ListConanPackages(r.Context(), repoID)
	if err != nil {
		h.Log.Errorw("failed to list conan packages", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Filter by query if provided
	var results []*models.ConanPackage

	for _, pkg := range packages {
		if query == "" || strings.Contains(pkg.Name, query) {
			results = append(results, pkg)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"results": results,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}
