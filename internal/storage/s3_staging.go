// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
)

const (
	stagingPrefix = "staging/"

	// maxStagingTTL bounds how long a presigned upload URL may live: uploads of
	// multi-gigabyte files need far longer than a download redirect.
	maxStagingTTL = 12 * time.Hour
)

var (
	_ Presigner    = (*S3BlobStore)(nil)
	_ StagingStore = (*S3BlobStore)(nil)
)

// stagingKey computes the object key of a staging upload. id must be a UUID.
func stagingKey(id string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("invalid staging id: %w", err)
	}

	return stagingPrefix + id, nil
}

// ClampStagingTTL clamps an upload URL TTL to [1 minute, 12 hours].
func ClampStagingTTL(ttl time.Duration) time.Duration {
	return min(max(ttl, time.Minute), maxStagingTTL)
}

// PresignStagingPut returns a presigned PutObject URL for the staging object id.
// The content length is part of the signature, so the object store itself
// rejects an upload of a different size.
func (s *S3BlobStore) PresignStagingPut(ctx context.Context, id string, size int64, ttl time.Duration) (PresignedPut, error) {
	key, err := stagingKey(id)
	if err != nil {
		return PresignedPut{}, err
	}

	ttl = ClampStagingTTL(ttl)

	req, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedPut{}, fmt.Errorf("s3: presign put %s: %w", key, err)
	}

	return PresignedPut{URL: req.URL, Headers: presignedHeaders(req.SignedHeader), Expires: time.Now().Add(ttl)}, nil
}

// StagingSupported reports that the S3 backend has a staging area.
func (s *S3BlobStore) StagingSupported() bool { return true }

// StatStaging describes a staging object.
func (s *S3BlobStore) StatStaging(ctx context.Context, id string) (StagedObject, error) {
	key, err := stagingKey(id)
	if err != nil {
		return StagedObject{}, err
	}

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return StagedObject{}, ErrStagingNotFound
		}

		return StagedObject{}, fmt.Errorf("s3: head %s: %w", key, err)
	}

	if out.ContentLength == nil || out.ETag == nil || *out.ETag == "" {
		return StagedObject{}, fmt.Errorf("s3: head %s: missing ContentLength or ETag", key) //nolint:err113 // dynamic error with runtime context
	}

	return StagedObject{Size: *out.ContentLength, Version: *out.ETag}, nil
}

// OpenStaging reads a staging object, conditional on its ETag.
func (s *S3BlobStore) OpenStaging(ctx context.Context, id, version string) (io.ReadCloser, error) {
	key, err := stagingKey(id)
	if err != nil {
		return nil, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:  aws.String(s.bucket),
		Key:     aws.String(key),
		IfMatch: aws.String(version),
	})
	if err != nil {
		switch {
		case isNotFound(err):
			return nil, ErrStagingNotFound
		case isPreconditionFailed(err):
			return nil, ErrStagingChanged
		}

		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}

	return out.Body, nil
}

// PromoteStaging copies a staging object to the content-addressed key with a
// server-side CopyObject (the bytes never pass through OpenArtifact). The copy
// is conditional on the staging object still having the ETag that was
// verified, so an overwrite through the still-valid upload URL after
// verification cannot put unverified bytes under a content-addressed key. When
// the blob already exists the copy is skipped: content-addressed objects with
// the same hash are identical.
func (s *S3BlobStore) PromoteStaging(ctx context.Context, id, sha256, version string, metadata map[string]string) error {
	src, err := stagingKey(id)
	if err != nil {
		return err
	}

	if err := ValidateBlobID(sha256); err != nil {
		return err
	}

	if _, exists, err := s.ExistsBySHA256(ctx, sha256); err != nil {
		return err
	} else if exists {
		return nil
	}

	return s.copyStaging(ctx, src, objectKey(sha256), version, promoteMetadata(sha256, metadata))
}

// isPreconditionFailed reports an S3 412 (a failed If-Match / x-amz-copy-source-if-match).
func isPreconditionFailed(err error) bool {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok && apiErr.ErrorCode() == "PreconditionFailed" {
		return true
	}

	respErr, ok := errors.AsType[*awshttp.ResponseError](err)

	return ok && respErr.HTTPStatusCode() == http.StatusPreconditionFailed
}

// WalkStaging lists staging objects last modified before olderThan.
func (s *S3BlobStore) WalkStaging(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(stagingPrefix),
	})

	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("s3: list %s: %w", stagingPrefix, err)
		}

		var ids []string

		for _, obj := range page.Contents {
			if obj.Key == nil || obj.LastModified == nil || !obj.LastModified.Before(olderThan) {
				continue
			}

			id := strings.TrimPrefix(*obj.Key, stagingPrefix)
			if _, err := uuid.Parse(id); err != nil {
				continue
			}

			ids = append(ids, id)
		}

		if len(ids) == 0 {
			continue
		}

		if err := fn(ids); err != nil {
			return err
		}
	}

	return s.walkStagingMultipart(ctx, olderThan, fn)
}

// walkStagingMultipart reports unfinished multipart uploads older than
// olderThan: their parts are invisible to the object listing and are billed
// until aborted (DeleteStaging aborts them).
func (s *S3BlobStore) walkStagingMultipart(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	ids, err := s.stagingMultipartIDs(ctx, olderThan)
	if err != nil || len(ids) == 0 {
		return err
	}

	return fn(ids)
}

// DeleteStaging removes a staging object and aborts any unfinished multipart
// upload of it; a missing object is not an error.
func (s *S3BlobStore) DeleteStaging(ctx context.Context, id string) error {
	key, err := stagingKey(id)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}

	return s.abortStagingMultipart(ctx, key)
}
