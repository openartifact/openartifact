// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// maxPartsPerRequest bounds how many part URLs one parts call signs.
const maxPartsPerRequest = 100

// PresignedPartsRequest is the body of POST /repositories/{name}/uploads/{id}/parts.
type PresignedPartsRequest struct {
	// PartNumbers are the 1-based parts to sign, at most 100 per call.
	PartNumbers []int `json:"part_numbers"`
}

// PresignedPart is the upload target of one part.
type PresignedPart struct {
	PartNumber int `json:"part_number"`
	// UploadURL accepts one PUT of exactly Size bytes. Do not log it: the
	// query string is the credential.
	UploadURL string `json:"upload_url"`
	Method    string `json:"method"`
	// Headers must be sent with the PUT.
	Headers map[string]string `json:"headers"`
	// Size is the exact size of the part in bytes.
	Size int64 `json:"size"`
}

// PresignedPartsResponse carries the signed part URLs.
type PresignedPartsResponse struct {
	Parts     []PresignedPart `json:"parts"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// completeTimeout bounds verifying and committing an upload of size bytes: the
// base budget plus the time to read the object back and copy it at a slow
// presignedCompleteScan, up to presignedCompleteMaxTimeout.
func completeTimeout(size int64) time.Duration {
	scan := time.Duration(size/presignedCompleteScan) * time.Second

	return min(presignedCompleteTimeout+scan, presignedCompleteMaxTimeout)
}

// planUpload validates the size and multipart fields of a create request and
// returns the part size of the upload (0 for a single PUT), writing the error
// response itself when they are invalid.
func (h *PresignedUploadHandler) planUpload(w http.ResponseWriter, req *CreatePresignedUploadRequest) (int64, bool) {
	if req.Size < 0 {
		writeError(w, http.StatusBadRequest, "invalid_size", "size is negative")
		return 0, false
	}

	if !req.Multipart {
		if req.PartSize != 0 {
			writeError(w, http.StatusBadRequest, "invalid_part_size", "part_size needs multipart")
			return 0, false
		}

		if req.Size > formatraw.MaxUploadBytes {
			writeError(w, http.StatusBadRequest, "invalid_size", "size is above the single-upload limit of 5 GiB; set multipart")
			return 0, false
		}

		return 0, true
	}

	if req.Size == 0 {
		writeError(w, http.StatusBadRequest, "invalid_size", "an empty file does not need a multipart upload")
		return 0, false
	}

	partSize, _, err := storage.PlanParts(req.Size, req.PartSize)
	if err != nil {
		code := "invalid_part_size"
		if req.Size > storage.MaxMultipartObjectBytes {
			code = "invalid_size"
		}

		writeError(w, http.StatusBadRequest, code, err.Error())

		return 0, false
	}

	return partSize, true
}

// startMultipart opens the multipart upload on the backend and answers with
// its plan. The row already exists; a failure removes it again.
func (h *PresignedUploadHandler) startMultipart(w http.ResponseWriter, r *http.Request, repo *models.Repository, up *models.PresignedUpload) {
	ctx := r.Context()

	handle, err := h.multipart.InitiateMultipart(ctx, up.ID, up.SizeBytes, up.PartSize)
	if err == nil && handle != "" {
		err = h.DB.SetPresignedUploadMultipartID(ctx, up.ID, handle)
	}

	if err != nil {
		h.forgetUpload(up.ID)

		if errors.Is(err, storage.ErrPresignNotSupported) {
			writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support multipart uploads")
			return
		}

		h.Log.Errorw("presigned upload: failed to start multipart upload", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	w.Header().Set("Cache-Control", "no-store")

	writeJSON(w, http.StatusCreated, CreatePresignedUploadResponse{
		ID:        up.ID,
		ExpiresAt: up.ExpiresAt,
		Multipart: true,
		PartSize:  up.PartSize,
		PartCount: storage.PartCount(up.SizeBytes, up.PartSize),
	})
}

// authorizedUpload resolves an upload of the repository in the URL for a
// caller that may act on it (the creator or an administrator), writing the
// error response when it cannot.
func (h *PresignedUploadHandler) authorizedUpload(w http.ResponseWriter, r *http.Request) (*models.Repository, *models.PresignedUpload) {
	vars := urlparams.Vars(r)
	name, id := vars["name"], vars["id"]

	if h.staging == nil {
		writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support presigned uploads")
		return nil, nil
	}

	repo := h.repoForUpload(w, r, name)
	if repo == nil {
		return nil, nil
	}

	up, err := h.DB.GetPresignedUpload(r.Context(), repo.ID, id)
	if err != nil {
		h.Log.Errorw("presigned upload: lookup failed", "upload_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return nil, nil
	}

	if up == nil {
		writeError(w, http.StatusNotFound, "not_found", "upload not found")
		return nil, nil
	}

	if !h.authorizeWrite(w, r, name, up.Path) || !h.authorizeCompleter(w, r, up) {
		return nil, nil
	}

	return repo, up
}

// handleParts signs the URLs of the requested parts of a multipart upload and
// keeps the upload alive: its complete deadline moves out to a full URL
// lifetime (plus the grace period) from now, so an upload that makes progress
// does not expire however long it takes.
func (h *PresignedUploadHandler) handleParts(w http.ResponseWriter, r *http.Request) {
	_, up := h.authorizedUpload(w, r)
	if up == nil {
		return
	}

	if up.PartSize == 0 {
		writeError(w, http.StatusBadRequest, "not_multipart", "the upload is not a multipart upload")
		return
	}

	if h.multipart == nil {
		writeError(w, http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support multipart uploads")
		return
	}

	if up.CompletedAt != nil {
		writeError(w, http.StatusConflict, "upload_completed", "the upload is already completed")
		return
	}

	if time.Now().After(up.ExpiresAt) {
		writeError(w, http.StatusGone, "upload_expired", "upload expired; start a new one")
		return
	}

	var req PresignedPartsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	if !validPartNumbers(req.PartNumbers, storage.PartCount(up.SizeBytes, up.PartSize)) {
		writeError(w, http.StatusBadRequest, "invalid_part_numbers",
			fmt.Sprintf("part_numbers must hold 1 to %d distinct part numbers within the upload's part count", maxPartsPerRequest))

		return
	}

	h.signParts(w, r, up, req.PartNumbers)
}

// validPartNumbers reports whether numbers is a non-empty, bounded list of
// distinct part numbers within 1..count.
func validPartNumbers(numbers []int, count int) bool {
	if len(numbers) == 0 || len(numbers) > maxPartsPerRequest {
		return false
	}

	seen := make(map[int]struct{}, len(numbers))

	for _, n := range numbers {
		if n < 1 || n > count {
			return false
		}

		if _, dup := seen[n]; dup {
			return false
		}

		seen[n] = struct{}{}
	}

	return true
}

func (h *PresignedUploadHandler) signParts(w http.ResponseWriter, r *http.Request, up *models.PresignedUpload, numbers []int) {
	ctx := r.Context()

	parts := make([]PresignedPart, 0, len(numbers))

	var expires time.Time

	for _, n := range numbers {
		length := storage.PartLength(up.SizeBytes, up.PartSize, n)

		put, err := h.multipart.PresignStagingPart(ctx, up.ID, up.MultipartID, n, length, h.urlTTL)
		if err != nil {
			h.Log.Errorw("presigned upload: failed to presign part", "upload_id", up.ID, "part", n, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

			return
		}

		expires = put.Expires

		parts = append(parts, PresignedPart{PartNumber: n, UploadURL: put.URL, Method: http.MethodPut, Headers: put.Headers, Size: length})
	}

	if err := h.DB.ExtendPresignedUploadExpiry(ctx, up.ID, time.Now().Add(h.urlTTL+presignedUploadGrace)); err != nil {
		h.Log.Warnw("presigned upload: failed to extend upload", "upload_id", up.ID, "error", err)
	}

	// The URLs are bearer credentials: never cache this response.
	w.Header().Set("Cache-Control", "no-store")

	writeJSON(w, http.StatusOK, PresignedPartsResponse{Parts: parts, ExpiresAt: expires})
}

// handleAbort discards an upload that will not be completed: its staging
// object, unfinished parts and row. Only the creator or an administrator can.
func (h *PresignedUploadHandler) handleAbort(w http.ResponseWriter, r *http.Request) {
	repo, up := h.authorizedUpload(w, r)
	if up == nil {
		return
	}

	if up.CompletedAt != nil {
		writeError(w, http.StatusConflict, "upload_completed", "the upload is already completed")
		return
	}

	// Take the lease like complete does: a verification in progress must not
	// lose its staging object under it.
	claimed, err := h.DB.ClaimPresignedUpload(r.Context(), repo.ID, up.ID, completeTimeout(up.SizeBytes)+time.Minute)
	if err != nil {
		h.Log.Errorw("presigned upload: failed to claim upload", "upload_id", up.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")

		return
	}

	if !claimed {
		w.Header().Set("Retry-After", presignedRetryAfter)
		writeError(w, http.StatusConflict, "upload_in_progress", "this upload is being completed; retry shortly")

		return
	}

	h.forgetUpload(up.ID)
	w.WriteHeader(http.StatusNoContent)
}

// assembleParts turns the uploaded parts of a multipart upload into the staging
// object; single-PUT uploads need nothing. A part that is missing leaves the
// upload retryable.
func (h *PresignedUploadHandler) assembleParts(ctx context.Context, up *models.PresignedUpload) *completeFailure {
	if up.PartSize == 0 {
		return nil
	}

	if h.multipart == nil {
		return &completeFailure{http.StatusNotImplemented, "presign_unsupported", "the storage backend does not support multipart uploads"}
	}

	err := h.multipart.CompleteMultipart(ctx, up.ID, up.MultipartID, up.SizeBytes, up.PartSize)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrMultipartIncomplete):
		h.release(ctx, up.ID)

		return &completeFailure{http.StatusConflict, "upload_incomplete", err.Error()}
	default:
		h.Log.Errorw("presigned upload: failed to assemble parts", "upload_id", up.ID, "error", err)
		h.release(ctx, up.ID)

		return failInternal()
	}
}
