// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// presignedUploadGrace is how long after its URL expires an upload may
	// still be completed (a PUT started just before expiry can run long).
	presignedUploadGrace = time.Hour

	// maxPresignedPathBytes bounds the artifact path of an upload.
	maxPresignedPathBytes = 1024

	// presignedUploadCleanupTimeout bounds the best-effort cleanup that runs
	// after the request context may already be cancelled.
	presignedUploadCleanupTimeout = 30 * time.Second
)

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PresignedUploadHandler implements client-direct uploads: the client PUTs the
// bytes straight to the object store using a short-lived presigned URL, then
// calls complete so the server can verify size and sha256 and record the
// artifact. Only raw hosted repositories are supported.
type PresignedUploadHandler struct {
	*base.Handler

	evaluator *auth.Evaluator
	staging   storage.StagingStore
	urlTTL    time.Duration
	reg       *metrics.Registry
}

// NewPresignedUploadHandler builds the handler. blobStore may or may not
// support staging uploads; an unsupported backend answers 501.
func NewPresignedUploadHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore,
	evaluator *auth.Evaluator, urlTTL time.Duration, reg *metrics.Registry,
) *PresignedUploadHandler {
	staging, _ := blobStore.(storage.StagingStore)

	return &PresignedUploadHandler{
		Handler:   base.New(log, db, blobStore, ""),
		evaluator: evaluator,
		staging:   staging,
		urlTTL:    urlTTL,
		reg:       reg,
	}
}

// Register mounts the routes.
func (h *PresignedUploadHandler) Register(router chi.Router) {
	router.Post("/api/v1/repositories/{name}/uploads", h.handleCreate)
	router.Post("/api/v1/repositories/{name}/uploads/{id}/complete", h.handleComplete)
}

// CreatePresignedUploadRequest is the body of POST /repositories/{name}/uploads.
type CreatePresignedUploadRequest struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
}

// CreatePresignedUploadResponse tells the client where and how to upload.
type CreatePresignedUploadResponse struct {
	ID string `json:"id"`
	// UploadURL accepts one PUT of exactly Size bytes. Do not log it: the
	// query string is the credential.
	UploadURL string `json:"upload_url"`
	Method    string `json:"method"`
	// Headers must be sent with the PUT.
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// CompletePresignedUploadResponse mirrors the response of a raw PUT.
type CompletePresignedUploadResponse struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// cleanPresignedPath validates and normalises an artifact path.
func cleanPresignedPath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "/")
	if p == "" || len(p) > maxPresignedPathBytes || strings.HasSuffix(p, "/") || strings.ContainsRune(p, 0) {
		return "", false
	}

	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}

	return p, true
}

// repoForUpload resolves the target repository, writing the error response
// when it is missing or not a hosted raw repository.
func (h *PresignedUploadHandler) repoForUpload(w http.ResponseWriter, r *http.Request, name string) *models.Repository {
	repo, err := h.DB.GetRepositoryByName(r.Context(), name)
	if err != nil {
		h.Log.Errorw("presigned upload: repository lookup failed", "repo", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return nil
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil
	}

	if repo.Format != "raw" || repo.RepoType != "hosted" {
		writeError(w, http.StatusBadRequest, "unsupported_repository",
			"presigned uploads are only available for hosted raw repositories")

		return nil
	}

	return repo
}

// authorizeWrite checks the path-scoped write permission (the RBAC middleware
// only checked repository-level write).
func (h *PresignedUploadHandler) authorizeWrite(w http.ResponseWriter, r *http.Request, repoName, path string) bool {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		w.Header().Set("WWW-Authenticate", `Bearer realm="openartifact"`)
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")

		return false
	}

	allowed, err := h.evaluator.Evaluate(r.Context(), *p, repoName, path, "write")
	if err != nil {
		h.Log.Errorw("presigned upload: permission check failed", "repo", repoName, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return false
	}

	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "write permission denied")
		return false
	}

	return true
}

func (h *PresignedUploadHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]

	var req CreatePresignedUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	path, ok := cleanPresignedPath(req.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_path", "path is empty, too long or contains empty, '.' or '..' segments")
		return
	}

	digest := strings.ToLower(req.SHA256)
	if !sha256HexPattern.MatchString(digest) {
		writeError(w, http.StatusBadRequest, "invalid_sha256", "sha256 must be 64 hex characters")
		return
	}

	if req.Size < 0 || req.Size > formatraw.MaxUploadBytes {
		writeError(w, http.StatusBadRequest, "invalid_size", "size is negative or above the single-upload limit")
		return
	}

	if h.staging == nil {
		writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support presigned uploads")
		return
	}

	repo := h.repoForUpload(w, r, name)
	if repo == nil || !h.authorizeWrite(w, r, name, path) {
		return
	}

	h.createUpload(w, r, repo, &models.PresignedUpload{
		RepositoryID: repo.ID,
		Path:         path,
		SizeBytes:    req.Size,
		SHA256:       digest,
		ContentType:  req.ContentType,
		CreatedBy:    currentPrincipal(r).Username,
		ExpiresAt:    time.Now().Add(h.urlTTL + presignedUploadGrace),
	})
}

func (h *PresignedUploadHandler) createUpload(w http.ResponseWriter, r *http.Request, repo *models.Repository, up *models.PresignedUpload) {
	ctx := r.Context()

	if err := h.DB.CreatePresignedUpload(ctx, up); err != nil {
		h.Log.Errorw("presigned upload: failed to record upload", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	put, err := h.staging.PresignStagingPut(ctx, up.ID, up.SizeBytes, h.urlTTL)
	if err != nil {
		h.forgetUpload(up.ID)

		if errors.Is(err, storage.ErrPresignNotSupported) {
			writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support presigned uploads")
			return
		}

		h.Log.Errorw("presigned upload: failed to presign", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	writeJSON(w, http.StatusCreated, CreatePresignedUploadResponse{
		ID:        up.ID,
		UploadURL: put.URL,
		Method:    http.MethodPut,
		Headers:   put.Headers,
		ExpiresAt: put.Expires,
	})
}

// forgetUpload removes an upload's row and staging object, best effort, even if
// the request context is already gone.
func (h *PresignedUploadHandler) forgetUpload(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), presignedUploadCleanupTimeout)
	defer cancel()

	if err := h.staging.DeleteStaging(ctx, id); err != nil {
		h.Log.Warnw("presigned upload: failed to delete staging object", "upload_id", id, "error", err)
	}

	if err := h.DB.DeletePresignedUpload(ctx, id); err != nil {
		h.Log.Warnw("presigned upload: failed to delete upload row", "upload_id", id, "error", err)
	}
}

// errUploadMismatch marks a verification failure that makes the upload
// unusable (as opposed to a transient storage error).
var errUploadMismatch = errors.New("upload does not match its declaration")

// verifyStaged checks the staged object against the declared size and sha256
// and returns the detected content type.
func (h *PresignedUploadHandler) verifyStaged(ctx context.Context, up *models.PresignedUpload) (string, error) {
	size, err := h.staging.StatStaging(ctx, up.ID)
	if err != nil {
		return "", err
	}

	if size != up.SizeBytes {
		return "", fmt.Errorf("%w: size %d, declared %d", errUploadMismatch, size, up.SizeBytes)
	}

	rc, err := h.staging.OpenStaging(ctx, up.ID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	hasher := sha256.New()
	head := &headCapture{limit: 512}

	n, err := io.Copy(io.MultiWriter(hasher, head), io.LimitReader(rc, up.SizeBytes+1))
	if err != nil {
		return "", fmt.Errorf("read staged object: %w", err)
	}

	if n != up.SizeBytes {
		return "", fmt.Errorf("%w: read %d bytes, declared %d", errUploadMismatch, n, up.SizeBytes)
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != up.SHA256 {
		return "", fmt.Errorf("%w: sha256 differs", errUploadMismatch)
	}

	if up.ContentType != "" {
		return up.ContentType, nil
	}

	return http.DetectContentType(head.buf), nil
}

// headCapture keeps the first limit bytes written to it.
type headCapture struct {
	buf   []byte
	limit int
}

func (c *headCapture) Write(p []byte) (int, error) {
	if room := c.limit - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}

	return len(p), nil
}

func (h *PresignedUploadHandler) countUpload(result string) {
	if h.reg != nil {
		h.reg.UploadsTotal.WithLabelValues("raw", result).Inc()
	}
}

func (h *PresignedUploadHandler) handleComplete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name, id := vars["name"], vars["id"]

	if h.staging == nil {
		writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support presigned uploads")
		return
	}

	repo := h.repoForUpload(w, r, name)
	if repo == nil {
		return
	}

	up, err := h.DB.GetPresignedUpload(r.Context(), repo.ID, id)
	if err != nil {
		h.Log.Errorw("presigned upload: lookup failed", "upload_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	if up == nil {
		writeError(w, http.StatusNotFound, "not_found", "upload not found or already completed")
		return
	}

	if !h.authorizeWrite(w, r, name, up.Path) {
		return
	}

	if time.Now().After(up.ExpiresAt) {
		h.forgetUpload(up.ID)
		writeError(w, http.StatusGone, "upload_expired", "upload expired; start a new one")

		return
	}

	h.complete(w, r, repo, up)
}

func (h *PresignedUploadHandler) complete(w http.ResponseWriter, r *http.Request, repo *models.Repository, up *models.PresignedUpload) {
	ctx := r.Context()

	contentType, err := h.verifyStaged(ctx, up)

	switch {
	case errors.Is(err, storage.ErrStagingNotFound):
		writeError(w, http.StatusConflict, "upload_missing", "no object has been uploaded to the upload URL yet")
		return
	case errors.Is(err, errUploadMismatch):
		// The object is not what was declared: discard it and record nothing.
		h.Log.Warnw("presigned upload rejected", "repo", repo.Name, "path", up.Path, "upload_id", up.ID, "reason", err.Error())
		h.forgetUpload(up.ID)
		h.countUpload("rejected")
		writeError(w, http.StatusUnprocessableEntity, "upload_mismatch", "uploaded object does not match the declared size and sha256")

		return
	case err != nil:
		h.Log.Errorw("presigned upload: verification failed", "upload_id", up.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	artifact := &models.RawArtifact{
		ID:           up.ID,
		RepositoryID: repo.ID,
		Path:         up.Path,
		ContentType:  contentType,
		SizeBytes:    up.SizeBytes,
		SHA256:       up.SHA256,
		BlobKey:      up.SHA256,
		Metadata:     make(map[string]any),
	}

	previous, err := formatraw.Commit(ctx, h.Handler, repo.Name, artifact, func(ctx context.Context) error {
		return h.staging.PromoteStaging(ctx, up.ID, up.SHA256, map[string]string{
			"sha256": up.SHA256,
			"format": "raw",
			"path":   up.Path,
		})
	})
	if err != nil {
		h.Log.Errorw("presigned upload: failed to record artifact", "repo", repo.Name, "path", up.Path, "error", err)
		h.countUpload("failure")
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	h.forgetUpload(up.ID)
	h.countUpload("success")

	writeJSON(w, http.StatusCreated, CompletePresignedUploadResponse{
		ID:     formatraw.StoredID(artifact, previous),
		Path:   up.Path,
		SHA256: up.SHA256,
		Size:   up.SizeBytes,
	})
}
