// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
)

// Store is the blob store the server uses when a replica is configured. It
// wraps the primary (tiered) store: every blob written through it is queued for
// copying, every deletion for deletion on the replica, and reads the primary
// cannot answer are served from the replica (and repaired on the primary).
// Writing and deleting never wait for the replica.
type Store struct {
	*core

	repairs *repairer
}

var (
	_ storage.BlobStore             = (*Store)(nil)
	_ storage.Presigner             = (*Store)(nil)
	_ storage.StagingStore          = (*Store)(nil)
	_ storage.MultipartStagingStore = (*Store)(nil)
)

// Put writes the blob to the primary store and queues its copy.
func (s *Store) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	id, err := s.inner.Put(ctx, data, metadata)
	if err != nil {
		return id, err //nolint:wrapcheck // transparent wrapper
	}

	s.enqueueWritten(ctx, id)

	return id, nil
}

// enqueueWritten queues the copy of a blob that was just written. A failure is
// logged and counted, not returned: the blob is safely stored, and the verify
// pass finds a blob that never made it into the queue.
func (s *Store) enqueueWritten(ctx context.Context, id string) {
	if err := s.enqueue(ctx, id, database.BlobReplicationCopy); err != nil {
		s.log.Errorw("could not queue blob for replication; the next verify pass will copy it", "sha256", id, "error", err)
	}
}

// Delete removes the blob from the primary store and queues the deletion of
// its replica copy. The replica copy outlives the primary one until the
// deletion is applied; the worker skips it when the blob is back or still
// referenced. A failure to queue is returned so the caller retries the delete.
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := s.inner.Delete(ctx, id); err != nil {
		return err //nolint:wrapcheck // transparent wrapper
	}

	return s.enqueue(ctx, id, database.BlobReplicationDelete)
}

// DeleteCopy removes the copy of a blob one storage backend holds (a retired
// tier copy). The replica is not touched: the blob itself still exists.
func (s *Store) DeleteCopy(ctx context.Context, backendID, id string) error {
	d, ok := s.inner.(interface {
		DeleteCopy(ctx context.Context, backendID, id string) error
	})
	if !ok {
		return storage.ErrPresignNotSupported
	}

	return d.DeleteCopy(ctx, backendID, id) //nolint:wrapcheck // transparent wrapper
}

// List lists the primary store; the replica is never consulted.
func (s *Store) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	return s.inner.List(ctx, token, limit) //nolint:wrapcheck // transparent wrapper
}

// canFallBack reports whether a failed primary call may be answered by the
// replica: never when the mode is off, never when the caller's own context
// ended or the id is invalid, and for errors other than "not found" only in
// the "error" mode.
func (s *Store) canFallBack(ctx context.Context, err error) bool {
	if s.opts.ReadFallback == storage.ReadFallbackOff || ctx.Err() != nil || errors.Is(err, storage.ErrInvalidBlobID) {
		return false
	}

	if err == nil || isNotFound(err) {
		return true
	}

	return s.opts.ReadFallback == storage.ReadFallbackError
}

// fallbackOK decides whether the replica may answer for id: a blob whose
// deletion is queued stays gone. When the queue cannot be read the replica may
// still answer (a read is harmless) but nothing is repaired. It returns whether
// the replica may answer and whether the primary should then be repaired.
func (s *Store) fallbackOK(ctx context.Context, id string) (bool, bool) {
	if storage.ValidateBlobID(id) != nil {
		return false, false
	}

	del, err := s.hasPendingDelete(ctx, id)
	if err != nil {
		s.log.Warnw("could not read the replication queue; serving from the replica without repair", "sha256", id, "error", err)

		return true, false
	}

	return !del, !del
}

// served records a read answered by the replica and schedules the primary's
// repair when the primary reported the blob missing.
func (s *Store) served(call, id string, repair bool, primaryErr error) {
	s.m.FallbackReads.WithLabelValues(call).Inc()
	s.log.Warnw("blob served from the replica", "call", call, "sha256", id, "primary_error", errString(primaryErr))

	if repair && (primaryErr == nil || isNotFound(primaryErr)) {
		s.repairs.schedule(id)
	}
}

func errString(err error) string {
	if err == nil {
		return "not found"
	}

	return err.Error()
}

// Get reads the blob from the primary store, from the replica when it is not
// there (see Options.ReadFallback).
func (s *Store) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	rc, err := s.inner.Get(ctx, id)
	if err == nil || !s.canFallBack(ctx, err) {
		return rc, err //nolint:wrapcheck // transparent wrapper
	}

	serve, repair := s.fallbackOK(ctx, id)
	if !serve {
		return nil, err //nolint:wrapcheck // transparent wrapper
	}

	rrc, rerr := s.replica.Get(ctx, id)
	if rerr != nil {
		s.logReplicaMiss("get", id, rerr)

		return nil, err //nolint:wrapcheck // the primary's error is the informative one
	}

	s.served("get", id, repair, err)

	return rrc, nil
}

// GetSize returns the blob's size from the primary store or the replica.
func (s *Store) GetSize(ctx context.Context, id string) (int64, error) {
	n, err := s.inner.GetSize(ctx, id)
	if err == nil || !s.canFallBack(ctx, err) {
		return n, err //nolint:wrapcheck // transparent wrapper
	}

	serve, repair := s.fallbackOK(ctx, id)
	if !serve {
		return 0, err //nolint:wrapcheck // transparent wrapper
	}

	rn, rerr := s.replica.GetSize(ctx, id)
	if rerr != nil {
		s.logReplicaMiss("size", id, rerr)

		return 0, err //nolint:wrapcheck // the primary's error is the informative one
	}

	s.served("size", id, repair, err)

	return rn, nil
}

// GetMetadata returns the blob's metadata from the primary store or the replica.
func (s *Store) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	md, err := s.inner.GetMetadata(ctx, id)
	if err == nil {
		return md, nil
	}

	cause := s.metadataMissCause(ctx, id, err)
	if !s.canFallBack(ctx, cause) {
		return nil, err //nolint:wrapcheck // transparent wrapper
	}

	serve, repair := s.fallbackOK(ctx, id)
	if !serve {
		return nil, err //nolint:wrapcheck // transparent wrapper
	}

	rmd, rerr := s.replica.GetMetadata(ctx, id)
	if rerr != nil {
		s.logReplicaMiss("metadata", id, rerr)

		return nil, err //nolint:wrapcheck // the primary's error is the informative one
	}

	s.served("metadata", id, repair, cause)

	return rmd, nil
}

// metadataMissCause classifies a failed metadata read: not every backend
// reports a missing blob as ErrBlobNotFound there (the filesystem store says
// "metadata not found"), so a failure for a blob the primary does not have
// counts as not found.
func (s *Store) metadataMissCause(ctx context.Context, id string, err error) error {
	if isNotFound(err) || s.opts.ReadFallback == storage.ReadFallbackOff || ctx.Err() != nil {
		return err
	}

	if ok, eerr := s.inner.Exists(ctx, id); eerr == nil && !ok {
		return storage.ErrBlobNotFound
	}

	return err
}

// Exists reports whether the blob is on the primary store or, failing that, on
// the replica. A hit on the replica queues the primary's repair: callers skip
// their upload when the blob "exists", so a blob only the replica has would
// otherwise never be written back.
func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	ok, err := s.inner.Exists(ctx, id)
	if ok || !s.canFallBack(ctx, err) {
		return ok, err //nolint:wrapcheck // transparent wrapper
	}

	serve, repair := s.fallbackOK(ctx, id)
	if !serve {
		return ok, err //nolint:wrapcheck // transparent wrapper
	}

	rok, rerr := s.replica.Exists(ctx, id)
	if rerr != nil || !rok {
		if rerr != nil {
			s.logReplicaMiss("exists", id, rerr)
		}

		return ok, err //nolint:wrapcheck // the primary's answer stands
	}

	s.served("exists", id, repair, err)

	return true, nil
}

// ExistsBySHA256 looks the blob up on the primary store, then on the replica.
func (s *Store) ExistsBySHA256(ctx context.Context, sha string) (string, bool, error) {
	id, ok, err := s.inner.ExistsBySHA256(ctx, sha)
	if ok || !s.canFallBack(ctx, err) {
		return id, ok, err //nolint:wrapcheck // transparent wrapper
	}

	serve, repair := s.fallbackOK(ctx, sha)
	if !serve {
		return id, ok, err //nolint:wrapcheck // transparent wrapper
	}

	rid, rok, rerr := s.replica.ExistsBySHA256(ctx, sha)
	if rerr != nil || !rok {
		if rerr != nil {
			s.logReplicaMiss("exists", sha, rerr)
		}

		return id, ok, err //nolint:wrapcheck // the primary's answer stands
	}

	s.served("exists", sha, repair, err)

	return rid, true, nil
}

func (s *Store) logReplicaMiss(call, id string, err error) {
	if !isNotFound(err) {
		s.log.Warnw("replica could not answer a fallback read", "call", call, "sha256", id, "error", err)
	}
}

// presignable reports whether a presigned download of the blob may be handed
// out: the primary must hold it. A blob only the replica has is streamed
// through the server instead (Get), where it is served and repaired. The check
// costs one HEAD per presigned download.
func (s *Store) presignable(ctx context.Context, id string) error {
	if s.opts.ReadFallback == storage.ReadFallbackOff {
		return nil
	}

	ok, err := s.inner.Exists(ctx, id)
	if err != nil || !ok {
		return storage.ErrPresignNotSupported
	}

	return nil
}

// PresignURL implements storage.BlobStore.
func (s *Store) PresignURL(ctx context.Context, sha string, ttl time.Duration) (string, error) {
	if err := s.presignable(ctx, sha); err != nil {
		return "", err
	}

	return s.inner.PresignURL(ctx, sha, ttl) //nolint:wrapcheck // transparent wrapper
}

// PresignGet implements storage.Presigner.
func (s *Store) PresignGet(ctx context.Context, sha string, ttl time.Duration, opts storage.PresignGetOptions) (string, error) {
	p, ok := s.inner.(storage.Presigner)
	if !ok {
		return "", storage.ErrPresignNotSupported
	}

	if err := s.presignable(ctx, sha); err != nil {
		return "", err
	}

	return p.PresignGet(ctx, sha, ttl, opts) //nolint:wrapcheck // transparent wrapper
}

func (s *Store) staging() (storage.StagingStore, bool) {
	st, ok := s.inner.(storage.StagingStore)

	return st, ok
}

// StagingSupported implements storage.StagingStore.
func (s *Store) StagingSupported() bool {
	st, ok := s.staging()

	return ok && st.StagingSupported()
}

// PresignStagingPut implements storage.StagingStore.
func (s *Store) PresignStagingPut(ctx context.Context, id string, size int64, ttl time.Duration) (storage.PresignedPut, error) {
	st, ok := s.staging()
	if !ok {
		return storage.PresignedPut{}, storage.ErrPresignNotSupported
	}

	return st.PresignStagingPut(ctx, id, size, ttl) //nolint:wrapcheck // transparent wrapper
}

// StatStaging implements storage.StagingStore.
func (s *Store) StatStaging(ctx context.Context, id string) (storage.StagedObject, error) {
	st, ok := s.staging()
	if !ok {
		return storage.StagedObject{}, storage.ErrPresignNotSupported
	}

	return st.StatStaging(ctx, id) //nolint:wrapcheck // transparent wrapper
}

// OpenStaging implements storage.StagingStore.
func (s *Store) OpenStaging(ctx context.Context, id, version string) (io.ReadCloser, error) {
	st, ok := s.staging()
	if !ok {
		return nil, storage.ErrPresignNotSupported
	}

	return st.OpenStaging(ctx, id, version) //nolint:wrapcheck // transparent wrapper
}

// PromoteStaging promotes the staged upload to its blob and queues the copy:
// this is where a client-direct upload becomes a blob.
func (s *Store) PromoteStaging(ctx context.Context, id, sha, version string, metadata map[string]string) error {
	st, ok := s.staging()
	if !ok {
		return storage.ErrPresignNotSupported
	}

	if err := st.PromoteStaging(ctx, id, sha, version, metadata); err != nil {
		return err //nolint:wrapcheck // transparent wrapper
	}

	s.enqueueWritten(ctx, sha)

	return nil
}

// DeleteStaging implements storage.StagingStore.
func (s *Store) DeleteStaging(ctx context.Context, id string) error {
	st, ok := s.staging()
	if !ok {
		return storage.ErrPresignNotSupported
	}

	return st.DeleteStaging(ctx, id) //nolint:wrapcheck // transparent wrapper
}

// WalkStaging implements storage.StagingStore.
func (s *Store) WalkStaging(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	st, ok := s.staging()
	if !ok {
		return storage.ErrPresignNotSupported
	}

	return st.WalkStaging(ctx, olderThan, fn) //nolint:wrapcheck // transparent wrapper
}

func (s *Store) multipart() (storage.MultipartStagingStore, bool) {
	m, ok := s.inner.(storage.MultipartStagingStore)

	return m, ok
}

// MultipartSupported implements storage.MultipartStagingStore.
func (s *Store) MultipartSupported() bool {
	m, ok := s.multipart()

	return ok && m.MultipartSupported()
}

// InitiateMultipart implements storage.MultipartStagingStore.
func (s *Store) InitiateMultipart(ctx context.Context, id string, size, partSize int64) (string, error) {
	m, ok := s.multipart()
	if !ok {
		return "", storage.ErrPresignNotSupported
	}

	return m.InitiateMultipart(ctx, id, size, partSize) //nolint:wrapcheck // transparent wrapper
}

// PresignStagingPart implements storage.MultipartStagingStore.
func (s *Store) PresignStagingPart(ctx context.Context, id, handle string, number int, length int64, ttl time.Duration) (storage.PresignedPut, error) {
	m, ok := s.multipart()
	if !ok {
		return storage.PresignedPut{}, storage.ErrPresignNotSupported
	}

	return m.PresignStagingPart(ctx, id, handle, number, length, ttl) //nolint:wrapcheck // transparent wrapper
}

// CompleteMultipart implements storage.MultipartStagingStore. It only
// assembles the staging object; the blob (and its queued copy) comes with
// PromoteStaging.
func (s *Store) CompleteMultipart(ctx context.Context, id, handle string, size, partSize int64) error {
	m, ok := s.multipart()
	if !ok {
		return storage.ErrPresignNotSupported
	}

	return m.CompleteMultipart(ctx, id, handle, size, partSize) //nolint:wrapcheck // transparent wrapper
}
