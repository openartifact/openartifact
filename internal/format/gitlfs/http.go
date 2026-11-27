// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gitlfs serves hosted Git LFS repositories: the batch API with the
// basic transfer adapter (https://github.com/git-lfs/git-lfs/blob/main/docs/api).
// Objects are immutable blobs named by their SHA-256 and live under
// /repos/gitlfs/<repo>; a client points lfs.url at that URL.
package gitlfs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/gitlfs"
	"github.com/openartifact/openartifact/pkg/logger"
)

// maxObjectBytes bounds a single object upload (the server's max body size
// applies as well).
const maxObjectBytes = 5 << 30

// maxVerifyBytes bounds a verify request, which names one object.
const maxVerifyBytes = 4 << 10

// HTTPStore is the part of the database HTTPHandler needs.
type HTTPStore interface {
	quarantine.Checker
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CountFileDownload(fileID string)
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	GetGitLFSObjects(ctx context.Context, repoID string, oids []string) ([]*models.GitLFSObject, error)
	GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error)
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	PutGitLFSObject(ctx context.Context, obj *models.GitLFSObject) error
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
}

// HTTPHandler serves the Git LFS protocol of hosted repositories.
type HTTPHandler struct {
	*base.Handler

	DB HTTPStore
}

// NewHTTPHandler returns a handler for repositories of format gitlfs.
func NewHTTPHandler(log *logger.Logger, db HTTPStore, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New("gitlfs", log, db, blobStore, baseURL), DB: db}
}

// RegisterRepoRoutes registers the protocol routes on the repository's
// sub-router (/repos/gitlfs/{name}); the caller owns the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Post("/objects/batch", h.handleBatch)
	r.Put("/objects/{oid}", h.handlePut)
	r.Get("/objects/{oid}", h.handleGet)
	r.Head("/objects/{oid}", h.handleGet)
	r.Post("/verify", h.handleVerify)

	// File locking is not offered: every locks endpoint answers 501, which a
	// client reads as "this server has no locking API".
	r.Post("/locks", h.handleLocksUnsupported)
	r.Get("/locks", h.handleLocksUnsupported)
	r.Post("/locks/verify", h.handleLocksUnsupported)
	r.Post("/locks/{id}/unlock", h.handleLocksUnsupported)
}

// RegisterRoutes registers the protocol routes under prefix.
func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, h.RegisterRepoRoutes)
	h.Log.Infow("gitlfs handler registered", "prefix", prefix)
}

// writeJSON answers with a protocol document.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil { // the documents are plain structs: this cannot happen
		status, body = http.StatusInternalServerError, []byte(`{"message":"internal error"}`)
	}

	w.Header().Set("Content-Type", gitlfs.MediaType)
	w.WriteHeader(status)

	_, _ = w.Write(append(body, '\n')) // the header is written; a failed write is the client's
}

// writeError answers a failed request with the protocol's error document.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, gitlfs.Error{Message: message})
}

func (h *HTTPHandler) handleLocksUnsupported(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "this server does not support the Git LFS file locking API")
}

// lookupOne returns the live object of the repository, nil when it is not stored.
func (h *HTTPHandler) lookupOne(ctx context.Context, repoID, oid string) (*models.GitLFSObject, error) {
	objs, err := h.DB.GetGitLFSObjects(ctx, repoID, []string{oid})
	if err != nil {
		return nil, err //nolint:wrapcheck // the callers log it and answer 500
	}

	for _, o := range objs {
		if o.OID == oid {
			return o, nil
		}
	}

	return nil, nil //nolint:nilnil // not stored is (nil, nil)
}

// isTooLarge reports whether err is the body cap of http.MaxBytesReader or the
// server-wide body limit.
func isTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError

	return errors.As(err, &tooLarge)
}
