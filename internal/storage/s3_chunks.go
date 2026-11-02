// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var _ ChunkStore = (*S3BlobStore)(nil)

// ChunksSupported implements ChunkStore: chunks are the objects uploads/<id> of
// the bucket (also on GCS, Garage and SeaweedFS, which share this store).
func (s *S3BlobStore) ChunksSupported() bool { return true }

// PutChunk implements ChunkStore. The transfer manager takes a body of unknown
// length that cannot seek: it reads the first part into memory and sends it as
// one PutObject when the body ends there, otherwise it streams the rest part by
// part (s3PartSize bytes in memory per part in flight) as a multipart upload,
// which it aborts when it fails. Nothing is spooled to disk.
func (s *S3BlobStore) PutChunk(ctx context.Context, id string, r io.Reader) (int64, error) {
	key, err := chunkKey(id)
	if err != nil {
		return 0, err
	}

	body := &countingReader{r: r}

	if _, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   body,
	}); err != nil {
		// A failure must leave nothing under id; best effort, even when ctx is done.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), chunkCleanupTimeout)
		defer cancel()

		_ = s.DeleteChunk(cleanup, id)

		return 0, fmt.Errorf("s3: upload %s: %w", key, err)
	}

	return body.count, nil
}

// OpenChunk implements ChunkStore.
func (s *S3BlobStore) OpenChunk(ctx context.Context, id string) (io.ReadCloser, error) {
	key, err := chunkKey(id)
	if err != nil {
		return nil, err
	}

	// The SDK checks the CRC32 of a download against the object's checksum header,
	// which S3-compatible stores (Garage) report for a multipart object as that of
	// one part, so a chunk uploaded in parts would fail to read back. Its integrity
	// is not left to the transport anyway: the digest of the assembled blob is
	// verified before anything is stored.
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)},
		func(o *s3.Options) { o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired })
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrChunkNotFound, id)
		}

		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}

	return out.Body, nil
}

// DeleteChunk implements ChunkStore.
func (s *S3BlobStore) DeleteChunk(ctx context.Context, id string) error {
	key, err := chunkKey(id)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}

	return nil
}

// WalkChunks implements ChunkStore.
func (s *S3BlobStore) WalkChunks(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(chunkPrefix),
	})

	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("s3: list %s: %w", chunkPrefix, err)
		}

		var ids []string

		for _, obj := range page.Contents {
			if obj.Key == nil || obj.LastModified == nil || !obj.LastModified.Before(olderThan) {
				continue
			}

			id := strings.TrimPrefix(*obj.Key, chunkPrefix)
			if validChunkID(id) != nil {
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

	return nil
}
