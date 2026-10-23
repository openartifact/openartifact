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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/ocimanifest"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// errMountSourceGone means the blob to mount was removed from the source
// repository after it was looked up.
var errMountSourceGone = errors.New("mount source blob no longer exists")

type HTTPHandler struct {
	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
	baseURL   string
	publisher webhook.Publisher
	authz     Authorizer
	uploadDir string
	uploads   uploadLocks
}

// DefaultUploadDir is where in-progress blob uploads are staged unless
// SetUploadDir overrides it.
const DefaultUploadDir = "/tmp/openartifact/docker-uploads"

const (
	// maxManifestBytes caps a pushed manifest; the distribution spec
	// recommends registries accept at least 4 MiB.
	maxManifestBytes = 4 << 20

	// defaultTagPage is the tags/list page size when `n` is absent; maxTagPage
	// caps an explicit `n`.
	defaultTagPage = 100
	maxTagPage     = 1000
)

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{log: log, db: db, blobStore: blobStore, baseURL: baseURL, uploadDir: DefaultUploadDir}
}

// SetUploadDir sets the directory blob uploads are staged in. It must be
// called before the handler serves requests.
func (h *HTTPHandler) SetUploadDir(dir string) {
	if dir != "" {
		h.uploadDir = dir
	}
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
	r.Get("/v2/_catalog", h.handleCatalog)
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

// isHostedDocker reports whether repo is a hosted docker repository, the only
// kind the registry handler reads from or writes to.
func isHostedDocker(repo *models.Repository) bool {
	return repo != nil && repo.Format == "docker" && (repo.RepoType == "" || repo.RepoType == repotype.Hosted)
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

	if !isHostedDocker(repo) {
		// Other formats and proxy/group repos are never writable (or even
		// served) through the hosted registry handler.
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

// quotaExceeded answers a push the repository's storage quota has no room for:
// 507 in the registry error envelope (code DENIED), so docker, podman and
// skopeo print the message.
func (h *HTTPHandler) quotaExceeded(w http.ResponseWriter) {
	h.errorResponse(w, http.StatusInsufficientStorage, "DENIED", quota.Message)
}

// Docker usage of a repository is the bytes of every layer/config blob it holds
// (one docker_blobs row each, 0 artifacts) plus the bytes of every manifest
// (one docker_manifests row each, 1 artifact), the same rows the reconcile in
// internal/database counts. Tags and in-progress upload sessions are not
// metered. A reservation is made only for a row a request is about to create,
// before anything is stored.

// releaseQuota returns a reservation, or the usage of a row that was removed:
// size bytes and count artifacts (1 for a manifest, 0 for a blob).
func (h *HTTPHandler) releaseQuota(ctx context.Context, repoID string, size int64, count int) {
	if err := quota.ReleaseN(ctx, h.db, repoID, size, count); err != nil {
		h.log.Warnw("failed to release quota", "repository_id", repoID, "error", err)
	}
}

// settleQuota reconciles a reservation with what the insert did, for the rare
// race where another request changed the row between the existence check and
// the insert: a row that turned out not to be new gives its reservation back,
// and a row that is new although none was reserved is charged now (the push
// has already succeeded, so a full quota cannot refuse it; the periodic
// reconcile corrects any excess).
func (h *HTTPHandler) settleQuota(ctx context.Context, repoID string, size int64, count int, reserved, inserted bool) {
	switch {
	case reserved && !inserted:
		h.releaseQuota(ctx, repoID, size, count)
	case !reserved && inserted:
		if err := quota.EnforceN(ctx, h.db, repoID, size, count); err != nil {
			h.log.Warnw("quota not charged for a re-created row; reconcile will repair it", "repository_id", repoID, "error", err)
		}
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

	w.Header().Set("Content-Type", ocipath.ServedMediaType(manifest.MediaType))
	w.Header().Set("Docker-Content-Digest", manifest.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(manifest.Size, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, content); err != nil {
		h.log.Errorw("failed to write manifest response", "digest", manifest.Digest, "error", err)
	}
}

// blobsExist verifies every blob a pushed manifest references is stored in the
// repository, so a manifest never lands pointing at a blob that registry GC
// already removed. It writes the error response and returns false otherwise.
func (h *HTTPHandler) blobsExist(w http.ResponseWriter, r *http.Request, repoID, image string, digests []string) bool {
	for _, d := range digests {
		blob, err := h.getVisibleBlob(r.Context(), repoID, image, d)
		if err != nil {
			h.log.Errorw("failed to look up referenced blob", "digest", d, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return false
		}

		if blob == nil {
			h.errorResponse(w, http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN", "manifest references a blob that is not in the image: "+d)

			return false
		}
	}

	return true
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManifestBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.errorResponse(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "manifest too large")

			return
		}

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

	if licensepolicy.Enforce(w, r, h.db, h.log, repo.ID, h.declaredLicenses(r.Context(), repo.ID, meta)...) {
		return
	}

	if !strings.HasPrefix(reference, "sha256:") && !h.tagMoveAllowed(w, r, repo, image, reference, digest) {
		return
	}

	// The blobs and child manifests this body references are stored with the
	// manifest so registry GC can tell what is still in use.
	refs := ocimanifest.ParseRefs(body)

	pushedBlobs := ocimanifest.PushedBlobs(body)

	if !h.blobsExist(w, r, repo.ID, image, pushedBlobs) {
		return
	}

	reserved, ok := h.reserveManifest(w, r, repo, image, digest, int64(len(body)))
	if !ok {
		return
	}

	manifest := &models.DockerManifest{
		ID:            uuid.New().String(),
		RepositoryID:  repo.ID,
		Image:         image,
		Digest:        digest,
		MediaType:     contentType,
		Size:          int64(len(body)),
		SubjectDigest: subjectDigest,
		Refs:          &refs,
		RequiredBlobs: pushedBlobs,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := h.storeManifest(r.Context(), manifest, digestHex, contentType, body); err != nil {
		if reserved {
			h.releaseQuota(r.Context(), repo.ID, manifest.Size, 1)
		}

		if errors.Is(err, database.ErrDockerBlobsMissing) {
			h.errorResponse(w, http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN", "manifest references a blob that is not in the image")
			return
		}

		h.log.Errorw("failed to store manifest", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	h.settleQuota(r.Context(), repo.ID, manifest.Size, 1, reserved, manifest.Inserted)

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

// storeManifest persists the manifest content in the blob store
// (content-addressed by sha256), ensures a row exists for it in the global
// blobs table (the docker_manifests row has a NOT NULL blob_id FK) and inserts
// the manifest, all under the per-blob lock so a concurrent unpublish cannot
// delete the blob in between. Both the store and the insert are idempotent, so
// a re-push of the same manifest is a refresh.
func (h *HTTPHandler) storeManifest(ctx context.Context, manifest *models.DockerManifest, digestHex, contentType string, body []byte) error {
	return storage.WithBlobLock(ctx, h.db, digestHex, func(ctx context.Context) error {
		if _, err := h.blobStore.Put(ctx, bytes.NewReader(body), map[string]string{
			"sha256":       digestHex,
			"content-type": contentType,
		}); err != nil {
			return fmt.Errorf("store manifest blob: %w", err)
		}

		// The blobs row is keyed by the sha256 computed above from the pushed
		// bytes, never by a value the store or client reports.
		blobID, err := h.db.EnsureBlob(ctx, digestHex, contentType, int64(len(body)))
		if err != nil {
			return fmt.Errorf("ensure manifest blob row: %w", err)
		}

		manifest.BlobID = blobID

		if err := h.db.CreateDockerManifest(ctx, manifest); err != nil {
			return fmt.Errorf("create manifest: %w", err)
		}

		return nil
	})
}

// reserveManifest reserves quota for a manifest the image does not hold yet
// (size bytes, one artifact). Re-pushing a manifest the image already holds
// (an idempotent refresh, or a tag move) reserves nothing; reserved is false
// then (the first result). When the second result is false the response has been written: 507 when the quota has
// no room, 500 otherwise.
func (h *HTTPHandler) reserveManifest(w http.ResponseWriter, r *http.Request, repo *models.Repository, image, digest string, size int64) (bool, bool) {
	existing, err := h.db.GetDockerManifest(r.Context(), repo.ID, image, digest)
	if err != nil {
		h.log.Errorw("failed to look up manifest before push", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return false, false
	}

	if existing != nil {
		return false, true
	}

	if err := quota.EnforceN(r.Context(), h.db, repo.ID, size, 1); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			h.quotaExceeded(w)
			return false, false
		}

		h.log.Errorw("failed to reserve quota", "repo", repo.Name, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return false, false
	}

	return true, true
}

// tagMoveAllowed reports whether pushing digest under tagName may proceed. On
// an immutable-releases repository an existing tag cannot be re-pointed to a
// different digest (every docker tag is a release) unless an admin forces it;
// re-pushing the same digest is idempotent and always allowed. When it returns
// false the error response has been written.
func (h *HTTPHandler) tagMoveAllowed(w http.ResponseWriter, r *http.Request, repo *models.Repository, image, tagName, digest string) bool {
	if !repo.ImmutableReleases {
		return true
	}

	existing, err := h.db.GetDockerTag(r.Context(), repo.ID, image, tagName)
	if err != nil {
		h.log.Errorw("failed to get tag before push", "tag", tagName, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return false
	}

	if existing == nil || existing.ManifestDigest == digest {
		return true
	}

	if err := trash.GuardRelease(r, repo, "docker", tagName); err != nil {
		h.deleteError(w, "tag", tagName, err)
		return false
	}

	return true
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

	if err := trash.GuardRelease(r, repo, "docker", reference); err != nil {
		h.deleteError(w, "manifest", reference, err)
		return
	}

	// Soft delete trashes the manifest together with every tag of the image
	// that pointed at it (one transaction in TrashRows); the manifest row keeps
	// its blob and layer references until purge. The permanent path below runs
	// for repositories with immediate deletion.
	err = trash.Delete(r.Context(), h.db, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindDockerManifest,
		IDs:      []string{deleted.ID},
		Resource: name + "@" + reference,
	}, func() error { return h.hardDeleteManifest(r.Context(), repo.ID, image, reference, deleted.Size) })
	if err != nil {
		h.deleteError(w, "manifest", reference, err)
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

// hardDeleteManifest permanently removes a manifest and the tags pointing at
// it, then its content blob when nothing else references it. Layer blobs are
// never touched here; registry GC reclaims them. The manifest's quota usage
// (size bytes, one artifact) is released only when this call removed the row;
// a soft delete keeps it until the trash purge.
func (h *HTTPHandler) hardDeleteManifest(ctx context.Context, repoID, image, digest string, size int64) error {
	if err := h.db.DeleteDockerTagsByDigest(ctx, repoID, image, digest); err != nil {
		return fmt.Errorf("delete tags of manifest: %w", err)
	}

	// Delete the manifest row first so BlobReferenceCount no longer sees it.
	if err := h.db.DeleteDockerManifest(ctx, repoID, image, digest); err != nil {
		return fmt.Errorf("delete manifest: %w", err)
	}

	h.releaseQuota(ctx, repoID, size, 1)

	// Reclaim the manifest's content blob from storage only if no other
	// docker_manifests row (a trashed copy included) or any other format table
	// still references it.
	if err := storage.SafeDeleteBlob(ctx, h.db, h.blobStore, manifestBlobKey(digest)); err != nil {
		return fmt.Errorf("delete manifest blob from store: %w", err)
	}

	return nil
}

// deleteError answers a failed tag/manifest delete: 404 when the row is no
// longer live, 409 when the repository's immutable_releases forbids it.
func (h *HTTPHandler) deleteError(w http.ResponseWriter, what, reference string, err error) {
	switch {
	case errors.Is(err, database.ErrTrashNotFound), errors.Is(err, database.ErrDockerRowNotFound):
		h.errorResponse(w, http.StatusNotFound, "MANIFEST_UNKNOWN", what+" not found")
	case errors.Is(err, trash.ErrImmutableRelease):
		h.errorResponse(w, http.StatusConflict, "DENIED", "released versions are immutable in this repository")
	default:
		h.log.Errorw("failed to delete "+what, what, reference, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
	}
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

	if err := trash.GuardRelease(r, repo, "docker", tagName); err != nil {
		h.deleteError(w, "tag", tagName, err)
		return
	}

	err = trash.Delete(r.Context(), h.db, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindDockerTag,
		IDs:      []string{tag.ID},
		Resource: name + ":" + tagName,
	}, func() error {
		if err := h.db.DeleteDockerTag(r.Context(), repo.ID, image, tagName); err != nil {
			return fmt.Errorf("delete tag: %w", err)
		}

		return nil
	})
	if err != nil {
		h.deleteError(w, "tag", tagName, err)
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

	w.Header().Set("Content-Type", ocipath.ServedMediaType(manifest.MediaType))
	w.Header().Set("Docker-Content-Digest", manifest.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(manifest.Size, 10))
	w.WriteHeader(http.StatusOK)
}

// handleGetBlob downloads a blob.
func (h *HTTPHandler) handleGetBlob(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	digest := vars["digest"]

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	blob, ok := h.servableBlob(w, r, repo.ID, image, digest)
	if !ok {
		return
	}

	size, err := h.blobSize(r.Context(), blob)
	if err != nil {
		h.log.Errorw("failed to size blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if quarantine.Enforce(w, r, h.db, repo.ID, quarantine.PathKeys(
		blob.ID, digest, name+"/blobs/"+digest, r.URL.Path)...) {
		return
	}

	// Layers and configs are the bulk of a pull; the registry spec lets a
	// server answer a blob GET with a redirect, which every client follows.
	// Manifests (above) are never presigned.
	if presign.Try(h.blobStore, w, r, presign.Blob{SHA256: blob.BlobSHA256, Size: size, Repository: repo}) {
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

	w.Header().Set("Content-Type", blobContentType)
	w.Header().Set("Docker-Content-Digest", blob.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
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

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	blob, ok := h.servableBlob(w, r, repo.ID, image, digest)
	if !ok {
		return
	}

	size, err := h.blobSize(r.Context(), blob)
	if err != nil {
		h.log.Errorw("failed to size blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// A client that finds the blob here skips uploading it, so registry GC
	// must not remove it before the manifest that references it arrives.
	still, err := h.touchBlob(r.Context(), repo.ID, digest)
	if err != nil {
		h.log.Errorw("failed to touch blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if !still {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return
	}

	w.Header().Set("Content-Type", blobContentType)
	w.Header().Set("Docker-Content-Digest", blob.Digest)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
}

// blobAlreadyHeld reports whether the repository has the blob, marking it used
// so registry GC keeps it and linking it to the image: the caller has just
// proven it holds the bytes. A blob GC removed since the lookup counts as absent.
func (h *HTTPHandler) blobAlreadyHeld(ctx context.Context, repoID, image, digest string) (bool, error) {
	existing, err := h.db.GetDockerBlob(ctx, repoID, digest)
	if err != nil {
		return false, fmt.Errorf("look up existing blob: %w", err)
	}

	if existing == nil {
		return false, nil
	}

	still, err := h.touchBlob(ctx, repoID, digest)
	if err != nil {
		return false, fmt.Errorf("touch existing blob: %w", err)
	}

	if still {
		if err := h.linkBlobImage(ctx, repoID, image, digest); err != nil {
			return false, fmt.Errorf("link existing blob: %w", err)
		}
	}

	return still, nil
}

// touchBlob records a client's use of the blob for registry GC. It reports
// false when the repository no longer has the blob; databases that do not
// track blob use always report true.
func (h *HTTPHandler) touchBlob(ctx context.Context, repoID, digest string) (bool, error) {
	t, ok := h.db.(database.DockerBlobToucher)
	if !ok {
		return true, nil
	}

	return t.TouchDockerBlob(ctx, repoID, digest)
}

// handleDeleteBlob deletes a blob.
func (h *HTTPHandler) handleDeleteBlob(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	digest := vars["digest"]

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	blob, err := h.getVisibleBlob(r.Context(), repo.ID, image, digest)
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
		if errors.Is(err, database.ErrDockerRowNotFound) {
			h.errorResponse(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
			return
		}

		h.log.Errorw("failed to delete blob from db", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// The row is gone for good (blobs have no trash): its bytes are free again.
	h.releaseQuota(r.Context(), repo.ID, blob.Size, 0)

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

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	if h.tryMountBlob(w, r, repo, image, name) {
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
	uploadDir := h.uploadDir
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
func (h *HTTPHandler) tryMountBlob(w http.ResponseWriter, r *http.Request, repo *models.Repository, image, name string) bool {
	q := r.URL.Query()
	digest, from := q.Get("mount"), q.Get("from")

	if digest == "" {
		return false
	}

	ctx := r.Context()

	existing, err := h.getVisibleBlob(ctx, repo.ID, image, digest)
	if err != nil {
		h.log.Errorw("failed to look up mount target blob", "digest", digest, "error", err)
		return false
	}

	if existing != nil {
		// The client reuses the blob without uploading it: keep it from GC.
		if still, err := h.touchBlob(ctx, repo.ID, digest); err != nil || !still {
			existing = nil
		}
	}

	if existing == nil {
		src := h.mountSource(r, repo, image, from, digest)
		if src == nil {
			return false
		}

		if err := h.linkBlob(ctx, repo.ID, image, digest, src); err != nil {
			// A mount that does not fit would not fit as an upload either:
			// answer now rather than make the client send the layer first.
			if errors.Is(err, quota.ErrExceeded) {
				h.quotaExceeded(w)
				return true
			}

			if !errors.Is(err, errMountSourceGone) {
				h.log.Errorw("failed to mount blob", "digest", digest, "error", err)
			}

			return false
		}
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)

	return true
}

// mountSource returns the blob digest in the `from` image when the caller may
// read it and it is not quarantined, or nil. `from` may be another image of the
// target repository: that is how an image takes a blob it was not pushed.
func (h *HTTPHandler) mountSource(r *http.Request, target *models.Repository, image, from, digest string) *models.DockerBlob {
	if from == "" || h.authz == nil {
		return nil
	}

	ctx := r.Context()

	srcRepoName, srcImage := ocipath.SplitName(from)
	if !ocipath.ValidImage(srcImage) {
		return nil
	}

	srcRepo, err := h.resolveRepo(ctx, srcRepoName)
	if err != nil || !isHostedDocker(srcRepo) || (srcRepo.ID == target.ID && srcImage == image) {
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

	blob, err := h.getVisibleBlob(ctx, srcRepo.ID, srcImage, digest)
	if err != nil || blob == nil {
		return nil
	}

	// A layer only trashed manifests of the source hold is not served there,
	// so it is not handed to another repository either.
	if trashed, terr := h.blobInTrash(ctx, srcRepo.ID, digest); terr != nil || trashed {
		return nil
	}

	quarantined, err := quarantine.IsQuarantined(ctx, h.db, srcRepo.ID, quarantine.PathKeys(blob.ID, digest, from+"/blobs/"+digest)...)
	if err != nil || quarantined {
		return nil
	}

	return blob
}

// linkBlob makes src available to the target image: it adds a docker_blobs row
// for the target repository pointing at the same content-addressed storage
// blob (none for a source in the same repository) and links the image. It
// returns nil when the link exists afterwards, errMountSourceGone when the
// source vanished and quota.ErrExceeded when the new row does not fit the
// target repository's quota.
func (h *HTTPHandler) linkBlob(ctx context.Context, repoID, image, digest string, src *models.DockerBlob) error {
	return storage.WithBlobLock(ctx, h.db, src.BlobSHA256, func(ctx context.Context) error {
		// Registry GC may have removed the source record (and its storage) since
		// mountSource looked it up; linking then would point at a blob that is gone.
		still, err := h.db.GetDockerBlob(ctx, src.RepositoryID, digest)
		if err != nil {
			return fmt.Errorf("re-check mount source: %w", err)
		}

		if still == nil {
			return errMountSourceGone
		}

		if src.RepositoryID == repoID {
			return h.linkBlobImage(ctx, repoID, image, digest)
		}

		return h.createMountedBlob(ctx, repoID, image, digest, src)
	})
}

// createMountedBlob gives the target repository its own row for the source's
// blob. The repository may already hold the blob (linked to another image):
// that mount stores nothing new and reserves nothing; otherwise the row's bytes
// are reserved first and returned if the insert fails.
func (h *HTTPHandler) createMountedBlob(ctx context.Context, repoID, image, digest string, src *models.DockerBlob) error {
	held, err := h.db.GetDockerBlob(ctx, repoID, digest)
	if err != nil {
		return fmt.Errorf("look up mount target blob: %w", err)
	}

	reserved := held == nil
	if reserved {
		if err := quota.EnforceN(ctx, h.db, repoID, src.Size, 0); err != nil {
			return fmt.Errorf("reserve blob quota: %w", err)
		}
	}

	blob := &models.DockerBlob{
		ID:           uuid.New().String(),
		RepositoryID: repoID,
		Digest:       digest,
		MediaType:    src.MediaType,
		Size:         src.Size,
		BlobSHA256:   src.BlobSHA256,
		Image:        &image,
		CreatedAt:    time.Now(),
	}

	if err := h.db.CreateDockerBlob(ctx, blob); err != nil {
		if reserved {
			h.releaseQuota(ctx, repoID, src.Size, 0)
		}

		return fmt.Errorf("create mounted blob record: %w", err)
	}

	h.settleQuota(ctx, repoID, src.Size, 0, reserved, blob.Inserted)

	return nil
}

// lockUpload takes the upload session's lock, so requests on one session run
// one at a time. On failure (the request was cancelled while waiting) it has
// written the error response.
func (h *HTTPHandler) lockUpload(w http.ResponseWriter, r *http.Request, repoID, uploadUUID string) (func(), bool) {
	unlock, err := h.uploads.lock(r.Context(), repoID+"/"+uploadUUID)
	if err != nil {
		h.errorResponse(w, http.StatusServiceUnavailable, "UNKNOWN", "upload session is busy")
		return nil, false
	}

	return unlock, true
}

// blobVisible reports whether the image may serve and reference the blob the
// repository holds. Databases that do not track image links see every blob of
// the repository.
func (h *HTTPHandler) blobVisible(ctx context.Context, repoID, image, digest string) (bool, error) {
	v, ok := h.db.(database.DockerBlobImages)
	if !ok {
		return true, nil
	}

	return v.DockerBlobVisible(ctx, repoID, image, digest)
}

// linkBlobImage records that the image holds a blob the repository has.
func (h *HTTPHandler) linkBlobImage(ctx context.Context, repoID, image, digest string) error {
	v, ok := h.db.(database.DockerBlobImages)
	if !ok {
		return nil
	}

	return v.LinkDockerBlob(ctx, repoID, image, digest)
}

// getVisibleBlob returns the repository's blob only when the image may see it
// (nil otherwise), so a principal limited to one image by RBAC path_pattern
// cannot read or reference another image's blobs through its own image path.
func (h *HTTPHandler) getVisibleBlob(ctx context.Context, repoID, image, digest string) (*models.DockerBlob, error) {
	blob, err := h.db.GetDockerBlob(ctx, repoID, digest)
	if err != nil || blob == nil {
		return nil, err //nolint:wrapcheck // callers log and answer with their own error
	}

	visible, err := h.blobVisible(ctx, repoID, image, digest)
	if err != nil {
		return nil, fmt.Errorf("check blob image link: %w", err)
	}

	if !visible {
		return nil, nil //nolint:nilnil // not visible reads as absent
	}

	return blob, nil
}

// blobInTrash reports whether the repository's manifests reference the blob and
// all of them are in the trash. Databases that do not track trashed manifests
// never report it.
func (h *HTTPHandler) blobInTrash(ctx context.Context, repoID, digest string) (bool, error) {
	t, ok := h.db.(database.DockerBlobTrash)
	if !ok {
		return false, nil
	}

	return t.DockerBlobInTrash(ctx, repoID, digest)
}

// servableBlob is getVisibleBlob for the endpoints that hand a blob to a
// client (GET and HEAD by digest): a blob only trashed manifests reference is
// absent until one of them is restored, while a blob a live manifest references
// (a layer shared with another image) or no manifest references yet (an upload
// awaiting its manifest) is served. Uploads, mounts into an image and manifest
// validation keep using getVisibleBlob, so pushing an image that reuses a
// trashed layer still works: the layer is found missing, uploaded again, and
// served once the new manifest references it. On failure it has written the
// registry error response and returns ok=false.
func (h *HTTPHandler) servableBlob(w http.ResponseWriter, r *http.Request, repoID, image, digest string) (*models.DockerBlob, bool) {
	blob, err := h.getVisibleBlob(r.Context(), repoID, image, digest)
	if err != nil {
		h.log.Errorw("failed to get blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return nil, false
	}

	if blob == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return nil, false
	}

	trashed, err := h.blobInTrash(r.Context(), repoID, digest)
	if err != nil {
		h.log.Errorw("failed to check blob trash state", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return nil, false
	}

	if trashed {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return nil, false
	}

	return blob, true
}

// blobSize is the blob's byte length for Content-Length: the size verified at
// upload, or, for rows whose recorded size is missing or zero (the shared blobs
// row keeps the first writer's size, which some formats record as 0), the size
// of the stored object.
func (h *HTTPHandler) blobSize(ctx context.Context, blob *models.DockerBlob) (int64, error) {
	if blob.Size > 0 {
		return blob.Size, nil
	}

	size, err := h.blobStore.GetSize(ctx, blob.BlobSHA256)
	if err != nil {
		return 0, fmt.Errorf("stat stored blob: %w", err)
	}

	return size, nil
}

// blobContentType is what every registry blob is served as; the layer or
// config media type lives in the manifest's descriptor, and the shared blobs
// row's media_type belongs to whichever format stored the bytes first.
const blobContentType = "application/octet-stream"

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
	w.Header().Set("Range", uploadRange(upload.Offset))
	w.Header().Set("Docker-Upload-Uuid", uploadUUID)
	w.WriteHeader(http.StatusNoContent)
}

// uploadRange formats the Range header for an upload holding offset bytes. An
// empty upload reports 0-0, as the initiate response does.
func uploadRange(offset int64) string {
	if offset <= 0 {
		return "0-0"
	}

	return fmt.Sprintf("0-%d", offset-1)
}

// openUploadFile opens the staging file for appending after truncating it to
// the offset the database accepted. Bytes a previous, failed request wrote
// past that offset are discarded, so a client resuming at the reported offset
// never appends after stray data.
func openUploadFile(path string, offset int64) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open upload file: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("stat upload file: %w", err)
	}

	if info.Size() < offset {
		_ = f.Close()

		return nil, errUploadFileShort
	}

	if info.Size() > offset {
		if err := f.Truncate(offset); err != nil {
			_ = f.Close()

			return nil, fmt.Errorf("truncate upload file: %w", err)
		}
	}

	return f, nil
}

// errUploadFileShort means the staging file holds fewer bytes than the
// database recorded (it was lost), so the upload cannot continue.
var errUploadFileShort = errors.New("upload file is shorter than the recorded offset")

// handleUploadChunk appends data to an in-progress upload.
func (h *HTTPHandler) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]

	repo, _, ok := h.target(w, r, name)
	if !ok {
		return
	}

	// One request at a time per session, and the offset is read under the
	// lock: the bytes this chunk appends start exactly where the previous
	// request on the session ended.
	unlock, ok := h.lockUpload(w, r, repo.ID, uploadUUID)
	if !ok {
		return
	}
	defer unlock()

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
			w.Header().Set("Range", uploadRange(upload.Offset))
			h.errorResponse(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", offsetErr.Error())

			return
		}
	}

	defer func() { _ = r.Body.Close() }()

	// Stream chunk data straight to the temp file to avoid buffering the whole
	// chunk (up to GBs for large layers) in memory.
	uploadDir := h.uploadDir
	tempFile := filepath.Join(uploadDir, uploadUUID)

	f, err := openUploadFile(tempFile, upload.Offset)
	if err != nil {
		h.uploadFileError(w, uploadUUID, err)

		return
	}

	body, room := h.quotaBoundedBody(w, r, repo.ID, upload.Offset, "")
	if !room {
		_ = f.Close()
		h.refuseOverQuota(w, r, repo.ID, uploadUUID, tempFile)

		return
	}

	written, err := io.Copy(f, body)
	if err != nil {
		_ = f.Close()

		if _, over := errors.AsType[*http.MaxBytesError](err); over {
			h.refuseOverQuota(w, r, repo.ID, uploadUUID, tempFile)
			return
		}

		h.log.Errorw("failed to write chunk", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if err := f.Close(); err != nil {
		h.log.Errorw("failed to close temp file", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	// Advance the offset only if it is still where this chunk started; the
	// bytes written past it are truncated by whichever request opens the file next.
	newOffset := upload.Offset + written

	moved, err := h.db.UpdateDockerUploadOffset(r.Context(), repo.ID, uploadUUID, upload.Offset, newOffset)
	if err != nil {
		h.log.Errorw("failed to update upload offset", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if !moved {
		h.errorResponse(w, http.StatusConflict, "BLOB_UPLOAD_INVALID", "upload changed concurrently; query its status and resume")
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", uploadRange(newOffset))
	w.Header().Set("Docker-Upload-Uuid", uploadUUID)
	w.WriteHeader(http.StatusAccepted)
}

// quotaBoundedBody returns the request body capped at the bytes the repository
// quota still has room for (the session's staged offset already counts), so a
// writer on a full repository cannot stage data the reservation at completion
// would refuse. ok=false means the declared Content-Length alone cannot fit and
// nothing may be staged. A completion whose digest is already stored in the
// repository (an idempotent re-push, which reserves nothing) is not capped.
func (h *HTTPHandler) quotaBoundedBody(w http.ResponseWriter, r *http.Request, repoID string, offset int64, digest string) (io.Reader, bool) {
	room, limited, err := quota.Room(r.Context(), h.db, repoID)
	if err != nil {
		// Advisory only: the reservation at completion stays authoritative.
		h.log.Warnw("failed to read quota room", "repo", repoID, "error", err)
		return r.Body, true
	}

	if !limited {
		return r.Body, true
	}

	if digest != "" {
		if held, err := h.db.GetDockerBlob(r.Context(), repoID, digest); err == nil && held != nil {
			return r.Body, true
		}
	}

	left := room - offset
	if left < 0 || (r.ContentLength > 0 && r.ContentLength > left) {
		return nil, false
	}

	return http.MaxBytesReader(w, r.Body, left), true
}

// refuseOverQuota answers a push the quota has no room for with 507 and
// discards the staged upload, like a completion the reservation refused.
func (h *HTTPHandler) refuseOverQuota(w http.ResponseWriter, r *http.Request, repoID, uploadUUID, tempFile string) {
	_ = os.Remove(tempFile)
	_ = h.db.DeleteDockerUpload(r.Context(), repoID, uploadUUID)

	h.quotaExceeded(w)
}

// uploadFileError answers a failure to open an upload's staging file.
func (h *HTTPHandler) uploadFileError(w http.ResponseWriter, uploadUUID string, err error) {
	h.log.Errorw("failed to open upload file", "uuid", uploadUUID, "error", err)

	if errors.Is(err, errUploadFileShort) {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload data lost; restart the upload")

		return
	}

	h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
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

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	// The session is locked from here to the end of the request: nothing can
	// append to or truncate the staged file while it is hashed and stored.
	unlock, ok := h.lockUpload(w, r, repo.ID, uploadUUID)
	if !ok {
		return
	}
	defer unlock()

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
			w.Header().Set("Range", uploadRange(upload.Offset))
			h.errorResponse(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", offsetErr.Error())

			return
		}
	}

	defer func() { _ = r.Body.Close() }()

	uploadDir := h.uploadDir
	tempFile := filepath.Join(uploadDir, uploadUUID)

	// Stream any final bytes from the request body directly to the temp file.
	// For monolithic uploads (single PUT carrying the whole blob) this avoids
	// buffering the entire layer in memory.
	if !h.appendFinalChunk(w, r, repo.ID, tempFile, uploadUUID, digest, upload.Offset) {
		return
	}

	// Compute the SHA256 (and size) of the temp file in a single pass.
	f, err := os.Open(tempFile)
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
		Image:        &image,
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
		held, err := h.blobAlreadyHeld(ctx, repo.ID, image, digest)
		if err != nil {
			return err
		}

		if held {
			return nil
		}

		return h.storeNewBlob(ctx, f, blob, computedDigestHex)
	}); err != nil {
		if errors.Is(err, storage.ErrDigestMismatch) {
			h.digestChanged(w, r, repo.ID, uploadUUID, tempFile, err)
			return
		}

		if errors.Is(err, quota.ErrExceeded) {
			// Nothing was stored; the staged bytes would only wait for the
			// stale-session sweep, so discard the session as a digest mismatch does.
			h.quotaExceeded(w)

			_ = os.Remove(tempFile)
			_ = h.db.DeleteDockerUpload(r.Context(), repo.ID, uploadUUID)

			return
		}

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

// storeNewBlob stores the verified bytes of a blob the repository does not hold
// and records its row. The blob's bytes are reserved first (quota.ErrExceeded
// when they do not fit, with nothing stored) and returned when storing or
// inserting fails. The caller holds the blob lock.
func (h *HTTPHandler) storeNewBlob(ctx context.Context, content io.Reader, blob *models.DockerBlob, shaHex string) error {
	if err := quota.EnforceN(ctx, h.db, blob.RepositoryID, blob.Size, 0); err != nil {
		return fmt.Errorf("reserve blob quota: %w", err)
	}

	if _, err := h.blobStore.Put(ctx, content, map[string]string{
		"sha256": shaHex,
		"digest": blob.Digest,
	}); err != nil {
		h.releaseQuota(ctx, blob.RepositoryID, blob.Size, 0)
		return fmt.Errorf("store blob: %w", err)
	}

	// Keyed by the sha256 computed from the spooled bytes, never by a value the
	// store or client reports.
	blob.BlobSHA256 = shaHex

	if err := h.db.CreateDockerBlob(ctx, blob); err != nil {
		h.releaseQuota(ctx, blob.RepositoryID, blob.Size, 0)
		return fmt.Errorf("create blob record: %w", err)
	}

	h.settleQuota(ctx, blob.RepositoryID, blob.Size, 0, true, blob.Inserted)

	return nil
}

// appendFinalChunk appends the request body to the staged file (after
// truncating it to offset). On failure it has written the error response.
func (h *HTTPHandler) appendFinalChunk(w http.ResponseWriter, r *http.Request, repoID, tempFile, uploadUUID, digest string, offset int64) bool {
	f, err := openUploadFile(tempFile, offset)
	if err != nil {
		h.uploadFileError(w, uploadUUID, err)

		return false
	}

	body, room := h.quotaBoundedBody(w, r, repoID, offset, digest)
	if !room {
		_ = f.Close()
		h.refuseOverQuota(w, r, repoID, uploadUUID, tempFile)

		return false
	}

	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()

		if _, over := errors.AsType[*http.MaxBytesError](err); over {
			h.refuseOverQuota(w, r, repoID, uploadUUID, tempFile)
			return false
		}

		h.log.Errorw("failed to write final chunk", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return false
	}

	if err := f.Close(); err != nil {
		h.log.Errorw("failed to close temp file", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return false
	}

	return true
}

// digestChanged answers a completion whose staged bytes no longer match the
// digest verified a moment earlier: the blob store re-hashes what it is given
// and refuses a mismatch, so nothing was stored; the upload is discarded.
func (h *HTTPHandler) digestChanged(w http.ResponseWriter, r *http.Request, repoID, uploadUUID, tempFile string, err error) {
	h.log.Warnw("staged upload changed while completing", "uuid", uploadUUID, "error", err)
	h.errorResponse(w, http.StatusBadRequest, "DIGEST_INVALID", "computed digest does not match")

	_ = os.Remove(tempFile)
	_ = h.db.DeleteDockerUpload(r.Context(), repoID, uploadUUID)
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

	unlock, ok := h.lockUpload(w, r, repo.ID, uploadUUID)
	if !ok {
		return
	}
	defer unlock()

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
	uploadDir := h.uploadDir
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

	// Pagination per the distribution spec: n bounds the page, last is the
	// final tag of the previous page, and a Link header points at the next
	// page whenever more tags remain.
	n, lastStr, ok := h.pageParams(w, r)
	if !ok {
		return
	}

	var tags []*models.DockerTag

	if n > 0 {
		var err error

		// One extra row tells whether another page follows.
		tags, err = h.db.ListDockerTags(r.Context(), repo.ID, image, lastStr, n+1)
		if err != nil {
			h.log.Errorw("failed to list tags", "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	}

	if len(tags) > n {
		tags = tags[:n]

		next := url.Values{"n": {strconv.Itoa(n)}, "last": {tags[n-1].Name}}
		w.Header().Set("Link", fmt.Sprintf("</v2/%s/tags/list?%s>; rel=\"next\"", name, next.Encode()))
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

		manifestBody, readErr := io.ReadAll(io.LimitReader(content, maxManifestBytes))
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
