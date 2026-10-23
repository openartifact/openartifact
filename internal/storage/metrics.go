// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"io"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/telemetry"
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

// observe times one blob store call: it feeds the operation latency histogram
// and, under a sampled request, a "storage.<operation>" span, so the object
// store shows up as its own step in a trace instead of a gap between queries.
// It returns the function that records the outcome.
func (m *MetricsBlobStore) observe(ctx context.Context, operation string) (context.Context, func(err error)) {
	start := time.Now()
	ctx, end := telemetry.StartSpan(ctx, "storage."+operation, attribute.String("blob.operation", operation))

	return ctx, func(err error) {
		end(err)

		result := "ok"
		if err != nil {
			result = "error"
		}

		m.reg.BlobOperationDurationSeconds.WithLabelValues(operation, result).Observe(time.Since(start).Seconds())
	}
}

func (m *MetricsBlobStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	cr := &countingReader{r: data}

	ctx, done := m.observe(ctx, "put")
	id, err := m.inner.Put(ctx, cr, metadata)
	done(err)

	if err == nil {
		m.reg.BlobBytesTotal.WithLabelValues("put").Add(float64(cr.count))
	}

	return id, err
}

func (m *MetricsBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	ctx, done := m.observe(ctx, "get")
	rc, err := m.inner.Get(ctx, id)
	done(err)

	if err != nil {
		return nil, err
	}

	return &countingReadCloser{ReadCloser: rc, reg: m.reg}, nil
}

func (m *MetricsBlobStore) Delete(ctx context.Context, id string) error {
	ctx, done := m.observe(ctx, "delete")
	err := m.inner.Delete(ctx, id)
	done(err)

	return err
}

func (m *MetricsBlobStore) Exists(ctx context.Context, id string) (bool, error) {
	ctx, done := m.observe(ctx, "exists")
	ok, err := m.inner.Exists(ctx, id)
	done(err)

	return ok, err
}

func (m *MetricsBlobStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	ctx, done := m.observe(ctx, "metadata")
	md, err := m.inner.GetMetadata(ctx, id)
	done(err)

	return md, err
}

func (m *MetricsBlobStore) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	return m.inner.List(ctx, token, limit)
}

func (m *MetricsBlobStore) GetSize(ctx context.Context, id string) (int64, error) {
	ctx, done := m.observe(ctx, "size")
	n, err := m.inner.GetSize(ctx, id)
	done(err)

	return n, err
}

func (m *MetricsBlobStore) ExistsBySHA256(ctx context.Context, sha256 string) (string, bool, error) {
	ctx, done := m.observe(ctx, "exists_by_sha256")
	key, ok, err := m.inner.ExistsBySHA256(ctx, sha256)
	done(err)

	return key, ok, err
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

// MultipartSupported reports whether the wrapped store can stage multipart uploads.
func (m *MetricsBlobStore) MultipartSupported() bool {
	s, ok := m.inner.(MultipartStagingStore)
	return ok && s.MultipartSupported()
}

// InitiateMultipart forwards to the wrapped store.
func (m *MetricsBlobStore) InitiateMultipart(ctx context.Context, id string, size, partSize int64) (string, error) {
	s, ok := m.inner.(MultipartStagingStore)
	if !ok {
		return "", ErrPresignNotSupported
	}

	return s.InitiateMultipart(ctx, id, size, partSize)
}

// PresignStagingPart forwards to the wrapped store.
func (m *MetricsBlobStore) PresignStagingPart(ctx context.Context, id, handle string, number int, length int64, ttl time.Duration) (PresignedPut, error) {
	s, ok := m.inner.(MultipartStagingStore)
	if !ok {
		return PresignedPut{}, ErrPresignNotSupported
	}

	return s.PresignStagingPart(ctx, id, handle, number, length, ttl)
}

// CompleteMultipart forwards to the wrapped store.
func (m *MetricsBlobStore) CompleteMultipart(ctx context.Context, id, handle string, size, partSize int64) error {
	s, ok := m.inner.(MultipartStagingStore)
	if !ok {
		return ErrPresignNotSupported
	}

	return s.CompleteMultipart(ctx, id, handle, size, partSize)
}
