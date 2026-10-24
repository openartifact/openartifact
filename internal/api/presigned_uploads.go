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
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
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

	// presignedCompleteTimeout bounds verifying and committing one upload. It
	// runs detached from the HTTP request: hashing a multi-GiB object takes far
	// longer than a client is willing to wait for response headers, and an
	// aborted verification could never be retried to completion.
	presignedCompleteTimeout = 30 * time.Minute

	// presignedCompleteScan is the assumed worst-case speed, in bytes per
	// second, of reading an object back from the store and copying it: the
	// complete timeout grows by size / presignedCompleteScan for large objects.
	presignedCompleteScan = 20 << 20

	// presignedCompleteMaxTimeout caps the complete timeout. The cleanup query
	// waits longer than this (plus a minute of lease) before it treats a
	// complete as crashed.
	presignedCompleteMaxTimeout = 6 * time.Hour

	// presignedRetryAfter is the retry hint, in seconds, for an upload that is
	// being completed or a user at the pending-upload cap.
	presignedRetryAfter = "5"
)

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PresignedUploadHandler implements client-direct uploads: the client PUTs the
// bytes straight to the object store using a short-lived presigned URL, then
// calls complete so the server can verify size and sha256 and record the
// artifact. Only raw hosted repositories are supported.
type PresignedUploadHandler struct {
	*base.Handler

	evaluator *auth.Evaluator
	// staging is nil when the storage backend has no staging area.
	staging storage.StagingStore
	// multipart is nil when the staging area cannot take uploads in parts.
	multipart  storage.MultipartStagingStore
	urlTTL     time.Duration
	maxPending int
	reg        *metrics.Registry
}

// NewPresignedUploadHandler builds the handler. blobStore may or may not
// support staging uploads; an unsupported backend answers 501 without recording
// anything. maxPending caps one user's outstanding uploads (0 = unlimited).
func NewPresignedUploadHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore,
	evaluator *auth.Evaluator, urlTTL time.Duration, maxPending int, reg *metrics.Registry,
) *PresignedUploadHandler {
	var staging storage.StagingStore
	if s, ok := blobStore.(storage.StagingStore); ok && s.StagingSupported() {
		staging = s
	}

	var multipart storage.MultipartStagingStore
	if m, ok := blobStore.(storage.MultipartStagingStore); ok && staging != nil && m.MultipartSupported() {
		multipart = m
	}

	return &PresignedUploadHandler{
		Handler:    base.New(log, db, blobStore, ""),
		evaluator:  evaluator,
		staging:    staging,
		multipart:  multipart,
		urlTTL:     urlTTL,
		maxPending: maxPending,
		reg:        reg,
	}
}

// Register mounts the routes.
func (h *PresignedUploadHandler) Register(router chi.Router) {
	router.Post("/api/v1/repositories/{name}/uploads", h.handleCreate)
	router.Post("/api/v1/repositories/{name}/uploads/{id}/complete", h.handleComplete)
	router.Post("/api/v1/repositories/{name}/uploads/{id}/parts", h.handleParts)
	router.Post("/api/v1/repositories/{name}/uploads/{id}/abort", h.handleAbort)
}

// CreatePresignedUploadRequest is the body of POST /repositories/{name}/uploads.
type CreatePresignedUploadRequest struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
	// Multipart asks for a multipart upload: the client PUTs the object in
	// parts to URLs fetched from the parts endpoint. Required above the
	// single-upload limit (5 GiB).
	Multipart bool `json:"multipart,omitempty"`
	// PartSize is the part size in bytes of a multipart upload (5 MiB to
	// 4000 MiB); 0 lets the server choose.
	PartSize int64 `json:"part_size,omitempty"`
}

// CreatePresignedUploadResponse tells the client where and how to upload.
type CreatePresignedUploadResponse struct {
	ID string `json:"id"`
	// UploadURL accepts one PUT of exactly Size bytes. Do not log it: the
	// query string is the credential. Absent for a multipart upload.
	UploadURL string `json:"upload_url,omitempty"`
	Method    string `json:"method,omitempty"`
	// Headers must be sent with the PUT.
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt time.Time         `json:"expires_at"`
	// Multipart, PartSize and PartCount describe a multipart upload: PUT each
	// part (all PartSize bytes but the last) to a URL from the parts endpoint.
	Multipart bool  `json:"multipart,omitempty"`
	PartSize  int64 `json:"part_size,omitempty"`
	PartCount int   `json:"part_count,omitempty"`
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

	partSize, ok := h.planUpload(w, &req)
	if !ok {
		return
	}

	if h.staging == nil || (req.Multipart && h.multipart == nil) {
		writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support presigned uploads")
		return
	}

	repo := h.repoForUpload(w, r, name)
	if repo == nil || !h.authorizeWrite(w, r, name, path) {
		return
	}

	// Advisory: refuse to park bytes in the bucket that completion would
	// refuse anyway (the reservation at completion stays authoritative).
	if err := quota.Precheck(r.Context(), h.DB, repo.ID, req.Size); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("presigned upload: quota precheck failed", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	createdBy := currentPrincipal(r).Username

	h.createUpload(w, r, repo, &models.PresignedUpload{
		RepositoryID: repo.ID,
		Path:         path,
		SizeBytes:    req.Size,
		PartSize:     partSize,
		SHA256:       digest,
		ContentType:  req.ContentType,
		CreatedBy:    createdBy,
		ExpiresAt:    time.Now().Add(h.urlTTL + presignedUploadGrace),
	})
}

// writePendingCap answers 429 when the user already has the maximum number of
// outstanding uploads. Each one can hold up to 5 GiB of staging storage.
func writePendingCap(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "too_many_uploads",
		"too many uploads in progress; complete or wait for expiry of existing ones")
}

func (h *PresignedUploadHandler) createUpload(w http.ResponseWriter, r *http.Request, repo *models.Repository, up *models.PresignedUpload) {
	ctx := r.Context()

	if err := h.DB.CreatePresignedUploadCapped(ctx, up, h.maxPending); err != nil {
		if errors.Is(err, database.ErrPresignedUploadCap) {
			writePendingCap(w)
			return
		}

		h.Log.Errorw("presigned upload: failed to record upload", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	if up.PartSize > 0 {
		h.startMultipart(w, r, repo, up)
		return
	}

	put, err := h.staging.PresignStagingPut(ctx, up.ID, up.SizeBytes, h.urlTTL)
	if err != nil {
		h.forgetUpload(up.ID)

		if errors.Is(err, storage.ErrSingleUploadTooLarge) {
			writeError(w, http.StatusBadRequest, "invalid_size", "size is too large for a single upload on this storage backend; set multipart")
			return
		}

		if errors.Is(err, storage.ErrPresignNotSupported) {
			writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support presigned uploads")
			return
		}

		h.Log.Errorw("presigned upload: failed to presign", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	// upload_url is a bearer credential: never cache this response.
	w.Header().Set("Cache-Control", "no-store")

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

	h.deleteStaging(ctx, id)

	if err := h.DB.DeletePresignedUpload(ctx, id); err != nil {
		h.Log.Warnw("presigned upload: failed to delete upload row", "upload_id", id, "error", err)
	}
}

// deleteStaging removes the staging object, best effort.
func (h *PresignedUploadHandler) deleteStaging(ctx context.Context, id string) {
	if err := h.staging.DeleteStaging(ctx, id); err != nil && !errors.Is(err, storage.ErrPresignNotSupported) {
		h.Log.Warnw("presigned upload: failed to delete staging object", "upload_id", id, "error", err)
	}
}

// errUploadMismatch marks a verification failure that makes the upload
// unusable (as opposed to a transient storage error).
var errUploadMismatch = errors.New("upload does not match its declaration")

// verifiedUpload is what verifyStaged established about a staged object.
type verifiedUpload struct {
	contentType string
	version     string
}

// verifyStaged checks the staged object against the declared size and sha256
// and returns the detected content type and the version (ETag) that was
// verified. Reading is conditional on that version, and the caller promotes
// only that version, so bytes uploaded after verification are never promoted.
func (h *PresignedUploadHandler) verifyStaged(ctx context.Context, up *models.PresignedUpload) (verifiedUpload, error) {
	obj, err := h.staging.StatStaging(ctx, up.ID)
	if err != nil {
		return verifiedUpload{}, err
	}

	if obj.Size != up.SizeBytes {
		return verifiedUpload{}, fmt.Errorf("%w: size %d, declared %d", errUploadMismatch, obj.Size, up.SizeBytes)
	}

	rc, err := h.staging.OpenStaging(ctx, up.ID, obj.Version)
	if err != nil {
		return verifiedUpload{}, err
	}
	defer func() { _ = rc.Close() }()

	hasher := sha256.New()
	head := &headCapture{limit: 512}

	n, err := io.Copy(io.MultiWriter(hasher, head), io.LimitReader(rc, up.SizeBytes+1))
	if err != nil {
		return verifiedUpload{}, fmt.Errorf("read staged object: %w", err)
	}

	if n != up.SizeBytes {
		return verifiedUpload{}, fmt.Errorf("%w: read %d bytes, declared %d", errUploadMismatch, n, up.SizeBytes)
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != up.SHA256 {
		return verifiedUpload{}, fmt.Errorf("%w: sha256 differs", errUploadMismatch)
	}

	if up.ContentType != "" {
		return verifiedUpload{up.ContentType, obj.Version}, nil
	}

	return verifiedUpload{http.DetectContentType(head.buf), obj.Version}, nil
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

// authorizeCompleter lets only the creator of an upload, or an administrator,
// complete it.
func (h *PresignedUploadHandler) authorizeCompleter(w http.ResponseWriter, r *http.Request, up *models.PresignedUpload) bool {
	p := currentPrincipal(r)
	if p.Username == up.CreatedBy {
		return true
	}

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil {
		h.Log.Errorw("presigned upload: admin check failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return false
	}

	if !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "only the creator of an upload or an administrator can complete it")
		return false
	}

	return true
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
		writeError(w, http.StatusNotFound, "not_found", "upload not found")
		return
	}

	if !h.authorizeWrite(w, r, name, up.Path) || !h.authorizeCompleter(w, r, up) {
		return
	}

	if up.CompletedAt != nil {
		// A retried complete (lost response, client timeout) gets the original result.
		writeJSON(w, http.StatusOK, completedResponse(up, up.ArtifactID))
		return
	}

	expired := time.Now().After(up.ExpiresAt)

	// Claim before anything else, even for an expired upload: a complete that
	// started earlier may still be verifying the staging object, and deleting
	// it from under that call would fail it. Only the claim holder may discard.
	claimed, err := h.DB.ClaimPresignedUpload(r.Context(), repo.ID, up.ID, completeTimeout(up.SizeBytes)+time.Minute)
	if err != nil {
		h.Log.Errorw("presigned upload: failed to claim upload", "upload_id", up.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	if !claimed {
		w.Header().Set("Retry-After", presignedRetryAfter)
		writeError(w, http.StatusConflict, "upload_in_progress", "this upload is being completed; retry shortly for the result")

		return
	}

	if expired {
		h.forgetUpload(up.ID)
		h.reg.CountUpload(metrics.FormatRaw, metrics.UploadError)
		writeError(w, http.StatusGone, "upload_expired", "upload expired; start a new one")

		return
	}

	// The work outlives the request: a client that gives up (or times out
	// waiting for headers) must not abort a verification in progress.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), completeTimeout(up.SizeBytes))
	defer cancel()

	// Commit reads the principal and ?force from the request; keep both while
	// running on the detached context.
	resp, fail := h.complete(r.WithContext(ctx), repo, up)
	if fail != nil {
		h.reg.CountUpload(metrics.FormatRaw, metrics.UploadError)
		writeError(w, fail.status, fail.code, fail.message)

		return
	}

	h.reg.CountUpload(metrics.FormatRaw, metrics.UploadSuccess)

	writeJSON(w, http.StatusCreated, resp)
}

func completedResponse(up *models.PresignedUpload, artifactID string) CompletePresignedUploadResponse {
	return CompletePresignedUploadResponse{ID: artifactID, Path: up.Path, SHA256: up.SHA256, Size: up.SizeBytes}
}

// completeFailure is an error response of complete.
type completeFailure struct {
	status  int
	code    string
	message string
}

// release gives up the complete lease after a failure that leaves the upload
// retryable.
func (h *PresignedUploadHandler) release(ctx context.Context, id string) {
	if err := h.DB.ReleasePresignedUpload(ctx, id); err != nil {
		h.Log.Warnw("presigned upload: failed to release upload", "upload_id", id, "error", err)
	}
}

// reject discards an upload that cannot be completed (the lease dies with the row).
func (h *PresignedUploadHandler) reject(repo *models.Repository, up *models.PresignedUpload, err error, f *completeFailure) *completeFailure {
	h.Log.Warnw("presigned upload rejected", "repo", repo.Name, "path", up.Path, "upload_id", up.ID, "reason", err.Error())
	h.forgetUpload(up.ID)

	return f
}

func failInternal() *completeFailure {
	return &completeFailure{http.StatusInternalServerError, "internal_error", "internal error"}
}

func failMismatch() *completeFailure {
	return &completeFailure{http.StatusUnprocessableEntity, "upload_mismatch", "uploaded object does not match the declared size and sha256"}
}

func failChanged() *completeFailure {
	return &completeFailure{http.StatusConflict, "upload_changed", "the uploaded object changed while it was being verified; start a new upload"}
}

func failImmutable(path string) *completeFailure {
	return &completeFailure{http.StatusConflict, "immutable_release", "repository has immutable releases: cannot overwrite " + path}
}

func failQuota() *completeFailure {
	return &completeFailure{http.StatusInsufficientStorage, "quota_exceeded", quota.Message}
}

func failMissing() *completeFailure {
	return &completeFailure{http.StatusConflict, "upload_missing", "no object has been uploaded to the upload URL yet"}
}

// complete verifies and commits a claimed upload. req carries the caller's
// principal and query but a context detached from the HTTP request.
func (h *PresignedUploadHandler) complete(req *http.Request, repo *models.Repository, up *models.PresignedUpload) (CompletePresignedUploadResponse, *completeFailure) {
	ctx := req.Context()

	if fail := h.assembleParts(ctx, up); fail != nil {
		return CompletePresignedUploadResponse{}, fail
	}

	verified, err := h.verifyStaged(ctx, up)

	switch {
	case errors.Is(err, storage.ErrStagingNotFound):
		h.release(ctx, up.ID)
		return CompletePresignedUploadResponse{}, failMissing()
	case errors.Is(err, errUploadMismatch):
		// The object is not what was declared: discard it and record nothing.
		return CompletePresignedUploadResponse{}, h.reject(repo, up, err, failMismatch())
	case errors.Is(err, storage.ErrStagingChanged):
		return CompletePresignedUploadResponse{}, h.reject(repo, up, err, failChanged())
	case err != nil:
		h.Log.Errorw("presigned upload: verification failed", "upload_id", up.ID, "error", err)
		h.release(ctx, up.ID)

		return CompletePresignedUploadResponse{}, failInternal()
	}

	artifact := &models.RawArtifact{
		ID:           up.ID,
		RepositoryID: repo.ID,
		Path:         up.Path,
		ContentType:  verified.contentType,
		SizeBytes:    up.SizeBytes,
		SHA256:       up.SHA256,
		BlobKey:      up.SHA256,
		Metadata:     make(map[string]any),
	}

	previous, err := formatraw.CommitPromoted(req, h.Handler, repo.Name, artifact, func(ctx context.Context) error {
		return h.staging.PromoteStaging(ctx, up.ID, up.SHA256, verified.version, map[string]string{
			"sha256": up.SHA256,
			"format": "raw",
			"path":   up.Path,
		})
	})

	switch {
	case errors.Is(err, storage.ErrStagingChanged):
		return CompletePresignedUploadResponse{}, h.reject(repo, up, err, failChanged())
	case errors.Is(err, trash.ErrImmutableRelease):
		// Nothing was promoted or recorded. The upload stays pending (not
		// completed), so a retry runs the guard again instead of replaying a
		// result, and an admin can complete it with ?force=true; the cleanup
		// worker removes the staging object after expiry otherwise.
		h.Log.Infow("presigned upload refused: immutable release", "repo", repo.Name, "path", up.Path, "upload_id", up.ID)
		h.release(ctx, up.ID)

		return CompletePresignedUploadResponse{}, failImmutable(up.Path)
	case errors.Is(err, quota.ErrExceeded):
		// Nothing was promoted or recorded. Like the immutable refusal the
		// upload stays pending, so it can be completed once space is freed;
		// the cleanup worker removes the staging object after expiry otherwise.
		h.Log.Infow("presigned upload refused: quota exceeded", "repo", repo.Name, "path", up.Path, "upload_id", up.ID)
		h.release(ctx, up.ID)

		return CompletePresignedUploadResponse{}, failQuota()
	case err != nil:
		h.Log.Errorw("presigned upload: failed to record artifact", "repo", repo.Name, "path", up.Path, "error", err)
		h.release(ctx, up.ID)

		return CompletePresignedUploadResponse{}, failInternal()
	}

	artifactID := formatraw.StoredID(artifact, previous)

	// Keep the row, marked completed, so a retried complete returns this
	// result; the cleanup worker removes it after expires_at.
	if err := h.DB.MarkPresignedUploadCompleted(ctx, up.ID, artifactID); err != nil {
		h.Log.Warnw("presigned upload: failed to mark upload completed", "upload_id", up.ID, "error", err)
		h.forgetUpload(up.ID)
	} else {
		h.deleteStaging(ctx, up.ID)
	}

	return completedResponse(up, artifactID), nil
}
