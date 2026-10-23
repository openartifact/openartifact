// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"
)

const (
	// defaultSingleCopyLimit is the largest object PromoteStaging copies with
	// one CopyObject call. S3 refuses CopyObject above 5 GiB; stay below it.
	defaultSingleCopyLimit int64 = 4 << 30

	// defaultCopyPartSize is the minimum range copied by one UploadPartCopy.
	defaultCopyPartSize int64 = 1 << 30

	// copyConcurrency is how many ranges of a large promote copy in parallel.
	copyConcurrency = 4

	// listPartsPageLimit is the page size for ListParts / ListMultipartUploads.
	listPartsPageLimit int32 = 1000
)

var _ MultipartStagingStore = (*S3BlobStore)(nil)

// MultipartSupported reports that the S3 backend can stage multipart uploads.
func (s *S3BlobStore) MultipartSupported() bool { return true }

// InitiateMultipart opens an S3 multipart upload on the staging key and
// returns its upload id.
func (s *S3BlobStore) InitiateMultipart(ctx context.Context, id string, _, _ int64) (string, error) {
	key, err := stagingKey(id)
	if err != nil {
		return "", err
	}

	out, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("s3: create multipart upload %s: %w", key, err)
	}

	if out.UploadId == nil || *out.UploadId == "" {
		return "", fmt.Errorf("s3: create multipart upload %s: no upload id", key) //nolint:err113 // dynamic error with runtime context
	}

	return *out.UploadId, nil
}

// PresignStagingPart returns a presigned UploadPart URL for one part. The part
// length is part of the signature.
func (s *S3BlobStore) PresignStagingPart(ctx context.Context, id, handle string, number int, length int64, ttl time.Duration) (PresignedPut, error) {
	key, err := stagingKey(id)
	if err != nil {
		return PresignedPut{}, err
	}

	if handle == "" || number < 1 || number > MaxParts {
		return PresignedPut{}, fmt.Errorf("%w: part %d", ErrInvalidPartPlan, number)
	}

	ttl = ClampStagingTTL(ttl)

	req, err := s.presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		UploadId:      aws.String(handle),
		PartNumber:    aws.Int32(int32(number)),
		ContentLength: aws.Int64(length),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedPut{}, fmt.Errorf("s3: presign upload part %d of %s: %w", number, key, err)
	}

	return PresignedPut{URL: req.URL, Headers: presignedHeaders(req.SignedHeader), Expires: time.Now().Add(ttl)}, nil
}

// presignedHeaders keeps the signed headers the client must send: Host is
// implied by the URL and Content-Length is set by the HTTP client.
func presignedHeaders(signed http.Header) map[string]string {
	headers := make(map[string]string, len(signed))

	for name, values := range signed {
		if len(values) == 0 || strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") {
			continue
		}

		headers[name] = values[0]
	}

	return headers
}

// CompleteMultipart lists the uploaded parts, checks them against the plan and
// completes the S3 multipart upload, which creates the staging object.
func (s *S3BlobStore) CompleteMultipart(ctx context.Context, id, handle string, size, partSize int64) error {
	key, err := stagingKey(id)
	if err != nil {
		return err
	}

	parts, err := s.listParts(ctx, key, handle)
	if err != nil {
		if !isNoSuchUpload(err) {
			return err
		}

		// The upload is gone: either an earlier call assembled it or it was
		// aborted. Only the object tells them apart.
		if obj, statErr := s.StatStaging(ctx, id); statErr == nil && obj.Size == size {
			return nil
		}

		return fmt.Errorf("%w: the upload no longer exists", ErrMultipartIncomplete)
	}

	completed, err := checkParts(parts, size, partSize)
	if err != nil {
		return err
	}

	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(handle),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return fmt.Errorf("s3: complete multipart upload %s: %w", key, err)
	}

	return nil
}

// listParts returns every uploaded part of the multipart upload.
func (s *S3BlobStore) listParts(ctx context.Context, key, handle string) ([]types.Part, error) {
	pages := s3.NewListPartsPaginator(s.client, &s3.ListPartsInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(handle),
		MaxParts: aws.Int32(listPartsPageLimit),
	})

	var parts []types.Part

	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: list parts of %s: %w", key, err)
		}

		parts = append(parts, page.Parts...)
	}

	return parts, nil
}

// checkParts verifies that parts is exactly 1..PartCount with the planned
// sizes and returns them as the part list of CompleteMultipartUpload.
func checkParts(parts []types.Part, size, partSize int64) ([]types.CompletedPart, error) {
	count := PartCount(size, partSize)
	have := make(map[int32]types.Part, len(parts))

	for _, p := range parts {
		if p.PartNumber != nil {
			have[*p.PartNumber] = p
		}
	}

	var missing []string

	completed := make([]types.CompletedPart, 0, count)

	for n := 1; n <= count; n++ {
		p, ok := have[int32(n)]
		if !ok || p.Size == nil || *p.Size != PartLength(size, partSize, n) || p.ETag == nil {
			missing = append(missing, strconv.Itoa(n))
			continue
		}

		completed = append(completed, types.CompletedPart{ETag: p.ETag, PartNumber: aws.Int32(partNumber(n))})
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: parts missing or of the wrong size: %s", ErrMultipartIncomplete, strings.Join(missing, ","))
	}

	return completed, nil
}

// isNoSuchUpload reports an S3 NoSuchUpload error.
func isNoSuchUpload(err error) bool {
	if _, ok := errors.AsType[*types.NoSuchUpload](err); ok {
		return true
	}

	apiErr, ok := errors.AsType[smithy.APIError](err)

	return ok && apiErr.ErrorCode() == "NoSuchUpload"
}

// isNotImplemented reports a backend that does not implement an S3 operation.
func isNotImplemented(err error) bool {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok && apiErr.ErrorCode() == "NotImplemented" {
		return true
	}

	respErr, ok := errors.AsType[*awshttp.ResponseError](err)

	return ok && respErr.HTTPStatusCode() == http.StatusNotImplemented
}

// multipartUploadsWhere walks the in-progress multipart uploads under the
// staging prefix and calls fn for every upload for which keep is true. A
// backend without ListMultipartUploads has none to report.
func (s *S3BlobStore) multipartUploadsWhere(ctx context.Context, prefix string, fn func(types.MultipartUpload) error) error {
	var keyMarker, uploadMarker *string

	for {
		out, err := s.client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket:         aws.String(s.bucket),
			Prefix:         aws.String(prefix),
			KeyMarker:      keyMarker,
			UploadIdMarker: uploadMarker,
			MaxUploads:     aws.Int32(listPartsPageLimit),
		})
		if err != nil {
			if isNotImplemented(err) {
				return nil
			}

			return fmt.Errorf("s3: list multipart uploads %s: %w", prefix, err)
		}

		for _, u := range out.Uploads {
			if u.Key == nil || u.UploadId == nil {
				continue
			}

			if err := fn(u); err != nil {
				return err
			}
		}

		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}

		keyMarker, uploadMarker = out.NextKeyMarker, out.NextUploadIdMarker
	}
}

// abortMultipart aborts one multipart upload; one that is already gone is fine.
func (s *S3BlobStore) abortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil && !isNoSuchUpload(err) {
		return fmt.Errorf("s3: abort multipart upload %s: %w", key, err)
	}

	return nil
}

// abortStagingMultipart aborts every in-progress multipart upload of the
// staging key, so a deleted staging area leaves no parts behind.
func (s *S3BlobStore) abortStagingMultipart(ctx context.Context, key string) error {
	var uploads []types.MultipartUpload

	err := s.multipartUploadsWhere(ctx, key, func(u types.MultipartUpload) error {
		if *u.Key == key {
			uploads = append(uploads, u)
		}

		return nil
	})
	if err != nil {
		return err
	}

	for _, u := range uploads {
		if err := s.abortMultipart(ctx, key, *u.UploadId); err != nil {
			return err
		}
	}

	return nil
}

// stagingMultipartIDs reports the ids of unfinished multipart uploads that
// started before olderThan.
func (s *S3BlobStore) stagingMultipartIDs(ctx context.Context, olderThan time.Time) ([]string, error) {
	var ids []string

	err := s.multipartUploadsWhere(ctx, stagingPrefix, func(u types.MultipartUpload) error {
		if u.Initiated == nil || !u.Initiated.Before(olderThan) {
			return nil
		}

		id := strings.TrimPrefix(*u.Key, stagingPrefix)
		if _, err := stagingKey(id); err == nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}

		return nil
	})

	return ids, err
}

// copyStaging copies the staging object src (exactly version) to dst. Objects
// that fit one CopyObject call use it; larger ones are copied in ranges with
// UploadPartCopy, each conditional on the same version, because CopyObject
// refuses sources above 5 GiB.
func (s *S3BlobStore) copyStaging(ctx context.Context, src, dst, version string, md map[string]string) error {
	if s.gcsCompose != nil {
		return s.gcsCompose.compose(ctx, src, dst, version, md)
	}

	size := int64(-1)

	if !s.gcs {
		head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(src), IfMatch: aws.String(version)})
		if err != nil {
			return mapCopyError(err, src, dst)
		}

		if head.ContentLength != nil {
			size = *head.ContentLength
		}
	}

	// An unknown size is only possible on GCS without the compose client (tests).
	if size < 0 || size <= s.copyLimits().single {
		_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:            aws.String(s.bucket),
			Key:               aws.String(dst),
			CopySource:        aws.String(s.bucket + "/" + url.PathEscape(src)),
			CopySourceIfMatch: aws.String(version),
			Metadata:          md,
			MetadataDirective: types.MetadataDirectiveReplace,
		})
		if err != nil {
			return mapCopyError(err, src, dst)
		}

		return nil
	}

	return s.copyStagingMultipart(ctx, src, dst, version, size, md)
}

// mapCopyError maps a failed copy to the staging sentinel errors.
func mapCopyError(err error, src, dst string) error {
	switch {
	case isNotFound(err):
		return ErrStagingNotFound
	case isPreconditionFailed(err):
		return ErrStagingChanged
	}

	return fmt.Errorf("s3: copy %s to %s: %w", src, dst, err)
}

// copyStagingMultipart copies size bytes with parallel UploadPartCopy ranges.
func (s *S3BlobStore) copyStagingMultipart(ctx context.Context, src, dst, version string, size int64, md map[string]string) error {
	partSize := max(s.copyLimits().part, (size+MaxParts-1)/MaxParts)
	count := PartCount(size, partSize)

	created, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(dst),
		Metadata: md,
	})
	if err != nil {
		return fmt.Errorf("s3: create multipart upload %s: %w", dst, err)
	}

	uploadID := *created.UploadId
	completed := make([]types.CompletedPart, count)

	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(copyConcurrency)

	for n := 1; n <= count; n++ {
		group.Go(func() error {
			start := int64(n-1) * partSize
			end := start + PartLength(size, partSize, n) - 1

			out, err := s.client.UploadPartCopy(gctx, &s3.UploadPartCopyInput{
				Bucket:            aws.String(s.bucket),
				Key:               aws.String(dst),
				UploadId:          aws.String(uploadID),
				PartNumber:        aws.Int32(partNumber(n)),
				CopySource:        aws.String(s.bucket + "/" + url.PathEscape(src)),
				CopySourceRange:   aws.String(fmt.Sprintf("bytes=%d-%d", start, end)),
				CopySourceIfMatch: aws.String(version),
			})
			if err != nil {
				return mapCopyError(err, src, dst)
			}

			if out.CopyPartResult == nil || out.CopyPartResult.ETag == nil {
				return fmt.Errorf("s3: copy part %d of %s: no ETag", n, src) //nolint:err113 // dynamic error with runtime context
			}

			completed[n-1] = types.CompletedPart{ETag: out.CopyPartResult.ETag, PartNumber: aws.Int32(partNumber(n))}

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		// The request context may be the cause of the failure.
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		_ = s.abortMultipart(abortCtx, dst, uploadID)

		return err
	}

	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(dst),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		_ = s.abortMultipart(abortCtx, dst, uploadID)

		return fmt.Errorf("s3: complete multipart copy %s: %w", dst, err)
	}

	return nil
}

// promoteMetadata merges the caller's metadata with the blob digest.
func promoteMetadata(sha256 string, metadata map[string]string) map[string]string {
	md := map[string]string{"sha256": sha256}
	maps.Copy(md, metadata)
	md["sha256"] = sha256

	return md
}

// copyLimit holds the thresholds of a promote copy.
type copyLimit struct{ single, part int64 }

// copyLimits returns the thresholds of a promote copy (tests lower them).
func (s *S3BlobStore) copyLimits() copyLimit {
	if s.copyOverride != nil {
		return *s.copyOverride
	}

	return copyLimit{single: defaultSingleCopyLimit, part: defaultCopyPartSize}
}

// partNumber converts a part number to the SDK's int32. Part numbers are
// bounded by MaxParts, far below the int32 range; the clamp makes that explicit.
func partNumber(n int) int32 {
	return int32(min(max(n, 0), MaxParts))
}
