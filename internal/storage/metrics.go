// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"io"
	"time"

	"github.com/openartifact/openartifact/internal/metrics"
)

type countingReader struct {
	r     io.Reader
	count int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.count += int64(n)

	return n, err
}

type MetricsBlobStore struct {
	inner BlobStore
	reg   *metrics.Registry
}

func NewMetricsBlobStore(inner BlobStore, reg *metrics.Registry) *MetricsBlobStore {
	return &MetricsBlobStore{inner: inner, reg: reg}
}

func (m *MetricsBlobStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	cr := &countingReader{r: data}

	id, err := m.inner.Put(ctx, cr, metadata)
	if err == nil {
		m.reg.BlobBytesTotal.WithLabelValues("put").Add(float64(cr.count))
	}

	return id, err
}

func (m *MetricsBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	rc, err := m.inner.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	return &countingReadCloser{ReadCloser: rc, reg: m.reg}, nil
}

func (m *MetricsBlobStore) Delete(ctx context.Context, id string) error {
	return m.inner.Delete(ctx, id)
}

func (m *MetricsBlobStore) Exists(ctx context.Context, id string) (bool, error) {
	return m.inner.Exists(ctx, id)
}

func (m *MetricsBlobStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	return m.inner.GetMetadata(ctx, id)
}

func (m *MetricsBlobStore) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	return m.inner.List(ctx, token, limit)
}

func (m *MetricsBlobStore) GetSize(ctx context.Context, id string) (int64, error) {
	return m.inner.GetSize(ctx, id)
}

func (m *MetricsBlobStore) ExistsBySHA256(ctx context.Context, sha256 string) (string, bool, error) {
	return m.inner.ExistsBySHA256(ctx, sha256)
}

func (m *MetricsBlobStore) PresignURL(ctx context.Context, sha256 string, ttl time.Duration) (string, error) {
	return m.inner.PresignURL(ctx, sha256, ttl)
}

type countingReadCloser struct {
	io.ReadCloser

	reg   *metrics.Registry
	count int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)

	c.count += int64(n)
	if err == io.EOF {
		c.reg.BlobBytesTotal.WithLabelValues("get").Add(float64(c.count))
	}

	return n, err
}

// PresignGet forwards to the wrapped store when it can sign download URLs.
func (m *MetricsBlobStore) PresignGet(ctx context.Context, sha256 string, ttl time.Duration, opts PresignGetOptions) (string, error) {
	p, ok := m.inner.(Presigner)
	if !ok {
		return "", ErrPresignNotSupported
	}

	return p.PresignGet(ctx, sha256, ttl, opts)
}

// ListMayRepeat forwards the wrapped store's RepeatingLister capability.
func (m *MetricsBlobStore) ListMayRepeat() bool {
	r, ok := m.inner.(RepeatingLister)
	return ok && r.ListMayRepeat()
}

// StagingSupported reports whether the wrapped store has a staging area.
func (m *MetricsBlobStore) StagingSupported() bool {
	s, ok := m.inner.(StagingStore)
	return ok && s.StagingSupported()
}

// PresignStagingPut forwards to the wrapped store when it has a staging area.
func (m *MetricsBlobStore) PresignStagingPut(ctx context.Context, id string, size int64, ttl time.Duration) (PresignedPut, error) {
	s, ok := m.inner.(StagingStore)
	if !ok {
		return PresignedPut{}, ErrPresignNotSupported
	}

	return s.PresignStagingPut(ctx, id, size, ttl)
}

// StatStaging forwards to the wrapped store.
func (m *MetricsBlobStore) StatStaging(ctx context.Context, id string) (StagedObject, error) {
	s, ok := m.inner.(StagingStore)
	if !ok {
		return StagedObject{}, ErrPresignNotSupported
	}

	return s.StatStaging(ctx, id)
}

// OpenStaging forwards to the wrapped store and counts the bytes read.
func (m *MetricsBlobStore) OpenStaging(ctx context.Context, id, version string) (io.ReadCloser, error) {
	s, ok := m.inner.(StagingStore)
	if !ok {
		return nil, ErrPresignNotSupported
	}

	rc, err := s.OpenStaging(ctx, id, version)
	if err != nil {
		return nil, err
	}

	return &countingReadCloser{ReadCloser: rc, reg: m.reg}, nil
}

// PromoteStaging forwards to the wrapped store.
func (m *MetricsBlobStore) PromoteStaging(ctx context.Context, id, sha256, version string, metadata map[string]string) error {
	s, ok := m.inner.(StagingStore)
	if !ok {
		return ErrPresignNotSupported
	}

	return s.PromoteStaging(ctx, id, sha256, version, metadata)
}

// DeleteStaging forwards to the wrapped store.
func (m *MetricsBlobStore) DeleteStaging(ctx context.Context, id string) error {
	s, ok := m.inner.(StagingStore)
	if !ok {
		return ErrPresignNotSupported
	}

	return s.DeleteStaging(ctx, id)
}

// WalkStaging forwards to the wrapped store.
func (m *MetricsBlobStore) WalkStaging(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	s, ok := m.inner.(StagingStore)
	if !ok {
		return ErrPresignNotSupported
	}

	return s.WalkStaging(ctx, olderThan, fn)
}
