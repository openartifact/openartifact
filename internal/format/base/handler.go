// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package base provides shared HTTP handler infrastructure for artifact format handlers.
// Artifact-level format handlers (cargo, helm, pypi, nuget, etc.) embed BaseHandler
// to eliminate duplicated struct definitions, constructors, and resolveRepoID.
package base

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Handler provides shared fields and methods for artifact-level format HTTP handlers.
// Embed this in format-specific HTTPHandler structs to eliminate duplicated boilerplate.
type Handler struct {
	Log       *logger.Logger
	DB        database.Database
	BlobStore storage.BlobStore
	BaseURL   string

	// Publisher receives artifact.* events; nil disables event publishing.
	Publisher webhook.Publisher

	// renderNS keeps this handler's rendered-document keys apart from every
	// other handler's in the shared render cache.
	renderNS string
}

var handlerSeq atomic.Uint64 //nolint:gochecknoglobals // numbers handlers for their render cache namespace

// New creates a new base handler with the common dependencies.
func New(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *Handler {
	return &Handler{
		Log:       log,
		DB:        db,
		BlobStore: blobStore,
		BaseURL:   baseURL,
		renderNS:  strconv.FormatUint(handlerSeq.Add(1), 10) + "|",
	}
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
	_, err = io.Copy(w, rc)
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

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
}
