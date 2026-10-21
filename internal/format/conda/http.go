// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // required by protocol specification
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const maxCondaUploadBytes = 500 << 20

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers Conda-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/{platform}/repodata.json", h.handleRepodata)
	r.Get("/{platform}/{filename}", h.handleDownload)
	r.Post("/{platform}", h.handleUpload)
	r.Delete("/{platform}/{filename}", h.handleDelete)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("conda handler registered", "prefix", prefix)
}

func (h *HTTPHandler) handleRepodata(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	platform := vars["platform"]

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

	// List all packages for this platform
	packages, err := h.DB.ListCondaPackages(r.Context(), repoID, platform)
	if err != nil {
		h.Log.Errorw("failed to list conda packages", "repo", repoName, "platform", platform, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Build repodata.json
	repodata := map[string]any{
		"info": map[string]any{
			"subdir": platform,
		},
		"packages":       make(map[string]any),
		"packages.conda": make(map[string]any),
	}

	pkgs, ok := repodata["packages"].(map[string]any)
	if !ok {
		h.Log.Errorw("failed to cast packages to map", "repo", repoName, "platform", platform)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	for _, pkg := range packages {
		filename := fmt.Sprintf("%s-%s-%s.tar.bz2", pkg.Name, pkg.Version, pkg.BuildString)
		pkgs[filename] = map[string]any{
			"name":    pkg.Name,
			"version": pkg.Version,
			"build":   pkg.BuildString,
			"depends": pkg.Depends,
			"sha256":  pkg.SHA256,
			"size":    pkg.SizeBytes,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(repodata); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	platform := vars["platform"]
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

	// Parse filename to get package info
	parts := strings.TrimSuffix(filename, ".tar.bz2")
	parts = strings.TrimSuffix(parts, ".conda")

	segments := strings.Split(parts, "-")
	if len(segments) < 3 {
		http.NotFound(w, r)
		return
	}

	name := segments[0]
	version := segments[1]
	buildString := strings.Join(segments[2:], "-")

	pkg, err := h.DB.GetCondaPackage(r.Context(), repoID, name, version, buildString, platform)
	if err != nil {
		h.Log.Errorw("failed to get conda package", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(filename, platform+"/"+filename, r.URL.Path)...) {
		return
	}

	// Download blob
	blob, err := h.BlobStore.Get(r.Context(), pkg.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", pkg.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = blob.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(pkg.SizeBytes, 10))
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, blob)
}

func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	platform := vars["platform"]

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

	// Get filename from query or header
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		filename = r.Header.Get("X-Filename")
	}

	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}

	// Read and hash the file
	data, err := base.ReadAllLimited(r.Body, maxCondaUploadBytes)
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
	md5Hash := md5.Sum(data) //nolint:gosec // required by protocol specification

	// Parse filename
	parts := strings.TrimSuffix(filename, ".tar.bz2")
	parts = strings.TrimSuffix(parts, ".conda")

	segments := strings.Split(parts, "-")
	if len(segments) < 3 {
		http.Error(w, "invalid filename format", http.StatusBadRequest)
		return
	}

	name := segments[0]
	version := segments[1]
	buildString := strings.Join(segments[2:], "-")

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, declaredLicense(filename, data)) {
		return
	}

	sha256Hex := hex.EncodeToString(sha256Hash[:])

	pkg := &models.CondaPackage{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		BuildString:  buildString,
		Platform:     platform,
		Depends:      []string{},
		SizeBytes:    int64(len(data)),
		SHA256:       sha256Hex,
		MD5:          hex.EncodeToString(md5Hash[:]),
	}

	// Store the blob and insert the referencing row under the per-blob lock so
	// a concurrent unpublish cannot delete the blob in between.
	if err := storage.WithBlobLock(r.Context(), h.DB, sha256Hex, func(ctx context.Context) error {
		blobKey, err := h.BlobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"sha256":   sha256Hex,
			"filename": filename,
			"platform": platform,
		})
		if err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		pkg.BlobKey = blobKey

		if err := h.DB.CreateCondaPackage(ctx, pkg); err != nil {
			return fmt.Errorf("create conda package: %w", err)
		}

		return nil
	}); err != nil {
		h.Log.Errorw("failed to store conda package", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "conda",
		Name:       name,
		Version:    version,
		Path:       platform + "/" + filename,
		SHA256:     sha256Hex,
		Size:       pkg.SizeBytes,
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":      pkg.ID,
		"name":    pkg.Name,
		"version": pkg.Version,
		"sha256":  pkg.SHA256,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	platform := vars["platform"]
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

	// Parse filename
	parts := strings.TrimSuffix(filename, ".tar.bz2")
	parts = strings.TrimSuffix(parts, ".conda")

	segments := strings.Split(parts, "-")
	if len(segments) < 3 {
		http.NotFound(w, r)
		return
	}

	name := segments[0]
	version := segments[1]
	buildString := strings.Join(segments[2:], "-")

	pkg, err := h.DB.GetCondaPackage(r.Context(), repoID, name, version, buildString, platform)
	if err != nil {
		h.Log.Errorw("failed to get conda package", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	// Delete package record
	err = h.DB.DeleteCondaPackage(r.Context(), pkg.ID)
	if err != nil {
		h.Log.Errorw("failed to delete conda package", "id", pkg.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The blob is content-addressed and may be shared; remove it only once no
	// row references it any more.
	if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, pkg.SHA256); delErr != nil {
		h.Log.Warnw("failed to clean up blob after conda delete", "sha256", pkg.SHA256, "error", delErr)
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "conda",
		Name:       pkg.Name,
		Version:    pkg.Version,
		Path:       platform + "/" + filename,
		SHA256:     pkg.SHA256,
		Size:       pkg.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}
