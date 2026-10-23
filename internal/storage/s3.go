// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/openartifact/openartifact/internal/telemetry"
)

// Sentinel errors for S3 Blob Store.
var (
	ErrS3PutRequiresSHA256 = errors.New("S3BlobStore.Put requires \"sha256\" in metadata")
)

// s3PartSize is the multipart part size; a verified spool no larger than this
// is stored with one PutObject instead of the transfer manager (see Put).
const s3PartSize = 16 * 1024 * 1024

// S3BlobStore implements BlobStore backed by Amazon S3 or any S3-compatible service.
type S3BlobStore struct {
	client     *s3.Client
	uploader   *transfermanager.Client
	presigner  *s3.PresignClient
	bucket     string
	presignTTL time.Duration
	// gcs marks Google Cloud Storage's XML API, which copies objects of any size
	// with CopyObject, subject to request time limits, but has no UploadPartCopy.
	gcs bool
	// gcsCompose promotes staged objects on GCS (no UploadPartCopy, and a
	// size-independent compose instead of a long synchronous copy).
	gcsCompose *gcsComposer
	// copyOverride replaces the promote copy thresholds (tests).
	copyOverride *copyLimit
}

// NewS3BlobStore creates a new S3BlobStore from the provided StorageConfig.
func NewS3BlobStore(cfg StorageConfig) (*S3BlobStore, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
	}

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load config: %w", err)
	}

	s3Opts := func(o *s3.Options) {
		o.UsePathStyle = cfg.ForcePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	}

	client := s3.NewFromConfig(awsCfg, s3Opts)

	uploader := transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = s3PartSize
		o.Concurrency = 4
	})

	// Presigned URLs embed the host they were signed for, so they are signed
	// by a client pointed at the public endpoint when one is configured; all
	// server-side I/O keeps using the (possibly in-cluster) endpoint above.
	presignClient := client
	if cfg.PresignEndpoint != "" {
		presignClient = s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			s3Opts(o)
			o.BaseEndpoint = aws.String(cfg.PresignEndpoint)
		})
	}

	presigner := s3.NewPresignClient(presignClient)

	ttl := cfg.PresignTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}

	return &S3BlobStore{
		client:     client,
		uploader:   uploader,
		presigner:  presigner,
		bucket:     cfg.Bucket,
		presignTTL: ttl,
	}, nil
}

// objectKey computes the S3 object key for a given SHA-256 hex string.
func objectKey(sha256 string) string {
	return "blobs/" + sha256[:2] + "/" + sha256[2:4] + "/" + sha256
}

// ExportedObjectKey is the exported form of objectKey for testing.
func ExportedObjectKey(sha256 string) string { return objectKey(sha256) }

// clampTTL clamps a presign TTL to [1 minute, 1 hour].
func clampTTL(ttl time.Duration) time.Duration {
	if ttl < time.Minute {
		return time.Minute
	}

	if ttl > time.Hour {
		return time.Hour
	}

	return ttl
}

// ExportedClampTTL is the exported form of clampTTL for testing.
func ExportedClampTTL(ttl time.Duration) time.Duration { return clampTTL(ttl) }

// isNotFound reports whether an S3 error represents a missing object.
// HeadObject returns *types.NotFound; GetObject/DeleteObject return *types.NoSuchKey.
func isNotFound(err error) bool {
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}

	_, ok := errors.AsType[*types.NotFound](err)

	return ok
}

// Put uploads data to S3. The metadata map must contain a "sha256" key.
func (s *S3BlobStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	sha, ok := metadata["sha256"]
	if !ok {
		return "", ErrS3PutRequiresSHA256
	}

	if err := ValidateBlobID(sha); err != nil {
		return "", err
	}

	key := objectKey(sha)

	// Hash into a private spool first: the object only becomes visible once
	// the claimed digest matches the bytes, and the bytes uploaded are the
	// bytes that were hashed.
	_, endSpool := telemetry.StartSpan(ctx, "storage.s3.spool")
	spool, size, err := spoolVerified(data, sha, "", "s3-put-*.tmp")
	endSpool(err)

	if err != nil {
		return "", fmt.Errorf("s3: %w", err)
	}

	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	// Verified content already present is never overwritten.
	existsCtx, endExists := telemetry.StartSpan(ctx, "storage.s3.head")
	exists, err := s.Exists(existsCtx, sha)
	endExists(err)

	if err != nil {
		return "", err
	}

	if exists {
		return sha, nil
	}

	ctx, endUpload := telemetry.StartSpan(ctx, "storage.s3.upload")
	defer func() { endUpload(err) }()

	if size <= s3PartSize {
		err = s.putSingle(ctx, key, spool, size, sha, metadata)
	} else {
		_, err = s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
			Bucket:   aws.String(s.bucket),
			Key:      aws.String(key),
			Body:     spool,
			Metadata: metadata,
		})
	}

	if err != nil {
		return "", fmt.Errorf("s3: upload %s: %w", key, err)
	}

	return sha, nil
}

// putSingle stores a verified spool of at most s3PartSize bytes with one
// PutObject. The transfer manager would first read the whole body into memory
// (io.ReadAll) and the SDK would then hash it twice more (SigV4 payload hash on
// plain-HTTP endpoints, default CRC32 checksum); the spool is already a
// seekable file whose sha256 was verified, so that digest is used as the
// signed x-amz-content-sha256 payload hash instead: the object store still
// checks the received bytes against it, with no extra pass over the body.
func (s *S3BlobStore) putSingle(ctx context.Context, key string, spool io.ReadSeeker, size int64, sha string, metadata map[string]string) error {
	_, err := s.client.PutObject(v4.SetPayloadHash(ctx, sha), &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          spool,
		ContentLength: aws.Int64(size),
		Metadata:      metadata,
	}, func(o *s3.Options) {
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	return err
}

// Get retrieves a blob by its SHA-256 identifier.
func (s *S3BlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	key := objectKey(id)

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
		}

		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}

	return out.Body, nil
}

// Delete removes a blob by its SHA-256 identifier. NoSuchKey is treated as success.
func (s *S3BlobStore) Delete(ctx context.Context, id string) error {
	if err := ValidateBlobID(id); err != nil {
		return err
	}

	key := objectKey(id)

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}

	return nil
}

// Exists checks whether a blob exists using HeadObject.
func (s *S3BlobStore) Exists(ctx context.Context, id string) (bool, error) {
	if err := ValidateBlobID(id); err != nil {
		return false, err
	}

	key := objectKey(id)

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}

	if isNotFound(err) {
		return false, nil
	}

	return false, fmt.Errorf("s3: head %s: %w", key, err)
}

// GetSize returns the size of a blob in bytes using HeadObject.
func (s *S3BlobStore) GetSize(ctx context.Context, id string) (int64, error) {
	if err := ValidateBlobID(id); err != nil {
		return 0, err
	}

	key := objectKey(id)

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return 0, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
		}

		return 0, fmt.Errorf("s3: head %s: %w", key, err)
	}

	if out.ContentLength == nil {
		return 0, fmt.Errorf("s3: nil ContentLength for %s", key) //nolint:err113 // dynamic error with runtime context
	}

	return *out.ContentLength, nil
}

// GetMetadata returns object metadata via HeadObject. Returns nil, nil if not found.
func (s *S3BlobStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	key := objectKey(id)

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
		}

		return nil, fmt.Errorf("s3: head %s: %w", key, err)
	}

	return out.Metadata, nil
}

// List returns one page of at most limit (capped at 1000) blob ids following
// token, the last key of the previous page (ListObjectsV2 StartAfter). Keys
// sort lexicographically, which for the ab/cd/<sha> layout is ascending sha
// order. Objects whose basename is not a blob id are skipped, so a page may
// hold fewer than limit ids; keep calling until next is empty.
func (s *S3BlobStore) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	in := &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.bucket),
		Prefix:  aws.String("blobs/"),
		MaxKeys: aws.Int32(int32(limit)),
	}
	if token != "" {
		in.StartAfter = aws.String(token)
	}

	page, err := s.client.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("s3: list: %w", err)
	}

	ids := make([]string, 0, len(page.Contents))
	last := ""

	for _, obj := range page.Contents {
		if obj.Key == nil {
			continue
		}

		last = *obj.Key

		if id := path.Base(last); ValidateBlobID(id) == nil {
			ids = append(ids, id)
		}
	}

	if !aws.ToBool(page.IsTruncated) {
		last = ""
	}

	return ids, last, nil
}

// ExistsBySHA256 checks whether a blob with the given SHA-256 hash exists using HeadObject.
func (s *S3BlobStore) ExistsBySHA256(ctx context.Context, sha256 string) (string, bool, error) {
	if err := ValidateBlobID(sha256); err != nil {
		return "", false, err
	}

	key := objectKey(sha256)

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return sha256, true, nil
	}

	if isNotFound(err) {
		return "", false, nil
	}

	return "", false, fmt.Errorf("s3: head %s: %w", key, err)
}

// PresignURL returns a presigned GetObject URL with a clamped TTL.
func (s *S3BlobStore) PresignURL(ctx context.Context, sha256 string, ttl time.Duration) (string, error) {
	return s.PresignGet(ctx, sha256, ttl, PresignGetOptions{})
}

// PresignGet returns a presigned GetObject URL with a clamped TTL. A TTL <= 0
// selects the store's configured presign TTL. The response overrides in opts
// are part of the signature.
func (s *S3BlobStore) PresignGet(ctx context.Context, sha256 string, ttl time.Duration, opts PresignGetOptions) (string, error) {
	if err := ValidateBlobID(sha256); err != nil {
		return "", err
	}

	if ttl <= 0 {
		ttl = s.presignTTL
	}

	ttl = clampTTL(ttl)
	key := objectKey(sha256)

	in := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if opts.ContentDisposition != "" {
		in.ResponseContentDisposition = aws.String(opts.ContentDisposition)
	}

	if opts.ContentType != "" {
		in.ResponseContentType = aws.String(opts.ContentType)
	}

	req, err := s.presigner.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3: presign %s: %w", key, err)
	}

	return req.URL, nil
}

// HealthCheck verifies S3 bucket accessibility via HeadBucket.
func (s *S3BlobStore) HealthCheck(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		return fmt.Errorf("s3: health check: %w", err)
	}

	return nil
}
