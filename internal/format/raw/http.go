// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package raw

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// maxRawUploadBytes bounds a single raw upload.
const maxRawUploadBytes = 5 << 30

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers Raw-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
// Routes use a true chi wildcard ("/*"): {name:regexp} params never span "/".
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/", h.handleGet)
	r.Put("/*", h.handlePut)
	r.Get("/*", h.handleGet)
	r.Head("/*", h.handleHead)
	r.Delete("/*", h.handleDelete)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("raw handler registered", "prefix", prefix)
}

func (h *HTTPHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	path := vars["*"]

	// Normalize path - remove leading slash if present
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		http.Error(w, "path cannot be empty", http.StatusBadRequest)
		return
	}

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

	// Spool the body to a temp file while hashing it, so the per-blob lock is
	// only held for the (fast) store + insert and not while the client streams.
	tmp, err := os.CreateTemp("", "raw-upload-*")
	if err != nil {
		h.Log.Errorw("failed to create spool file", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	defer func() { _ = r.Body.Close() }()

	hasher := sha256.New()

	size, err := iocopy.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(r.Body, maxRawUploadBytes+1))
	if err != nil {
		h.Log.Errorw("failed to read upload", "path", path, "error", err)
		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}

	if size > maxRawUploadBytes {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}

	sha256Hex := hex.EncodeToString(hasher.Sum(nil))

	// Detect content type
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		buf := make([]byte, 512)

		n, _ := tmp.ReadAt(buf, 0)
		if n > 0 {
			contentType = http.DetectContentType(buf[:n])
		} else {
			contentType = "application/octet-stream"
		}
	}

	artifact := &models.RawArtifact{
		ID:           uuid.New().String(),
		RepositoryID: repoID,
		Path:         path,
		ContentType:  contentType,
		SizeBytes:    size,
		SHA256:       sha256Hex,
		Metadata:     make(map[string]any),
		// Zero unless an administrator supplied an origin timestamp; the
		// database then stamps "now".
		CreatedAt: importmeta.CreatedAt(r.Context()),
	}

	previous, err := Commit(r, h.Handler, repoName, artifact, storage.ReaderBlob(sha256Hex, map[string]string{
		"sha256": sha256Hex,
		"format": "raw",
		"path":   path,
	}, func() io.Reader { return io.NewSectionReader(tmp, 0, size) }))
	if err != nil {
		if errors.Is(err, trash.ErrImmutableRelease) {
			h.writeGuardError(w, err, "overwrite", path)
			return
		}

		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to store raw artifact", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":     StoredID(artifact, previous),
		"path":   path,
		"sha256": sha256Hex,
		"size":   size,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	path := vars["*"]

	// Normalize path
	path = strings.TrimPrefix(path, "/")

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

	// If path is empty or ends with /, list files
	if path == "" || strings.HasSuffix(path, "/") {
		h.handleList(w, r, repoID, path)
		return
	}

	// Get artifact
	artifact, err := h.DB.GetRawArtifact(r.Context(), repoID, path)
	if err != nil {
		h.Log.Errorw("failed to get artifact", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(path, r.URL.Path)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: artifact.BlobKey, Size: artifact.SizeBytes, Filename: path[strings.LastIndexByte(path, '/')+1:], ContentType: artifact.ContentType}) {
		return
	}

	// Stream blob
	rc, err := h.BlobStore.Get(r.Context(), artifact.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", artifact.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))

	w.Header().Set("X-Checksum-Sha256", artifact.SHA256)
	w.WriteHeader(http.StatusOK)

	if _, err := iocopy.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write response", "path", path, "error", err)
	}
}

func (h *HTTPHandler) handleList(w http.ResponseWriter, r *http.Request, repoID, prefix string) {
	// A directory listing matches whole path segments: "docs/" must not list
	// "docs-old/x". The store matches a plain string prefix, so keep the slash.
	if prefix = strings.TrimSuffix(prefix, "/"); prefix != "" {
		prefix += "/"
	}

	artifacts, err := h.DB.ListRawArtifacts(r.Context(), repoID, prefix)
	if err != nil {
		h.Log.Errorw("failed to list artifacts", "prefix", prefix, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifacts == nil {
		artifacts = []*models.RawArtifact{}
	}

	// Build response
	var items []map[string]any
	for _, a := range artifacts {
		items = append(items, map[string]any{
			"path":         a.Path,
			"size":         a.SizeBytes,
			"content_type": a.ContentType,
			"sha256":       a.SHA256,
			"created_at":   a.CreatedAt,
			"updated_at":   a.UpdatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(items); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleHead(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	path := vars["*"]

	// Normalize path
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		http.Error(w, "path cannot be empty", http.StatusBadRequest)
		return
	}

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

	// Get artifact
	artifact, err := h.DB.GetRawArtifact(r.Context(), repoID, path)
	if err != nil {
		h.Log.Errorw("failed to get artifact", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(path, r.URL.Path)...) {
		return
	}

	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))

	w.Header().Set("X-Checksum-Sha256", artifact.SHA256)
	w.WriteHeader(http.StatusOK)
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	path := vars["*"]

	// Normalize path
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		http.Error(w, "path cannot be empty", http.StatusBadRequest)
		return
	}

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

	artifact, err := h.DB.GetRawArtifact(r.Context(), repoID, path)
	if err != nil {
		h.Log.Errorw("failed to get artifact", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repoName, "raw", ""); gErr != nil {
		h.writeGuardError(w, gErr, "delete", path)
		return
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repoID,
		Kind:     models.TrashKindRaw,
		IDs:      []string{artifact.ID},
		Resource: path,
	}, func() error {
		if delErr := h.DB.DeleteRawArtifact(r.Context(), repoID, path); delErr != nil {
			return fmt.Errorf("delete raw artifact: %w", delErr)
		}

		if relErr := quota.Release(r.Context(), h.DB, repoID, artifact.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", relErr)
		}

		// The blob is content-addressed and may be shared; remove it only once
		// no row references it any more.
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, artifact.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up blob after raw delete", "sha256", artifact.SHA256, "error", delErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete artifact", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "raw",
		Name:       path,
		Path:       path,
		SHA256:     artifact.SHA256,
		Size:       artifact.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}

// writeGuardError answers a failed immutable-release guard.
func (h *HTTPHandler) writeGuardError(w http.ResponseWriter, err error, action, path string) {
	if errors.Is(err, trash.ErrImmutableRelease) {
		http.Error(w, "repository has immutable releases: cannot "+action+" "+path, http.StatusConflict)
		return
	}

	h.Log.Errorw("failed to check release immutability", "path", path, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
