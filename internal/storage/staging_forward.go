// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"io"
	"time"
)

// StagingForwarder is the staging and multipart staging capability of a store
// that fronts another one: every call goes to the store target returns, and a
// target without the capability answers ErrPresignNotSupported. A wrapper
// embeds it and overrides the calls it has to intercept.
type StagingForwarder struct {
	target func() BlobStore
}

var (
	_ StagingStore          = StagingForwarder{}
	_ MultipartStagingStore = StagingForwarder{}
)

// NewStagingForwarder returns a forwarder to the store target returns; target
// is called on every request, so the store it names may change.
func NewStagingForwarder(target func() BlobStore) StagingForwarder {
	return StagingForwarder{target: target}
}

func (f StagingForwarder) staging() (StagingStore, bool) {
	st, ok := f.target().(StagingStore)

	return st, ok
}

func (f StagingForwarder) multipart() (MultipartStagingStore, bool) {
	m, ok := f.target().(MultipartStagingStore)

	return m, ok
}

// StagingSupported implements StagingStore.
func (f StagingForwarder) StagingSupported() bool {
	st, ok := f.staging()

	return ok && st.StagingSupported()
}

// PresignStagingPut implements StagingStore.
func (f StagingForwarder) PresignStagingPut(ctx context.Context, id string, size int64, ttl time.Duration) (PresignedPut, error) {
	st, ok := f.staging()
	if !ok {
		return PresignedPut{}, ErrPresignNotSupported
	}

	return st.PresignStagingPut(ctx, id, size, ttl)
}

// StatStaging implements StagingStore.
func (f StagingForwarder) StatStaging(ctx context.Context, id string) (StagedObject, error) {
	st, ok := f.staging()
	if !ok {
		return StagedObject{}, ErrPresignNotSupported
	}

	return st.StatStaging(ctx, id)
}

// OpenStaging implements StagingStore.
func (f StagingForwarder) OpenStaging(ctx context.Context, id, version string) (io.ReadCloser, error) {
	st, ok := f.staging()
	if !ok {
		return nil, ErrPresignNotSupported
	}

	return st.OpenStaging(ctx, id, version)
}

// PromoteStaging implements StagingStore.
func (f StagingForwarder) PromoteStaging(ctx context.Context, id, sha256, version string, metadata map[string]string) error {
	st, ok := f.staging()
	if !ok {
		return ErrPresignNotSupported
	}

	return st.PromoteStaging(ctx, id, sha256, version, metadata)
}

// DeleteStaging implements StagingStore.
func (f StagingForwarder) DeleteStaging(ctx context.Context, id string) error {
	st, ok := f.staging()
	if !ok {
		return ErrPresignNotSupported
	}

	return st.DeleteStaging(ctx, id)
}

// WalkStaging implements StagingStore.
func (f StagingForwarder) WalkStaging(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	st, ok := f.staging()
	if !ok {
		return ErrPresignNotSupported
	}

	return st.WalkStaging(ctx, olderThan, fn)
}

// MultipartSupported implements MultipartStagingStore.
func (f StagingForwarder) MultipartSupported() bool {
	m, ok := f.multipart()

	return ok && m.MultipartSupported()
}

// InitiateMultipart implements MultipartStagingStore.
func (f StagingForwarder) InitiateMultipart(ctx context.Context, id string, size, partSize int64) (string, error) {
	m, ok := f.multipart()
	if !ok {
		return "", ErrPresignNotSupported
	}

	return m.InitiateMultipart(ctx, id, size, partSize)
}

// PresignStagingPart implements MultipartStagingStore.
func (f StagingForwarder) PresignStagingPart(
	ctx context.Context,
	id, handle string,
	number int,
	length int64,
	ttl time.Duration,
) (PresignedPut, error) {
	m, ok := f.multipart()
	if !ok {
		return PresignedPut{}, ErrPresignNotSupported
	}

	return m.PresignStagingPart(ctx, id, handle, number, length, ttl)
}

// CompleteMultipart implements MultipartStagingStore.
func (f StagingForwarder) CompleteMultipart(ctx context.Context, id, handle string, size, partSize int64) error {
	m, ok := f.multipart()
	if !ok {
		return ErrPresignNotSupported
	}

	return m.CompleteMultipart(ctx, id, handle, size, partSize)
}
