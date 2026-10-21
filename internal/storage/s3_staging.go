// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
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

// clampStagingTTL clamps an upload URL TTL to [1 minute, 12 hours].
func clampStagingTTL(ttl time.Duration) time.Duration {
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

	ttl = clampStagingTTL(ttl)

	req, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedPut{}, fmt.Errorf("s3: presign put %s: %w", key, err)
	}

	headers := make(map[string]string, len(req.SignedHeader))

	for name, values := range req.SignedHeader {
		// Host is implied by the URL; Content-Length is set by the HTTP client.
		if len(values) == 0 || name == "Host" || name == "host" || name == "Content-Length" || name == "content-length" {
			continue
		}

		headers[name] = values[0]
	}

	return PresignedPut{URL: req.URL, Headers: headers, Expires: time.Now().Add(ttl)}, nil
}

// StatStaging returns the size of a staging object.
func (s *S3BlobStore) StatStaging(ctx context.Context, id string) (int64, error) {
	key, err := stagingKey(id)
	if err != nil {
		return 0, err
	}

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return 0, ErrStagingNotFound
		}

		return 0, fmt.Errorf("s3: head %s: %w", key, err)
	}

	if out.ContentLength == nil {
		return 0, fmt.Errorf("s3: nil ContentLength for %s", key) //nolint:err113 // dynamic error with runtime context
	}

	return *out.ContentLength, nil
}

// OpenStaging reads a staging object.
func (s *S3BlobStore) OpenStaging(ctx context.Context, id string) (io.ReadCloser, error) {
	key, err := stagingKey(id)
	if err != nil {
		return nil, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrStagingNotFound
		}

		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}

	return out.Body, nil
}

// PromoteStaging copies a staging object to the content-addressed key with a
// server-side CopyObject (the bytes never pass through OpenArtifact). When the
// blob already exists the copy is skipped: content-addressed objects with the
// same hash are identical.
func (s *S3BlobStore) PromoteStaging(ctx context.Context, id, sha256 string, metadata map[string]string) error {
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

	md := map[string]string{"sha256": sha256}
	maps.Copy(md, metadata)
	md["sha256"] = sha256

	dst := objectKey(sha256)

	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(s.bucket),
		Key:               aws.String(dst),
		CopySource:        aws.String(s.bucket + "/" + url.PathEscape(src)),
		Metadata:          md,
		MetadataDirective: types.MetadataDirectiveReplace,
	})
	if err != nil {
		if isNotFound(err) {
			return ErrStagingNotFound
		}

		return fmt.Errorf("s3: copy %s to %s: %w", src, dst, err)
	}

	return nil
}

// DeleteStaging removes a staging object; a missing object is not an error.
func (s *S3BlobStore) DeleteStaging(ctx context.Context, id string) error {
	key, err := stagingKey(id)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}

	return nil
}
