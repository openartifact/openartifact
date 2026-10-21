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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/ocipath"
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
	authz     Authorizer
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{log: log, db: db, blobStore: blobStore, baseURL: baseURL}
}

// SetPublisher sets the webhook publisher used for artifact events. It must be
// called before the handler serves requests.
func (h *HTTPHandler) SetPublisher(p webhook.Publisher) { h.publisher = p }

// Authorizer decides whether a principal may perform an action on a
// repository. The handler needs it for the one operation that reads a second
// repository: a cross-repository blob mount.
type Authorizer interface {
	Evaluate(ctx context.Context, principal auth.Principal, repoName, artifactPath, action string) (bool, error)
}

// SetAuthorizer sets the authorizer used to check read access to a mount
// source repository. Without one, cross-repository mounts are declined and the
// client falls back to a regular upload. It must be called before the handler
// serves requests.
func (h *HTTPHandler) SetAuthorizer(a Authorizer) { h.authz = a }

// RegisterRoutes mounts the registry. Distribution names contain "/", which
// chi's single-segment params cannot capture, so one wildcard route hands the
// path to ocipath — the same parser RBAC uses to pick the repository.
func (h *HTTPHandler) RegisterRoutes(r chi.Router) {
	r.Get("/v2/", h.handleVersionCheck)
	r.HandleFunc("/v2/*", h.serveV2)
}

// route is one registry endpoint: the methods it serves, and the URL
// parameter its trailing reference binds to.
type route struct {
	refParam string
	methods  map[string]http.HandlerFunc
}

func (h *HTTPHandler) routes(kind ocipath.Kind) (route, bool) {
	switch kind {
	case ocipath.KindManifest:
		return route{"reference", map[string]http.HandlerFunc{
			http.MethodGet:    h.handleGetManifest,
			http.MethodHead:   h.handleHeadManifest,
			http.MethodPut:    h.handlePutManifest,
			http.MethodDelete: h.handleDeleteManifest,
		}}, true
	case ocipath.KindBlob:
		return route{"digest", map[string]http.HandlerFunc{
			http.MethodGet:    h.handleGetBlob,
			http.MethodHead:   h.handleHeadBlob,
			http.MethodDelete: h.handleDeleteBlob,
		}}, true
	case ocipath.KindUploadStart:
		return route{"", map[string]http.HandlerFunc{
			http.MethodPost: h.handleInitiateBlobUpload,
		}}, true
	case ocipath.KindUpload:
		return route{"uuid", map[string]http.HandlerFunc{
			http.MethodGet:    h.handleGetUploadStatus,
			http.MethodPatch:  h.handleUploadChunk,
			http.MethodPut:    h.handleCompleteBlobUpload,
			http.MethodDelete: h.handleCancelBlobUpload,
		}}, true
	case ocipath.KindTags:
		return route{"", map[string]http.HandlerFunc{
			http.MethodGet: h.handleListTags,
		}}, true
	case ocipath.KindReferrers:
		return route{"digest", map[string]http.HandlerFunc{
			http.MethodGet: h.handleGetReferrers,
		}}, true
	case ocipath.KindUnknown:
	}

	return route{}, false
}

// serveV2 dispatches /v2/<name>/<resource> requests, binding the parsed
// distribution name and reference as URL params for the handlers.
func (h *HTTPHandler) serveV2(w http.ResponseWriter, r *http.Request) {
	req := ocipath.Parse(strings.TrimPrefix(r.URL.Path, "/v2/"))

	rt, ok := h.routes(req.Kind)
	if !ok {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown registry endpoint")
		return
	}

	handler, ok := rt.methods[r.Method]
	if !ok {
		allowed := make([]string, 0, len(rt.methods))
		for m := range rt.methods {
			allowed = append(allowed, m)
		}

		slices.Sort(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		h.errorResponse(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")

		return
	}

	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		rctx.URLParams.Add("name", req.Name)

		if rt.refParam != "" {
			rctx.URLParams.Add(rt.refParam, req.Ref)
		}
	}

	handler(w, r)
}

// resolveRepo looks up a repository by name.
func (h *HTTPHandler) resolveRepo(ctx context.Context, name string) (*models.Repository, error) {
	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		return nil, err
	}

	return repo, nil
}

// target resolves a distribution name ("repo/org/team/app") to the
// OpenArtifact repository (first segment) and the image inside it (the rest;
// empty for the repository's default image). On failure it has already written
// the registry error response and returns ok=false.
func (h *HTTPHandler) target(w http.ResponseWriter, r *http.Request, name string) (*models.Repository, string, bool) {
	repoName, image := ocipath.SplitName(name)

	if !ocipath.ValidImage(image) {
		h.errorResponse(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return nil, "", false
	}

	repo, err := h.resolveRepo(r.Context(), repoName)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return nil, "", false
	}

	if repo == nil {
		h.errorResponse(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return nil, "", false
	}

	return repo, image, true
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

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	var (
		manifest *models.DockerManifest
		digest   string
		err      error
	)

	// If reference is a digest (sha256:...), look up directly
	if strings.HasPrefix(reference, "sha256:") {
		digest = reference

		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, image, digest)
		if err != nil {
			h.log.Errorw("failed to get manifest by digest", "digest", digest, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	} else {
		// Otherwise, treat as a tag name
		tag, err := h.db.GetDockerTag(r.Context(), repo.ID, image, reference)
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

		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, image, digest)
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

// handlePutManifest pushes a manifest. Pushing a manifest the image already
// holds succeeds, and pushing it under a tag moves that tag to it.
func (h *HTTPHandler) handlePutManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	reference := vars["reference"]

	repo, image, ok := h.target(w, r, name)
	if !ok {
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
		Image:         image,
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
	// the blob in between. Both the store and the insert are idempotent, so a
	// re-push of the same manifest is a refresh.
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
			Image:          image,
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

	repoName, _ := ocipath.SplitName(name)

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "docker",
		Name:       name,
		Version:    reference,
		Path:       name + "/manifests/" + reference,
		SHA256:     digestHex,
		Size:       int64(len(body)),
	}))

	w.Header().Set("Location", "/v2/"+name+"/manifests/"+digest)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// handleDeleteManifest deletes a tag or a manifest. Deleting by tag removes
// only that tag; the manifest stays reachable by digest, as other tags may
// still point at it. Deleting by digest removes the manifest and every tag of
// the image that pointed at it, so no tag is left dangling.
func (h *HTTPHandler) handleDeleteManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	reference := vars["reference"]

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	repoName, _ := ocipath.SplitName(name)

	if !strings.HasPrefix(reference, "sha256:") {
		h.deleteTag(w, r, repo, image, name, reference)
		return
	}

	deleted, err := h.db.GetDockerManifest(r.Context(), repo.ID, image, reference)
	if err != nil {
		h.log.Errorw("failed to get manifest before delete", "digest", reference, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if deleted == nil {
		h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found")
		return
	}

	if err := h.db.DeleteDockerTagsByDigest(r.Context(), repo.ID, image, reference); err != nil {
		h.log.Errorw("failed to delete tags of manifest", "digest", reference, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Delete the manifest row first so BlobReferenceCount no longer sees it.
	if err := h.db.DeleteDockerManifest(r.Context(), repo.ID, image, reference); err != nil {
		h.log.Errorw("failed to delete manifest", "digest", reference, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Reclaim the manifest's content blob from storage only if no other
	// docker_manifests row (or any other format table) still references it.
	if err := storage.SafeDeleteBlob(r.Context(), h.db, h.blobStore, manifestBlobKey(reference)); err != nil {
		h.log.Errorw("failed to delete manifest blob from store", "digest", reference, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "docker",
		Name:       name,
		Version:    reference,
		Path:       name + "/manifests/" + reference,
		SHA256:     manifestBlobKey(reference),
		Size:       deleted.Size,
	}))

	w.WriteHeader(http.StatusAccepted)
}

// deleteTag removes one tag of an image, leaving its manifest in place.
func (h *HTTPHandler) deleteTag(w http.ResponseWriter, r *http.Request, repo *models.Repository, image, name, tagName string) {
	tag, err := h.db.GetDockerTag(r.Context(), repo.ID, image, tagName)
	if err != nil {
		h.log.Errorw("failed to get tag", "tag", tagName, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if tag == nil {
		h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "tag not found")
		return
	}

	if err := h.db.DeleteDockerTag(r.Context(), repo.ID, image, tagName); err != nil {
		h.log.Errorw("failed to delete tag", "tag", tagName, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	repoName, _ := ocipath.SplitName(name)

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "docker",
		Name:       name,
		Version:    tagName,
		Path:       name + "/manifests/" + tagName,
		SHA256:     manifestBlobKey(tag.ManifestDigest),
	}))

	w.WriteHeader(http.StatusAccepted)
}

// handleHeadManifest checks if a manifest exists.
func (h *HTTPHandler) handleHeadManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	reference := vars["reference"]

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	var (
		manifest *models.DockerManifest
		err      error
	)

	// If reference is a digest, look up directly
	if strings.HasPrefix(reference, "sha256:") {
		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, image, reference)
		if err != nil {
			h.log.Errorw("failed to get manifest by digest", "digest", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	} else {
		// Otherwise, resolve tag to digest
		tag, err := h.db.GetDockerTag(r.Context(), repo.ID, image, reference)
		if err != nil {
			h.log.Errorw("failed to get tag", "tag", reference, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}

		if tag == nil {
			h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "tag not found")
			return
		}

		manifest, err = h.db.GetDockerManifest(r.Context(), repo.ID, image, tag.ManifestDigest)
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

	repo, _, ok := h.target(w, r, name)
	if !ok {
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

	repo, _, ok := h.target(w, r, name)
	if !ok {
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

	repo, _, ok := h.target(w, r, name)
	if !ok {
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

	repo, _, ok := h.target(w, r, name)
	if !ok {
		return
	}

	if h.tryMountBlob(w, r, repo, name) {
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

// tryMountBlob serves `POST …/blobs/uploads/?mount=<digest>&from=<name>`. When
// the blob is already in the target repository, or can be linked from a source
// repository the caller may read, it answers 201 and returns true. Otherwise it
// returns false without writing anything and the caller opens a regular upload
// session — the spec's fallback, which also avoids revealing whether a blob the
// caller cannot read exists elsewhere.
func (h *HTTPHandler) tryMountBlob(w http.ResponseWriter, r *http.Request, repo *models.Repository, name string) bool {
	q := r.URL.Query()
	digest, from := q.Get("mount"), q.Get("from")

	if digest == "" {
		return false
	}

	ctx := r.Context()

	existing, err := h.db.GetDockerBlob(ctx, repo.ID, digest)
	if err != nil {
		h.log.Errorw("failed to look up mount target blob", "digest", digest, "error", err)
		return false
	}

	if existing == nil {
		src := h.mountSource(r, repo, from, digest)
		if src == nil {
			return false
		}

		if !h.linkBlob(ctx, repo.ID, digest, src) {
			return false
		}
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)

	return true
}

// mountSource returns the blob digest in the `from` repository when the caller
// may read it and it is not quarantined, or nil.
func (h *HTTPHandler) mountSource(r *http.Request, target *models.Repository, from, digest string) *models.DockerBlob {
	if from == "" || h.authz == nil {
		return nil
	}

	ctx := r.Context()

	srcRepoName, srcImage := ocipath.SplitName(from)
	if !ocipath.ValidImage(srcImage) {
		return nil
	}

	srcRepo, err := h.resolveRepo(ctx, srcRepoName)
	if err != nil || srcRepo == nil || srcRepo.ID == target.ID {
		return nil
	}

	principal := middleware.PrincipalFromContext(ctx)
	if principal == nil {
		return nil
	}

	path := "blobs/" + digest
	if srcImage != "" {
		path = srcImage + "/" + path
	}

	if allowed, authErr := h.authz.Evaluate(ctx, *principal, srcRepo.Name, path, "read"); authErr != nil || !allowed {
		return nil
	}

	blob, err := h.db.GetDockerBlob(ctx, srcRepo.ID, digest)
	if err != nil || blob == nil {
		return nil
	}

	quarantined, err := quarantine.IsQuarantined(ctx, h.db, srcRepo.ID, quarantine.PathKeys(blob.ID, digest, from+"/blobs/"+digest)...)
	if err != nil || quarantined {
		return nil
	}

	return blob
}

// linkBlob adds a docker_blobs row for the target repository pointing at the
// same content-addressed storage blob as src. It reports whether the link
// exists afterwards.
func (h *HTTPHandler) linkBlob(ctx context.Context, repoID, digest string, src *models.DockerBlob) bool {
	err := storage.WithBlobLock(ctx, h.db, src.BlobSHA256, func(ctx context.Context) error {
		return h.db.CreateDockerBlob(ctx, &models.DockerBlob{
			ID:           uuid.New().String(),
			RepositoryID: repoID,
			Digest:       digest,
			MediaType:    src.MediaType,
			Size:         src.Size,
			BlobSHA256:   src.BlobSHA256,
			CreatedAt:    time.Now(),
		})
	})
	if err != nil {
		h.log.Errorw("failed to mount blob", "digest", digest, "error", err)
		return false
	}

	return true
}

// handleGetUploadStatus gets the status of an in-progress upload.
func (h *HTTPHandler) handleGetUploadStatus(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]

	repo, _, ok := h.target(w, r, name)
	if !ok {
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

	repo, _, ok := h.target(w, r, name)
	if !ok {
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

	repo, _, ok := h.target(w, r, name)
	if !ok {
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
	// only held for the existence check + store + insert: a concurrent unpublish cannot delete the
	// blob in between. Content-addressed by sha256: every BlobStore backend keys
	// (and some, like S3/Azure/OCI, require) storage on the "sha256" metadata
	// entry — not the OCI-prefixed "digest" string.
	if err := storage.WithBlobLock(r.Context(), h.db, computedDigestHex, func(ctx context.Context) error {
		// A blob the repository already holds is not stored again: pushing an
		// existing digest completes with 201, as the distribution spec requires.
		existing, err := h.db.GetDockerBlob(ctx, repo.ID, digest)
		if err != nil {
			return fmt.Errorf("look up existing blob: %w", err)
		}

		if existing != nil {
			return nil
		}

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

	repo, _, ok := h.target(w, r, name)
	if !ok {
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

	repo, image, ok := h.target(w, r, name)
	if !ok {
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

	tags, err := h.db.ListDockerTags(r.Context(), repo.ID, image, lastStr, n)
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

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	referrers, err := h.db.ListDockerManifestsBySubject(r.Context(), repo.ID, image, digest)
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
