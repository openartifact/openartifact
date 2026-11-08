// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
)

const (
	// uploadLeaseTTL is how long a request owns an upload session without
	// renewing; a request renews every uploadLeaseTTL/4 while it runs, so a
	// replica that dies mid-request frees the session within this time.
	uploadLeaseTTL = 2 * time.Minute

	// uploadLeaseRenewTimeout bounds one renewal.
	uploadLeaseRenewTimeout = 10 * time.Second

	// uploadCleanupTimeout bounds the best-effort removal of an upload's data.
	uploadCleanupTimeout = time.Minute
)

var (
	// errUploadMoved means the session's position was not what this request
	// started from when it tried to advance it: another request or replica got
	// there first, or the session was deleted.
	errUploadMoved = errors.New("upload changed concurrently")

	// errUploadLeaseLost means this request no longer owns the session (it could
	// not renew in time and another request took it over).
	errUploadLeaseLost = errors.New("upload lease lost")
)

// uploadSession is the exclusive claim one request holds on an upload session.
// The session row carries everything a request needs to continue it on any
// replica: the offset, the sha256 state of the bytes before it and the storage
// backend's own position token. While a request streams a body into the upload
// no other request, on this or any other replica, may touch it, because the
// stored bytes and the running digest must never diverge; the lease in the
// database is what enforces that across replicas.
type uploadSession struct {
	h      *HTTPHandler
	repoID string
	uuid   string
	token  string
	// upload is the row as of the moment the lease was taken.
	upload *models.DockerUpload
	// ctx ends with the request or when the lease is lost.
	ctx    context.Context //nolint:containedctx // the session is scoped to one request
	cancel context.CancelCauseFunc

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// acquireUpload claims the session for this request. On failure it has written
// the error response: 404 for an unknown session (or one started for another
// image), 409 while another request holds it (the client asks for the status
// and resumes).
func (h *HTTPHandler) acquireUpload(w http.ResponseWriter, r *http.Request, repoID, image, uploadUUID string) (*uploadSession, bool) {
	token := uuid.NewString()

	upload, err := h.db.AcquireDockerUploadLease(r.Context(), repoID, image, uploadUUID, token, uploadLeaseTTL)
	if err != nil {
		h.log.Errorw("failed to claim upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return nil, false
	}

	if upload == nil {
		existing, err := h.db.GetDockerUpload(r.Context(), repoID, image, uploadUUID)

		switch {
		case err != nil:
			h.log.Errorw("failed to get upload", "uuid", uploadUUID, "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		case existing == nil:
			h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		default:
			w.Header().Set("Range", uploadRange(existing.Offset))
			h.errorResponse(
				w,
				http.StatusConflict,
				"BLOB_UPLOAD_INVALID",
				"upload is in use by another request; query its status and resume",
			)
		}

		return nil, false
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	s := &uploadSession{
		h:      h,
		repoID: repoID,
		uuid:   uploadUUID,
		token:  token,
		upload: upload,
		ctx:    ctx,
		cancel: cancel,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}

	go s.keepAlive(uploadLeaseTTL / 4)

	return s, true
}

// keepAlive renews the lease until the session is released. A renewal that
// fails for another reason than a lost lease is retried at the next tick: the
// lease has time to spare for a few of them.
func (s *uploadSession) keepAlive(every time.Duration) {
	defer close(s.done)

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}

		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), uploadLeaseRenewTimeout)
		renewed, err := s.h.db.RenewDockerUploadLease(ctx, s.repoID, s.uuid, s.token, uploadLeaseTTL)

		cancel()

		switch {
		case err != nil:
			s.h.log.Warnw("failed to renew upload lease", "uuid", s.uuid, "error", err)
		case !renewed:
			s.cancel(errUploadLeaseLost)

			return
		}
	}
}

// release hands the session back. It is safe after the session row is gone and
// after a position update that already released the lease.
func (s *uploadSession) release() {
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.done
		s.cancel(nil)

		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), uploadLeaseRenewTimeout)
		defer cancel()

		if err := s.h.db.ReleaseDockerUploadLease(ctx, s.repoID, s.uuid, s.token); err != nil {
			s.h.log.Warnw("failed to release upload lease", "uuid", s.uuid, "error", err)
		}
	})
}

// position is where the session stood when it was claimed.
func (s *uploadSession) position() storage.UploadPosition {
	return storage.UploadPosition{State: s.upload.StageState, Offset: s.upload.Offset}
}

// resumeDigest rebuilds the running sha256 of the bytes already in the upload.
// errUploadDataLost when the session has bytes but no usable digest state, as a
// session opened by a release that kept chunks in separate objects does.
func (s *uploadSession) resumeDigest() (hash.Hash, error) {
	h := sha256.New()
	if s.upload.Offset == 0 {
		return h, nil
	}

	state := s.upload.HashState

	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok || len(state) < 8 {
		return nil, fmt.Errorf("%w: no digest state at offset %d", storage.ErrUploadDataLost, s.upload.Offset)
	}

	if err := u.UnmarshalBinary(state); err != nil {
		return nil, fmt.Errorf("%w: digest state: %w", storage.ErrUploadDataLost, err)
	}

	// The state ends with the number of bytes hashed; a state that does not
	// match the offset would verify other bytes than the ones stored.
	if n := binary.BigEndian.Uint64(state[len(state)-8:]); n != uint64(s.upload.Offset) { //nolint:gosec // offsets are never negative
		return nil, fmt.Errorf("%w: digest covers %d bytes, upload holds %d", storage.ErrUploadDataLost, n, s.upload.Offset)
	}

	return h, nil
}

// append streams body into the upload, hashing it on the way, and records the
// new position (offset, digest state, storage token) in one atomic update. With
// keepLease the session stays claimed afterwards. It returns the position after
// the append, the starting one when body was empty.
func (s *uploadSession) append(body io.Reader, digest hash.Hash, keepLease bool) (storage.UploadPosition, error) {
	at := s.position()
	pos := at

	err := s.h.uploads.AppendUpload(s.ctx, s.uuid, at, io.TeeReader(body, digest),
		func(ctx context.Context, state string, n int64) error {
			sum, err := digestState(digest)
			if err != nil {
				return err
			}

			moved, err := s.h.db.AdvanceDockerUpload(
				ctx,
				s.repoID,
				s.uuid,
				s.token,
				at.Offset,
				at.Offset+n,
				sum,
				state,
				uploadLeaseTTL,
				keepLease,
			)
			if err != nil {
				return fmt.Errorf("record upload position: %w", err)
			}

			if !moved {
				return errUploadMoved
			}

			pos = storage.UploadPosition{State: state, Offset: at.Offset + n}

			return nil
		})
	if err != nil {
		if errors.Is(context.Cause(s.ctx), errUploadLeaseLost) {
			return at, errUploadLeaseLost
		}

		return at, fmt.Errorf("append to upload: %w", err)
	}

	return pos, nil
}

// digestState marshals the running sha256.
func digestState(digest hash.Hash) ([]byte, error) {
	m, ok := digest.(encoding.BinaryMarshaler)
	if !ok {
		return nil, errors.New("sha256 state cannot be saved") //nolint:err113 // cannot happen with the standard library
	}

	state, err := m.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("save digest state: %w", err)
	}

	return state, nil
}

// checkChunkStart answers 416 with the current Range when a request's declared
// Content-Range does not start where the upload stands, and 400 for a header
// that does not parse; it reports whether the request may go on.
func (h *HTTPHandler) checkChunkStart(w http.ResponseWriter, r *http.Request, name, uploadUUID string, offset int64) bool {
	cr := r.Header.Get("Content-Range")
	if cr == "" {
		return true
	}

	start, _, rangeErr := parseContentRange(cr)
	if rangeErr != nil {
		h.errorResponse(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", rangeErr.Error())
		return false
	}

	if offsetErr := validateChunkOffset(offset, start); offsetErr != nil {
		h.log.Warnw("chunk offset mismatch", "uuid", uploadUUID, "current_offset", offset, "start", start)
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
		w.Header().Set("Range", uploadRange(offset))
		h.errorResponse(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", offsetErr.Error())

		return false
	}

	return true
}

// appendFailed answers a failed append.
func (h *HTTPHandler) appendFailed(w http.ResponseWriter, r *http.Request, repoID, uploadUUID string, err error) {
	switch {
	case errors.Is(err, errUploadMoved), errors.Is(err, errUploadLeaseLost):
		h.errorResponse(w, http.StatusConflict, "BLOB_UPLOAD_INVALID", "upload changed concurrently; query its status and resume")
	case isOverQuota(err):
		h.refuseOverQuota(w, r, repoID, uploadUUID)
	case errors.Is(err, storage.ErrUploadDataLost):
		h.uploadDataLost(w, uploadUUID, err)
	case errors.Is(err, storage.ErrUploadTooLarge):
		h.errorResponse(w, http.StatusRequestEntityTooLarge, "BLOB_UPLOAD_INVALID", "blob is too large for the storage backend")
	default:
		h.log.Errorw("failed to store upload data", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
	}
}

// isOverQuota reports a body the quota-bounded reader cut off.
func isOverQuota(err error) bool {
	_, over := errors.AsType[*http.MaxBytesError](err)

	return over
}

// handleUploadChunk appends data to an in-progress upload. The body is
// streamed into the session's upload object while its sha256 is updated, and
// the new offset, digest state and storage position are recorded together; no
// replica keeps any state of the session, so the next request may land on
// another one.
func (h *HTTPHandler) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	// Requests on one session that reach this replica run one at a time, so a
	// client that retries a slow request waits for it instead of being refused.
	// Requests on other replicas are kept apart by the lease below.
	unlock, ok := h.lockUpload(w, r, repo.ID, uploadUUID)
	if !ok {
		return
	}
	defer unlock()

	sess, ok := h.acquireUpload(w, r, repo.ID, image, uploadUUID)
	if !ok {
		return
	}
	defer sess.release()

	defer func() { _ = r.Body.Close() }()

	digest, err := sess.resumeDigest()
	if err != nil {
		h.uploadDataLost(w, uploadUUID, err)
		return
	}

	// A chunk's declared start MUST be contiguous with what the server has
	// already accepted — no gap, no overlap. Reject before writing anything.
	if !h.checkChunkStart(w, r, name, uploadUUID, sess.upload.Offset) {
		return
	}

	body, room := h.quotaBoundedBody(w, r, repo.ID, sess.upload.Offset, "")
	if !room {
		h.refuseOverQuota(w, r, repo.ID, uploadUUID)
		return
	}

	pos, err := sess.append(body, digest, false)
	if err != nil {
		h.appendFailed(w, r, repo.ID, uploadUUID, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", uploadRange(pos.Offset))
	w.Header().Set("Docker-Upload-Uuid", uploadUUID)
	w.WriteHeader(http.StatusAccepted)
}

// deleteUpload deletes an upload session and the data it holds. The session
// row goes first, so a failure to remove the data only leaves it for the sweep
// of uploads no session owns.
func (h *HTTPHandler) deleteUpload(ctx context.Context, repoID, uploadUUID string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadCleanupTimeout)
	defer cancel()

	if err := h.db.DeleteDockerUpload(ctx, repoID, uploadUUID); err != nil {
		return fmt.Errorf("delete upload record: %w", err)
	}

	if err := h.uploads.DeleteUpload(ctx, uploadUUID); err != nil {
		h.log.Warnw("failed to delete upload data", "uuid", uploadUUID, "error", err)
	}

	return nil
}

// discardUpload is deleteUpload for a request that has already decided its
// answer: a failure is logged, not reported.
func (h *HTTPHandler) discardUpload(ctx context.Context, repoID, uploadUUID string) {
	if err := h.deleteUpload(ctx, repoID, uploadUUID); err != nil {
		h.log.Warnw("failed to discard upload", "uuid", uploadUUID, "error", err)
	}
}

// refuseOverQuota answers a push the quota has no room for with 507 and
// discards the upload, like a completion the reservation refused.
func (h *HTTPHandler) refuseOverQuota(w http.ResponseWriter, r *http.Request, repoID, uploadUUID string) {
	h.discardUpload(r.Context(), repoID, uploadUUID)
	h.quotaExceeded(w)
}

// uploadDataLost answers a request on a session whose data is gone.
func (h *HTTPHandler) uploadDataLost(w http.ResponseWriter, uploadUUID string, err error) {
	h.log.Errorw("upload data lost", "uuid", uploadUUID, "error", err)
	h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload data lost; restart the upload")
}

// handleCompleteBlobUpload finalizes a blob upload: the final bytes in the
// request body (if any) are appended, the running digest is checked against
// the one the client claims, and the upload object becomes the blob without
// being read back: the bytes that were hashed are the bytes that were stored.
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

	unlock, ok := h.lockUpload(w, r, repo.ID, uploadUUID)
	if !ok {
		return
	}
	defer unlock()

	// The session stays claimed to the end of the request: a chunk appended
	// through another replica meanwhile could not be part of what is verified
	// and stored, but it would overwrite stored bytes the digest covers.
	sess, ok := h.acquireUpload(w, r, repo.ID, image, uploadUUID)
	if !ok {
		return
	}
	defer sess.release()

	defer func() { _ = r.Body.Close() }()

	running, err := sess.resumeDigest()
	if err != nil {
		h.uploadDataLost(w, uploadUUID, err)
		return
	}

	// A final chunk's declared start MUST be contiguous with what the server
	// has already accepted, same as PATCH.
	if !h.checkChunkStart(w, r, name, uploadUUID, sess.upload.Offset) {
		return
	}

	// Any final bytes in the request body are appended like a PATCH. For a
	// monolithic upload (a single PUT carrying the whole blob) the body is all
	// there is, and it is never buffered in memory.
	body, room := h.quotaBoundedBody(w, r, repo.ID, sess.upload.Offset, digest)
	if !room {
		h.refuseOverQuota(w, r, repo.ID, uploadUUID)
		return
	}

	pos, err := sess.append(body, running, true)
	if err != nil {
		h.appendFailed(w, r, repo.ID, uploadUUID, err)
		return
	}

	computedHex := hex.EncodeToString(running.Sum(nil))

	if "sha256:"+computedHex != digest {
		h.log.Warnw("digest mismatch", "expected", digest, "computed", "sha256:"+computedHex)
		h.errorResponse(w, http.StatusBadRequest, "DIGEST_INVALID", "computed digest does not match")
		h.discardUpload(r.Context(), repo.ID, uploadUUID)

		return
	}

	object, err := h.uploads.CompleteUpload(sess.ctx, uploadUUID, pos)
	if err != nil {
		if errors.Is(err, storage.ErrUploadDataLost) {
			h.uploadDataLost(w, uploadUUID, err)
			return
		}

		h.log.Errorw("failed to complete upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	h.commitUpload(w, r, repo.ID, image, name, uploadUUID, digest, computedHex, object)
}

// commitUpload stores the completed, verified upload as the blob and answers
// the completion. The upload object was verified as it was written, so the
// blob store promotes it in place (a server-side copy or link) where it can;
// otherwise the object is streamed into Put, which hashes it again.
func (h *HTTPHandler) commitUpload(
	w http.ResponseWriter,
	r *http.Request,
	repoID, image, name, uploadUUID, digest, computedHex string,
	object storage.StagedObject,
) {
	blob := &models.DockerBlob{
		ID:           uuid.New().String(),
		RepositoryID: repoID,
		Digest:       digest,
		MediaType:    "application/octet-stream",
		Size:         object.Size,
		Image:        &image,
		CreatedAt:    time.Now(),
	}

	meta := map[string]string{"sha256": computedHex, "digest": digest}

	var opened []io.Closer

	defer func() {
		for _, c := range opened {
			_ = c.Close()
		}
	}()

	// The bytes are stored first and the per-blob lock covers only the existence
	// re-check, quota reservation and insert, so a concurrent unpublish cannot
	// delete the blob in between. Content-addressed by sha256: every BlobStore
	// backend keys (and some, like S3/Azure/OCI, require) storage on the "sha256"
	// metadata entry — not the OCI-prefixed "digest" string.
	staged := storage.Blob{
		SHA256:   computedHex,
		Metadata: meta,
		Open: func() (io.Reader, error) {
			rc, err := h.uploads.OpenUpload(r.Context(), uploadUUID, object.Version)
			if err != nil {
				return nil, fmt.Errorf("open upload: %w", err)
			}

			opened = append(opened, rc)

			return rc, nil
		},
		Promote: func(ctx context.Context) error {
			return h.uploads.PromoteUpload(ctx, uploadUUID, computedHex, object.Version, meta)
		},
	}

	if err := storage.CommitBlob(r.Context(), h.db, h.blobStore, staged, func(ctx context.Context, _ string) error {
		// A blob the repository already holds is not stored again: pushing an
		// existing digest completes with 201, as the distribution spec requires.
		held, err := h.blobAlreadyHeld(ctx, repoID, image, digest)
		if err != nil {
			return err
		}

		if held {
			return nil
		}

		return h.storeNewBlob(ctx, blob, computedHex)
	}); err != nil {
		h.commitFailed(w, r, repoID, uploadUUID, computedHex, digest, err)
		return
	}

	// Remove the upload's data and the session record.
	h.discardUpload(r.Context(), repoID, uploadUUID)

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// commitFailed answers a completion whose blob could not be stored.
func (h *HTTPHandler) commitFailed(w http.ResponseWriter, r *http.Request, repoID, uploadUUID, computedHex, digest string, err error) {
	switch {
	case errors.Is(err, storage.ErrDigestMismatch), errors.Is(err, storage.ErrStagingChanged):
		// The upload object is no longer the bytes that were verified; nothing
		// was stored under the digest and the upload is discarded.
		h.log.Warnw("upload changed while completing", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusBadRequest, "DIGEST_INVALID", "computed digest does not match")
		h.discardUpload(r.Context(), repoID, uploadUUID)
	case errors.Is(err, storage.ErrStagingNotFound):
		h.uploadDataLost(w, uploadUUID, err)
	case errors.Is(err, quota.ErrExceeded):
		// No row was recorded; the object stored ahead of the commit is
		// unreferenced and the upload would only wait for the stale-session
		// sweep, so drop both and discard the session.
		h.discardOrphan(r.Context(), computedHex)
		h.quotaExceeded(w)
		h.discardUpload(r.Context(), repoID, uploadUUID)
	default:
		h.discardOrphan(r.Context(), computedHex)
		h.log.Errorw("failed to store blob", "digest", digest, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		// The upload file may already be hard-linked as the blob under its
		// digest: a later PATCH would append to that shared inode. Drop the
		// session even when the client has gone away.
		h.discardUpload(context.WithoutCancel(r.Context()), repoID, uploadUUID)
	}
}

// handleCancelBlobUpload cancels an in-progress upload.
func (h *HTTPHandler) handleCancelBlobUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]
	uploadUUID := vars["uuid"]

	repo, image, ok := h.target(w, r, name)
	if !ok {
		return
	}

	unlock, ok := h.lockUpload(w, r, repo.ID, uploadUUID)
	if !ok {
		return
	}
	defer unlock()

	upload, err := h.db.GetDockerUpload(r.Context(), repo.ID, image, uploadUUID)
	if err != nil {
		h.log.Errorw("failed to get upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	if upload == nil {
		h.errorResponse(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}

	// Cancelling does not wait for a request another replica is serving: that
	// request finds the session gone when it records its position, and the data
	// it left behind is swept.
	if err := h.deleteUpload(r.Context(), repo.ID, uploadUUID); err != nil {
		h.log.Errorw("failed to cancel upload", "uuid", uploadUUID, "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	w.WriteHeader(http.StatusNoContent)
}
