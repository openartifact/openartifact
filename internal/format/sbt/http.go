// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
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
// chi does not answer HEAD from a GET route, so it is registered explicitly:
// resolvers probe for a file with HEAD before fetching it.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/*", h.handleRequest)
	r.Head("/*", h.handleRequest)
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
	case http.MethodGet, http.MethodHead:
		h.handleDownload(w, r, repoID, filePath)
	case http.MethodPut:
		h.handleUpload(w, r, repoID, filePath)
	case http.MethodDelete:
		h.handleDelete(w, r, repoID, filePath)
	}
}

func repoNameOf(r *http.Request) string { return urlparams.Vars(r)["name"] }

func (h *HTTPHandler) get(ctx context.Context, repoID string, c base.ArtifactPath) (*models.SBTArtifact, error) {
	return h.DB.GetSBTArtifact(ctx, repoID, c.Group, c.Module, c.Version, c.Filename)
}

// lookup finds the artifact at cur, else the one an older release stored under
// the legacy reading of the same path.
func (h *HTTPHandler) lookup(ctx context.Context, repoID string, cur, legacy base.ArtifactPath) (*models.SBTArtifact, error) {
	artifact, err := h.get(ctx, repoID, cur)
	if err != nil || artifact != nil || cur == legacy {
		return artifact, err
	}

	return h.get(ctx, repoID, legacy)
}

// handleDownload serves a file (GET) or just its headers (HEAD).
func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	cur, legacy, ok := base.SplitArtifactPath(filePath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	artifact, err := h.lookup(r.Context(), repoID, cur, legacy)
	if err != nil {
		h.Log.Errorw("failed to get sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(filePath, cur.Filename, r.URL.Path)...) {
		return
	}

	if r.Method != http.MethodHead && h.TryPresign(w, r, presign.Blob{SHA256: artifact.BlobKey, Size: artifact.SizeBytes, Filename: path.Base(cur.Filename), ContentType: "application/octet-stream"}) {
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), artifact.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = blob.Close() }()

	w.WriteHeader(http.StatusOK)
	_, _ = iocopy.Copy(w, blob)
}

// lookupForUpload returns the row an upload replaces (previous: current
// coordinates only) and the row it collides with (existing: previous, else one
// stored under the legacy reading of the path), so the immutability guard
// cannot be sidestepped by shadowing a legacy row.
func (h *HTTPHandler) lookupForUpload(ctx context.Context, repoID string, cur, legacy base.ArtifactPath) (*models.SBTArtifact, *models.SBTArtifact, error) {
	previous, err := h.get(ctx, repoID, cur)
	if err != nil || previous != nil || cur == legacy {
		return previous, previous, err
	}

	existing, err := h.get(ctx, repoID, legacy)

	return nil, existing, err
}

// handleUpload stores a file. Publishing the same bytes again is a no-op;
// different bytes replace the file unless the repository has immutable releases
// and the version is a release.
func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	cur, legacy, ok := base.SplitArtifactPath(filePath)
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	spool, err := base.SpoolLimited(r.Body, maxSBTUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}

		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()
	defer spool.Close()

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, base.SpoolDescriptorLicenses(cur.Filename, spool)...) {
		return
	}

	sha256Hex := spool.SHA256Hex()

	artifact := &models.SBTArtifact{
		RepositoryID: repoID,
		GroupID:      cur.Group,
		ArtifactID:   cur.Module,
		Version:      cur.Version,
		ScalaVersion: "",
		Filename:     cur.Filename,
		SizeBytes:    spool.Size(),
		SHA256:       sha256Hex,
	}

	previous, existing, err := h.lookupForUpload(r.Context(), repoID, cur, legacy)
	if err != nil {
		h.Log.Errorw("failed to look up sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		if existing.SHA256 == sha256Hex {
			writeStored(w, existing)
			return
		}

		guardVersion := cur.Version
		if previous == nil {
			guardVersion = legacy.Version
		}

		if guardErr := trash.GuardReleaseByName(r, h.DB, repoNameOf(r), "sbt", guardVersion); guardErr != nil {
			h.rejectImmutable(w, filePath, guardErr)
			return
		}
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if err := quota.Enforce(r.Context(), h.DB, repoID, artifact.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to reserve quota", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Store the blob and insert the referencing row under the per-blob lock so
	// a concurrent unpublish cannot delete the blob in between.
	if err := storage.WithBlobLock(r.Context(), h.DB, sha256Hex, func(ctx context.Context) error {
		blobKey, err := h.BlobStore.Put(ctx, spool.Reader(), map[string]string{
			"sha256":   sha256Hex,
			"filename": cur.Filename,
			"group":    cur.Group,
		})
		if err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		artifact.BlobKey = blobKey

		artifact.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreateSBTArtifact(ctx, artifact); err != nil {
			return fmt.Errorf("create sbt artifact: %w", err)
		}

		return nil
	}); err != nil {
		if relErr := quota.Release(r.Context(), h.DB, repoID, artifact.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "error", relErr)
		}

		h.Log.Errorw("failed to store sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The replaced row's usage is gone once the new content is in place.
	if previous != nil {
		if relErr := quota.Release(r.Context(), h.DB, repoID, previous.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "error", relErr)
		}
	}

	// The replaced bytes are content-addressed and may be shared; drop them only
	// once nothing references them.
	if previous != nil {
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, previous.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up replaced sbt blob", "sha256", previous.SHA256, "error", delErr)
		}
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoNameOf(r),
		Format:     "sbt",
		Name:       cur.Group + "/" + cur.Module,
		Version:    cur.Version,
		Path:       filePath,
		SHA256:     sha256Hex,
		Size:       artifact.SizeBytes,
	}))

	writeStored(w, artifact)
}

func writeStored(w http.ResponseWriter, a *models.SBTArtifact) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":     a.ID,
		"sha256": a.SHA256,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// rejectImmutable answers a refused overwrite or delete: 409 when the
// repository has immutable releases.
func (h *HTTPHandler) rejectImmutable(w http.ResponseWriter, filePath string, err error) {
	if errors.Is(err, trash.ErrImmutableRelease) {
		http.Error(w, "released versions are immutable in this repository", http.StatusConflict)
		return
	}

	h.Log.Errorw("failed to check release immutability", "path", filePath, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	cur, legacy, ok := base.SplitArtifactPath(filePath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	artifact, err := h.lookup(r.Context(), repoID, cur, legacy)
	if err != nil {
		h.Log.Errorw("failed to get sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	if guardErr := trash.GuardReleaseByName(r, h.DB, repoNameOf(r), "sbt", cur.Version); guardErr != nil {
		h.rejectImmutable(w, filePath, guardErr)
		return
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID: repoID, Kind: models.TrashKindSBT, IDs: []string{artifact.ID}, Resource: filePath,
	}, func() error {
		if delErr := h.DB.DeleteSBTArtifact(r.Context(), artifact.ID); delErr != nil {
			return delErr
		}

		if relErr := quota.Release(r.Context(), h.DB, repoID, artifact.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "error", relErr)
		}

		// The blob is content-addressed and may be shared; remove it only once no
		// row references it any more.
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, artifact.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up blob after sbt delete", "sha256", artifact.SHA256, "error", delErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete sbt artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoNameOf(r),
		Format:     "sbt",
		Name:       cur.Group + "/" + cur.Module,
		Version:    cur.Version,
		Path:       filePath,
		SHA256:     artifact.SHA256,
		Size:       artifact.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}
