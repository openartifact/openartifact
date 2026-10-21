// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

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
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const maxSBTUploadBytes = 500 << 20

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers SBT-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
// Routes use a true chi wildcard ("/*"): {name:regexp} params never span "/".
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/*", h.handleRequest)
	r.Put("/*", h.handleRequest)
	r.Delete("/*", h.handleRequest)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("sbt handler registered", "prefix", prefix)
}

func (h *HTTPHandler) handleRequest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filePath := vars["*"]

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

	switch r.Method {
	case http.MethodGet:
		h.handleDownload(w, r, repoID, filePath)
	case http.MethodPut:
		h.handleUpload(w, r, repoID, filePath)
	case http.MethodDelete:
		h.handleDelete(w, r, repoID, filePath)
	}
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	// Parse path: group/artifact/version/filename
	parts := strings.Split(filePath, "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}

	groupID := parts[0]
	artifactID := parts[1]
	version := parts[2]
	filename := strings.Join(parts[3:], "/")

	artifact, err := h.DB.GetSBTArtifact(r.Context(), repoID, groupID, artifactID, version, filename)
	if err != nil {
		h.Log.Errorw("failed to get sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(filePath, filename, r.URL.Path)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: artifact.BlobKey, Size: artifact.SizeBytes, Filename: filename, ContentType: "application/octet-stream"}) {
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), artifact.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = blob.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, blob)
}

func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	parts := strings.Split(filePath, "/")
	if len(parts) < 4 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	groupID := parts[0]
	artifactID := parts[1]
	version := parts[2]
	filename := strings.Join(parts[3:], "/")

	data, err := base.ReadAllLimited(r.Body, maxSBTUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}

		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, base.DescriptorLicenses(filename, data)...) {
		return
	}

	sha256Hash := sha256.Sum256(data)

	sha256Hex := hex.EncodeToString(sha256Hash[:])

	artifact := &models.SBTArtifact{
		RepositoryID: repoID,
		GroupID:      groupID,
		ArtifactID:   artifactID,
		Version:      version,
		ScalaVersion: "",
		Filename:     filename,
		SizeBytes:    int64(len(data)),
		SHA256:       sha256Hex,
	}

	// Store the blob and insert the referencing row under the per-blob lock so
	// a concurrent unpublish cannot delete the blob in between.
	if err := storage.WithBlobLock(r.Context(), h.DB, sha256Hex, func(ctx context.Context) error {
		blobKey, err := h.BlobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"sha256":   sha256Hex,
			"filename": filename,
			"group":    groupID,
		})
		if err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		artifact.BlobKey = blobKey

		if err := h.DB.CreateSBTArtifact(ctx, artifact); err != nil {
			return fmt.Errorf("create sbt artifact: %w", err)
		}

		return nil
	}); err != nil {
		h.Log.Errorw("failed to store sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: urlparams.Vars(r)["name"],
		Format:     "sbt",
		Name:       groupID + "/" + artifactID,
		Version:    version,
		Path:       filePath,
		SHA256:     sha256Hex,
		Size:       artifact.SizeBytes,
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":     artifact.ID,
		"sha256": artifact.SHA256,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	parts := strings.Split(filePath, "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}

	groupID := parts[0]
	artifactID := parts[1]
	version := parts[2]
	filename := strings.Join(parts[3:], "/")

	artifact, err := h.DB.GetSBTArtifact(r.Context(), repoID, groupID, artifactID, version, filename)
	if err != nil {
		h.Log.Errorw("failed to get sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	err = h.DB.DeleteSBTArtifact(r.Context(), artifact.ID)
	if err != nil {
		h.Log.Errorw("failed to delete sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The blob is content-addressed and may be shared; remove it only once no
	// row references it any more.
	if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, artifact.SHA256); delErr != nil {
		h.Log.Warnw("failed to clean up blob after sbt delete", "sha256", artifact.SHA256, "error", delErr)
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: urlparams.Vars(r)["name"],
		Format:     "sbt",
		Name:       groupID + "/" + artifactID,
		Version:    version,
		Path:       filePath,
		SHA256:     artifact.SHA256,
		Size:       artifact.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}
