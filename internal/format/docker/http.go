// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

type HTTPHandler struct {
	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
	baseURL   string
	publisher webhook.Publisher
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{log: log, db: db, blobStore: blobStore, baseURL: baseURL}
}

// SetPublisher sets the webhook publisher used for artifact events. It must be
// called before the handler serves requests.
func (h *HTTPHandler) SetPublisher(p webhook.Publisher) { h.publisher = p }

func (h *HTTPHandler) RegisterRoutes(r chi.Router) {
	r.Get("/v2/", h.handleVersionCheck)

	// Manifest operations
	r.Get("/v2/{name:*}/manifests/{reference}", h.handleGetManifest)
	r.Put("/v2/{name:*}/manifests/{reference}", h.handlePutManifest)
	r.Delete("/v2/{name:*}/manifests/{reference}", h.handleDeleteManifest)
	r.Head("/v2/{name:*}/manifests/{reference}", h.handleHeadManifest)

	// Blob operations
	r.Get("/v2/{name:*}/blobs/{digest}", h.handleGetBlob)
	r.Head("/v2/{name:*}/blobs/{digest}", h.handleHeadBlob)
	r.Delete("/v2/{name:*}/blobs/{digest}", h.handleDeleteBlob)

	// Blob upload flow
	r.Post("/v2/{name:*}/blobs/uploads/", h.handleInitiateBlobUpload)
	r.Get("/v2/{name:*}/blobs/uploads/{uuid}", h.handleGetUploadStatus)
	r.Patch("/v2/{name:*}/blobs/uploads/{uuid}", h.handleUploadChunk)
	r.Put("/v2/{name:*}/blobs/uploads/{uuid}", h.handleCompleteBlobUpload)
	r.Delete("/v2/{name:*}/blobs/uploads/{uuid}", h.handleCancelBlobUpload)

	// Tags list
	r.Get("/v2/{name:*}/tags/list", h.handleListTags)

	// OCI Referrers
	r.Get("/v2/{name:*}/referrers/{digest}", h.handleGetReferrers)
}

// resolveRepo looks up a repository by name.
func (h *HTTPHandler) resolveRepo(ctx context.Context, name string) (*models.Repository, error) {
	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		return nil, err
	}

	return repo, nil
}

// errorResponse writes a Docker Registry error response.
func (h *HTTPHandler) errorResponse(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	resp := map[string]any{
		"errors": []map[string]any{
			{
				"code":    code,
				"message": message,
				"detail":  nil,
			},
		},
	}
	if encErr := json.NewEncoder(w).Encode(resp); encErr != nil {
		_ = encErr // response header already written
	}
}

// handleVersionCheck returns 200 OK with Docker-Distribution-API-Version header.
func (h *HTTPHandler) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

// handleGetManifest retrieves a manifest by tag or digest.
func (h *HTTPHandler) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	reference := vars["reference"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	var (
		manifest *models.DockerManifest
		digest   string
	)

	// If reference is a digest (sha256:...), look up directly
	if strings.HasPrefix(reference, "sha256:") {
		digest = reference

		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, digest)
		if err != nil {
			h.log.Errorw("failed to get manifest by digest", "digest", digest, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	} else {
		// Otherwise, treat as a tag name
		tag, err := h.db.GetDockerTag(r.Context(), repo.ID, reference)
		if err != nil {
			h.log.Errorw("failed to get tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}

		if tag == nil {
			h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "tag not found")
			return
		}

		digest = tag.ManifestDigest

		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, digest)
		if err != nil {
			h.log.Errorw("failed to get manifest by tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	}

	if manifest == nil {
		h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found")
		return
	}

	if quarantine.Enforce(w, r, h.db, repo.ID, quarantine.PathKeys(
		manifest.ID, manifest.Digest, reference, name+"/manifests/"+reference, name+"/manifests/"+manifest.Digest, r.URL.Path)...) {
		return
	}

	// Manifest content is content-addressed in the blob store, keyed by the
	// hex portion of its digest; the DB row only tracks metadata + blob_id.
	content, err := h.blobStore.Get(r.Context(), manifestBlobKey(manifest.Digest))
	if err != nil {
		h.log.Errorw("failed to get manifest blob from store", "digest", manifest.Digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}
	defer func() { _ = content.Close() }()

	w.Header().Set("Content-Type", manifest.MediaType)
	w.Header().Set("Docker-Content-Digest", manifest.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(manifest.Size, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, content); err != nil {
		h.log.Errorw("failed to write manifest response", "digest", manifest.Digest, "error", err)
	}
}

// handlePutManifest pushes a manifest.
func (h *HTTPHandler) handlePutManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	reference := vars["reference"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	// Read manifest body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.Errorw("failed to read manifest body", "error", err)
		h.errorResponse(w, http.StatusBadRequest, "DIGEST_INVALID", "failed to read body")

		return
	}
	defer func() { _ = r.Body.Close() }()

	// Compute SHA256 digest
	hash := sha256.Sum256(body)
	digestHex := hex.EncodeToString(hash[:])
	digest := "sha256:" + digestHex

	// When pushed by digest reference, the client's claimed digest MUST
	// match the content actually received.
	if strings.HasPrefix(reference, "sha256:") && reference != digest {
		h.log.Warnw("manifest digest mismatch", "reference", reference, "computed", digest)
		h.errorResponse(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest digest does not match reference")

		return
	}

	// Get content type from header
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/vnd.docker.distribution.manifest.v2+json"
	}

	meta := parseOCIManifestMeta(body)

	subjectDigest := ""
	if meta.Subject != nil {
		subjectDigest = meta.Subject.Digest
	}

	manifest := &models.DockerManifest{
		ID:            uuid.New().String(),
		RepositoryID:  repo.ID,
		Digest:        digest,
		MediaType:     contentType,
		Size:          int64(len(body)),
		SubjectDigest: subjectDigest,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// Persist the manifest content in the blob store (content-addressed by
	// sha256), ensure a row exists for it in the global blobs table (the
	// docker_manifests row has a NOT NULL blob_id FK) and insert the manifest
	// — all under the per-blob lock so a concurrent unpublish cannot delete
	// the blob in between.
	if err := storage.WithBlobLock(r.Context(), h.db, digestHex, func(ctx context.Context) error {
		blobKey, err := h.blobStore.Put(ctx, bytes.NewReader(body), map[string]string{
			"sha256":       digestHex,
			"content-type": contentType,
		})
		if err != nil {
			return fmt.Errorf("store manifest blob: %w", err)
		}

		blobID, err := h.db.EnsureBlob(ctx, blobKey, contentType, int64(len(body)))
		if err != nil {
			return fmt.Errorf("ensure manifest blob row: %w", err)
		}

		manifest.BlobID = blobID

		if err := h.db.CreateDockerManifest(ctx, manifest); err != nil {
			return fmt.Errorf("create manifest: %w", err)
		}

		return nil
	}); err != nil {
		h.log.Errorw("failed to store manifest", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// If reference is a tag (not a digest), upsert the tag
	if !strings.HasPrefix(reference, "sha256:") {
		tag := &models.DockerTag{
			ID:             uuid.New().String(),
			RepositoryID:   repo.ID,
			Name:           reference,
			ManifestDigest: digest,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}
		if err := h.db.UpsertDockerTag(r.Context(), tag); err != nil {
			h.log.Errorw("failed to upsert tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	}

	if subjectDigest != "" {
		// Tells the client we processed the manifest's `subject` field, per
		// the OCI distribution spec's "Pushing Manifests with Subject".
		w.Header().Set("Oci-Subject", subjectDigest)
	}

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: name,
		Format:     "docker",
		Name:       name,
		Version:    reference,
		Path:       name + "/manifests/" + reference,
		SHA256:     digestHex,
		Size:       int64(len(body)),
	}))

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// handleDeleteManifest deletes a manifest.
func (h *HTTPHandler) handleDeleteManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	reference := vars["reference"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	var digest string

	// If reference is a digest, use it directly
	if strings.HasPrefix(reference, "sha256:") {
		digest = reference
	} else {
		// Otherwise, resolve tag to digest
		tag, err := h.db.GetDockerTag(r.Context(), repo.ID, reference)
		if err != nil {
			h.log.Errorw("failed to get tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}

		if tag == nil {
			h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "tag not found")
			return
		}

		digest = tag.ManifestDigest

		// Delete the tag
		if err := h.db.DeleteDockerTag(r.Context(), repo.ID, reference); err != nil {
			h.log.Errorw("failed to delete tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	}

	deleted, err := h.db.GetDockerManifest(r.Context(), repo.ID, digest)
	if err != nil {
		h.log.Errorw("failed to get manifest before delete", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Delete the manifest row first so BlobReferenceCount no longer sees it.
	if err := h.db.DeleteDockerManifest(r.Context(), repo.ID, digest); err != nil {
		h.log.Errorw("failed to delete manifest", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Reclaim the manifest's content blob from storage only if no other
	// docker_manifests row (or any other format table) still references it.
	if err := storage.SafeDeleteBlob(r.Context(), h.db, h.blobStore, manifestBlobKey(digest)); err != nil {
		h.log.Errorw("failed to delete manifest blob from store", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	var deletedSize int64
	if deleted != nil {
		deletedSize = deleted.Size
	}

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: name,
		Format:     "docker",
		Name:       name,
		Version:    reference,
		Path:       name + "/manifests/" + reference,
		SHA256:     manifestBlobKey(digest),
		Size:       deletedSize,
	}))

	w.WriteHeader(http.StatusAccepted)
}

// handleHeadManifest checks if a manifest exists.
func (h *HTTPHandler) handleHeadManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	reference := vars["reference"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	var manifest *models.DockerManifest

	// If reference is a digest, look up directly
	if strings.HasPrefix(reference, "sha256:") {
		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, reference)
		if err != nil {
			h.log.Errorw("failed to get manifest by digest", "digest", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	} else {
		// Otherwise, resolve tag to digest
		tag, err := h.db.GetDockerTag(r.Context(), repo.ID, reference)
		if err != nil {
			h.log.Errorw("failed to get tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}

		if tag == nil {
			h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "tag not found")
			return
		}

		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, tag.ManifestDigest)
		if err != nil {
			h.log.Errorw("failed to get manifest by tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	}

	if manifest == nil {
		h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found")
		return
	}

	w.Header().Set("Content-Type", manifest.MediaType)
	w.Header().Set("Docker-Content-Digest", manifest.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(manifest.Size, 10))
	w.WriteHeader(http.StatusOK)
}

// handleGetBlob downloads a blob.
func (h *HTTPHandler) handleGetBlob(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	digest := vars["digest"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	blob, err := h.db.GetDockerBlob(r.Context(), repo.ID, digest)
	if err != nil {
		h.log.Errorw("failed to get blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if blob == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return
	}

	if quarantine.Enforce(w, r, h.db, repo.ID, quarantine.PathKeys(
		blob.ID, digest, name+"/blobs/"+digest, r.URL.Path)...) {
		return
	}

	// Get blob from blobstore
	rc, err := h.blobStore.Get(r.Context(), blob.BlobSHA256)
	if err != nil {
		h.log.Errorw("failed to get blob from store", "sha256", blob.BlobSHA256, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", blob.MediaType)
	w.Header().Set("Docker-Content-Digest", blob.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(blob.Size, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, rc); err != nil {
		h.log.Errorw("failed to write blob response", "digest", digest, "error", err)
	}
}

// handleHeadBlob checks if a blob exists.
func (h *HTTPHandler) handleHeadBlob(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	digest := vars["digest"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	blob, err := h.db.GetDockerBlob(r.Context(), repo.ID, digest)
	if err != nil {
		h.log.Errorw("failed to get blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if blob == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return
	}

	w.Header().Set("Content-Type", blob.MediaType)
	w.Header().Set("Docker-Content-Digest", blob.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(blob.Size, 10))
	w.WriteHeader(http.StatusOK)
}

// handleDeleteBlob deletes a blob.
func (h *HTTPHandler) handleDeleteBlob(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	digest := vars["digest"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	blob, err := h.db.GetDockerBlob(r.Context(), repo.ID, digest)
	if err != nil {
		h.log.Errorw("failed to get blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if blob == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return
	}

	// Delete the per-repo registry row first so BlobReferenceCount no
	// longer sees it, then let SafeDeleteBlob decide whether the underlying
	// storage content is still referenced elsewhere (cross-repo dedup)
	// before reclaiming it.
	if err := h.db.DeleteDockerBlob(r.Context(), repo.ID, digest); err != nil {
		h.log.Errorw("failed to delete blob from db", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if err := storage.SafeDeleteBlob(r.Context(), h.db, h.blobStore, blob.BlobSHA256); err != nil {
		h.log.Errorw("failed to delete blob from store", "sha256", blob.BlobSHA256, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleInitiateBlobUpload starts a chunked blob upload.
func (h *HTTPHandler) handleInitiateBlobUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	// Create upload record with UUID
	uploadUUID := uuid.New().String()
	upload := &models.DockerUpload{
		ID:           uuid.New().String(),
		RepositoryID: repo.ID,
		UploadUUID:   uploadUUID,
		Offset:       0,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := h.db.CreateDockerUpload(r.Context(), upload); err != nil {
		h.log.Errorw("failed to create upload record", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Create temp directory if needed
	uploadDir := "/tmp/openartifact/docker-uploads"
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		h.log.Errorw("failed to create upload directory", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", "0-0")
	w.Header().Set("Docker-Upload-Uuid", uploadUUID)
	w.WriteHeader(http.StatusAccepted)
}

// handleGetUploadStatus gets the status of an in-progress upload.
func (h *HTTPHandler) handleGetUploadStatus(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	upload, err := h.db.GetDockerUpload(r.Context(), repo.ID, uploadUUID)
	if err != nil {
		h.log.Errorw("failed to get upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if upload == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", fmt.Sprintf("0-%d", upload.Offset-1))
	w.Header().Set("Docker-Upload-Uuid", uploadUUID)
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadChunk appends data to an in-progress upload.
func (h *HTTPHandler) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	upload, err := h.db.GetDockerUpload(r.Context(), repo.ID, uploadUUID)
	if err != nil {
		h.log.Errorw("failed to get upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if upload == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}

	// A chunk's declared start MUST be contiguous with what the server has
	// already accepted — no gap, no overlap. Reject before writing anything.
	if cr := r.Header.Get("Content-Range"); cr != "" {
		start, _, rangeErr := parseContentRange(cr)
		if rangeErr != nil {
			h.errorResponse(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", rangeErr.Error())
			return
		}

		if offsetErr := validateChunkOffset(upload.Offset, start); offsetErr != nil {
			h.log.Warnw("chunk offset mismatch", "uuid", uploadUUID, "current_offset", upload.Offset, "start", start)
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
			w.Header().Set("Range", fmt.Sprintf("0-%d", upload.Offset-1))
			h.errorResponse(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", offsetErr.Error())

			return
		}
	}

	defer func() { _ = r.Body.Close() }()

	// Stream chunk data straight to the temp file to avoid buffering the whole
	// chunk (up to GBs for large layers) in memory.
	uploadDir := "/tmp/openartifact/docker-uploads"
	tempFile := filepath.Join(uploadDir, uploadUUID)

	//nolint:gosec // G703: path is constructed from validated upload UUID, not user input
	f, err := os.OpenFile(tempFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		h.log.Errorw("failed to open temp file", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	written, err := io.Copy(f, r.Body)
	if err != nil {
		_ = f.Close()

		h.log.Errorw("failed to write chunk", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if err := f.Close(); err != nil {
		h.log.Errorw("failed to close temp file", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Update offset in database
	newOffset := upload.Offset + written
	if err := h.db.UpdateDockerUploadOffset(r.Context(), repo.ID, uploadUUID, newOffset); err != nil {
		h.log.Errorw("failed to update upload offset", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", fmt.Sprintf("0-%d", newOffset-1))
	w.Header().Set("Docker-Upload-Uuid", uploadUUID)
	w.WriteHeader(http.StatusAccepted)
}

// handleCompleteBlobUpload finalizes a blob upload.
func (h *HTTPHandler) handleCompleteBlobUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]
	digest := r.URL.Query().Get("digest")

	if digest == "" {
		h.errorResponse(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query parameter required")
		return
	}

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	upload, err := h.db.GetDockerUpload(r.Context(), repo.ID, uploadUUID)
	if err != nil {
		h.log.Errorw("failed to get upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if upload == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}

	// A final chunk's declared start MUST be contiguous with what the
	// server has already accepted, same as PATCH.
	if cr := r.Header.Get("Content-Range"); cr != "" {
		start, _, rangeErr := parseContentRange(cr)
		if rangeErr != nil {
			h.errorResponse(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", rangeErr.Error())
			return
		}

		if offsetErr := validateChunkOffset(upload.Offset, start); offsetErr != nil {
			h.log.Warnw("final chunk offset mismatch", "uuid", uploadUUID, "current_offset", upload.Offset, "start", start)
			w.Header().Set("Range", fmt.Sprintf("0-%d", upload.Offset-1))
			h.errorResponse(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", offsetErr.Error())

			return
		}
	}

	defer func() { _ = r.Body.Close() }()

	uploadDir := "/tmp/openartifact/docker-uploads"
	tempFile := filepath.Join(uploadDir, uploadUUID)

	// Stream any final bytes from the request body directly to the temp file.
	// For monolithic uploads (single PUT carrying the whole blob) this avoids
	// buffering the entire layer in memory.
	//nolint:gosec // G703: path is constructed from validated upload UUID, not user input
	f, err := os.OpenFile(tempFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		h.log.Errorw("failed to open temp file for final chunk", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if _, err := io.Copy(f, r.Body); err != nil {
		_ = f.Close()

		h.log.Errorw("failed to write final chunk", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if err := f.Close(); err != nil {
		h.log.Errorw("failed to close temp file", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Compute the SHA256 (and size) of the temp file in a single pass.
	//nolint:gosec // G703: path is constructed from validated upload UUID, not user input
	f, err = os.Open(tempFile)
	if err != nil {
		h.log.Errorw("failed to open temp file for hashing", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}
	defer func() { _ = f.Close() }()

	hash := sha256.New()

	size, err := io.Copy(hash, f)
	if err != nil {
		h.log.Errorw("failed to hash temp file", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	computedDigestHex := hex.EncodeToString(hash.Sum(nil))
	computedDigest := "sha256:" + computedDigestHex

	// Verify digest matches
	if computedDigest != digest {
		h.log.Warnw("digest mismatch", "expected", digest, "computed", computedDigest)
		h.errorResponse(w, http.StatusBadRequest, "DIGEST_INVALID", "computed digest does not match")
		// Clean up temp file
		//nolint:gosec // G703: path is constructed from validated upload UUID, not user input
		_ = os.Remove(tempFile)
		_ = h.db.DeleteDockerUpload(r.Context(), repo.ID, uploadUUID)

		return
	}

	// Rewind and stream the verified blob into storage using the same handle,
	// avoiding a second open (and the fd it would leak alongside this defer).
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		h.log.Errorw("failed to rewind temp file for storage", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	blob := &models.DockerBlob{
		ID:           uuid.New().String(),
		RepositoryID: repo.ID,
		Digest:       digest,
		MediaType:    "application/octet-stream",
		Size:         size,
		CreatedAt:    time.Now(),
	}

	// The request body is already spooled and verified, so the per-blob lock is
	// only held for the store + insert: a concurrent unpublish cannot delete the
	// blob in between. Content-addressed by sha256: every BlobStore backend keys
	// (and some, like S3/Azure/OCI, require) storage on the "sha256" metadata
	// entry — not the OCI-prefixed "digest" string.
	if err := storage.WithBlobLock(r.Context(), h.db, computedDigestHex, func(ctx context.Context) error {
		blobSHA256, err := h.blobStore.Put(ctx, f, map[string]string{
			"sha256": computedDigestHex,
			"digest": digest,
		})
		if err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		blob.BlobSHA256 = blobSHA256

		if err := h.db.CreateDockerBlob(ctx, blob); err != nil {
			return fmt.Errorf("create blob record: %w", err)
		}

		return nil
	}); err != nil {
		h.log.Errorw("failed to store blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Clean up temp file and upload record
	//nolint:gosec // G703: path is constructed from validated upload UUID, not user input
	_ = os.Remove(tempFile)
	_ = h.db.DeleteDockerUpload(r.Context(), repo.ID, uploadUUID)

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// handleCancelBlobUpload cancels an in-progress upload.
func (h *HTTPHandler) handleCancelBlobUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	upload, err := h.db.GetDockerUpload(r.Context(), repo.ID, uploadUUID)
	if err != nil {
		h.log.Errorw("failed to get upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if upload == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}

	// Delete temp file
	uploadDir := "/tmp/openartifact/docker-uploads"
	tempFile := filepath.Join(uploadDir, uploadUUID)
	//nolint:gosec // G703: path is constructed from validated upload UUID, not user input
	_ = os.Remove(tempFile)

	// Delete upload record
	if err := h.db.DeleteDockerUpload(r.Context(), repo.ID, uploadUUID); err != nil {
		h.log.Errorw("failed to delete upload record", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListTags lists tags in a repository.
func (h *HTTPHandler) handleListTags(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	// Parse pagination parameters
	nStr := r.URL.Query().Get("n")
	lastStr := r.URL.Query().Get("last")

	n := 100 // default

	if nStr != "" {
		if parsed, err := strconv.Atoi(nStr); err == nil && parsed > 0 {
			n = parsed
		}
	}

	tags, err := h.db.ListDockerTags(r.Context(), repo.ID, lastStr, n)
	if err != nil {
		h.log.Errorw("failed to list tags", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Build tag names list
	tagNames := make([]string, 0)
	for _, tag := range tags {
		tagNames = append(tagNames, tag.Name)
	}

	resp := map[string]any{
		"name": name,
		"tags": tagNames,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(resp); encErr != nil {
		_ = encErr // response header already written
	}
}

// handleGetReferrers returns the OCI referrers list for a digest: every
// manifest in the repository whose `subject` field points at it.
func (h *HTTPHandler) handleGetReferrers(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	digest := vars["digest"]

	if !strings.HasPrefix(digest, "sha256:") {
		h.errorResponse(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest syntax")
		return
	}

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	referrers, err := h.db.ListDockerManifestsBySubject(r.Context(), repo.ID, digest)
	if err != nil {
		h.log.Errorw("failed to list referrers", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	manifests := make([]map[string]any, 0, len(referrers))

	for _, m := range referrers {
		entry := map[string]any{
			"mediaType": m.MediaType,
			"digest":    m.Digest,
			"size":      m.Size,
		}

		// artifactType/annotations require reading the manifest content
		// itself; a store error here shouldn't fail the whole listing.
		content, getErr := h.blobStore.Get(r.Context(), manifestBlobKey(m.Digest))
		if getErr != nil {
			h.log.Warnw("failed to load referrer content", "digest", m.Digest, "error", getErr)
			manifests = append(manifests, entry)

			continue
		}

		manifestBody, readErr := io.ReadAll(content)
		_ = content.Close()

		if readErr == nil {
			meta := parseOCIManifestMeta(manifestBody)

			artifactType := meta.ArtifactType
			if artifactType == "" && meta.Config != nil {
				artifactType = meta.Config.MediaType
			}

			if artifactType != "" {
				entry["artifactType"] = artifactType
			}

			if len(meta.Annotations) > 0 {
				entry["annotations"] = meta.Annotations
			}
		}

		manifests = append(manifests, entry)
	}

	resp := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     manifests,
	}

	w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(resp); encErr != nil {
		_ = encErr // response header already written
	}
}
