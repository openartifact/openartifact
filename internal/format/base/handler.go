// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package base provides shared HTTP handler infrastructure for artifact format handlers.
// Artifact-level format handlers (cargo, helm, nuget, pypi, etc.) embed BaseHandler
// to eliminate duplicated struct definitions, constructors, and resolveRepoID.
package base

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/publicurl"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// HandlerStore is the part of the database Handler needs.
type HandlerStore interface {
	quarantine.Checker
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
}

// Handler provides shared fields and methods for artifact-level format HTTP handlers.
// Embed this in format-specific HTTPHandler structs to eliminate duplicated boilerplate.
type Handler struct {
	Log       *logger.Logger
	DB        HandlerStore
	BlobStore storage.BlobStore
	// BaseURL is the configured BASE_URL when the operator set one, "" otherwise.
	// Read it only through PublicBase.
	BaseURL string

	// Publisher receives artifact.* events; nil disables event publishing.
	Publisher webhook.Publisher

	// renderNS keeps this handler's rendered-document keys apart from every
	// other handler's in the shared render cache.
	renderNS string
}

var handlerSeq atomic.Uint64 //nolint:gochecknoglobals // numbers handlers for their render cache namespace

// New creates a new base handler with the common dependencies.
func New(log *logger.Logger, db HandlerStore, blobStore storage.BlobStore, baseURL string) *Handler {
	return &Handler{
		Log:       log,
		DB:        db,
		BlobStore: blobStore,
		BaseURL:   baseURL,
		renderNS:  strconv.FormatUint(handlerSeq.Add(1), 10) + "|",
	}
}

// PublicBase returns the origin (scheme://host, plus BASE_URL's path prefix,
// no trailing slash) this handler hands to clients in absolute URLs for the
// request r: BASE_URL when it is set, else the request's own origin, with
// X-Forwarded-* honored only from a trusted proxy (internal/publicurl). A
// document built from it MUST carry the result in its validator or cache key,
// and PublicBase MUST run before the response headers are written (it adds
// Vary). Formats whose clients resolve relative URLs should emit those instead.
func (h *Handler) PublicBase(w http.ResponseWriter, r *http.Request) string {
	return publicurl.Resolve(w, r, h.BaseURL)
}

// Rendered returns the metadata document cached under key, rendering it with
// render on a miss (concurrent misses share one render). key must embed the
// validator of the data the document is derived from; see RenderCache.
func (h *Handler) Rendered(ctx context.Context, key string, render func(ctx context.Context) (*Rendered, error)) (*Rendered, error) {
	return sharedRender.Do(ctx, h.renderNS+key, render)
}

// SetPublisher sets the webhook publisher used by Emit. It must be called
// before the handler serves requests.
func (h *Handler) SetPublisher(p webhook.Publisher) { h.Publisher = p }

// Emit publishes a webhook event (a no-op without a Publisher). It never fails
// the caller.
func (h *Handler) Emit(ctx context.Context, repoID, eventType string, payload map[string]any) {
	webhook.Emit(ctx, h.Publisher, repoID, eventType, payload)
}

// DenyIfQuarantined refuses the download with 403 when the artifact is
// quarantined and returns true; the caller MUST then return immediately. keys
// are every identifier the artifact is known by (see quarantine.PathKeys).
func (h *Handler) DenyIfQuarantined(w http.ResponseWriter, r *http.Request, repoID string, keys ...string) bool {
	return quarantine.Enforce(w, r, h.DB, repoID, keys...)
}

// ResolveRepoID resolves a repository name to its ID. Returns empty string if not found.
func (h *Handler) ResolveRepoID(ctx context.Context, name string) (string, error) {
	repo, err := h.DB.GetRepositoryByName(ctx, name)
	if err != nil {
		return "", err
	}
	if repo == nil {
		return "", nil
	}
	return repo.ID, nil
}

// ResolveRepoIDOrRespond is ResolveRepoID for handlers that answer the request
// themselves: on a lookup failure it writes a 500, for an unknown repository a
// 404, and returns "" in both cases; the caller MUST then return immediately.
func (h *Handler) ResolveRepoIDOrRespond(w http.ResponseWriter, r *http.Request, name string) string {
	repoID, err := h.ResolveRepoID(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return ""
	}

	if repoID == "" {
		http.NotFound(w, r)
	}

	return repoID
}

// ReserveQuotaOrRespond reserves size bytes (one artifact) of the repository's
// storage quota before an upload is persisted. When it cannot it writes the
// response (the quota-exceeded one, or a 500 for a lookup failure) and returns
// false; the caller MUST then return immediately. After a successful
// reservation every failure path must return it with quota.Release.
func (h *Handler) ReserveQuotaOrRespond(ctx context.Context, w http.ResponseWriter, repoID, repoName string, size int64) bool {
	err := quota.Enforce(ctx, h.DB, repoID, size)
	if err == nil {
		return true
	}

	if errors.Is(err, quota.ErrExceeded) {
		quota.WriteExceeded(w)

		return false
	}

	h.Log.Errorw("failed to reserve quota", "repo", repoName, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)

	return false
}

// ReleaseQuota returns size bytes (one artifact) to the repository's storage
// quota; a failure is only logged, repoName names the repository in the log.
func (h *Handler) ReleaseQuota(ctx context.Context, repoID, repoName string, size int64) {
	if err := quota.Release(ctx, h.DB, repoID, size); err != nil {
		h.Log.Warnw("failed to release quota", "repo", repoName, "error", err)
	}
}

// TryPresign answers a blob download with a 307 to a presigned object-store URL
// when presigned downloads are enabled for the repository and the blob
// qualifies (see package presign), and returns true; the caller MUST then
// return immediately. It returns false, having written nothing, when the
// caller should stream the blob itself.
//
// This is the single hook of the download path: call it from the binary
// download branch of a handler only after authorisation (the RBAC middleware
// runs before every format handler), quarantine checks and download counters,
// and right before opening the blob. Never call it for metadata. blob.Repo
// defaults to the {name} URL parameter.
func (h *Handler) TryPresign(w http.ResponseWriter, r *http.Request, blob presign.Blob) bool {
	if blob.Repo == "" && blob.Repository == nil {
		blob.Repo = urlparams.Vars(r)["name"]
	}

	return presign.Try(h.BlobStore, w, r, blob)
}

// StreamBlob streams a blob from the store to the response writer.
// Returns false if the blob is not found.
func (h *Handler) StreamBlob(w http.ResponseWriter, r *http.Request, blobID, contentType string) bool {
	if blobID == "" {
		return false
	}
	rc, err := h.BlobStore.Get(r.Context(), blobID)
	if err != nil {
		return false
	}
	defer func() { CloseLog(rc, h.Log) }()

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, err = iocopy.Copy(w, rc)
	return err == nil
}

// CloseLog closes a Closer and logs any error. Use for deferred closes where
// the error cannot be meaningfully handled but should not be silently discarded.
func CloseLog(c io.Closer, log *logger.Logger) {
	if err := c.Close(); err != nil {
		log.Warnw("close error", "error", err)
	}
}

// WriteJSON writes a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
