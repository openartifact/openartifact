// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// MinPartSize is the smallest part of a multipart upload (the S3 minimum;
	// the last part may be smaller).
	MinPartSize int64 = 5 << 20

	// MaxPartSize is the largest part of a multipart upload (the Azure block
	// limit; S3 allows 5 GiB).
	MaxPartSize int64 = 4000 << 20

	// DefaultPartSize is the part size used when the client does not ask for one.
	DefaultPartSize int64 = 64 << 20

	// MaxParts is the largest number of parts of one upload (the S3 limit).
	MaxParts = 10000

	// MaxMultipartObjectBytes is the largest object a multipart upload can
	// assemble (the S3 object limit).
	MaxMultipartObjectBytes int64 = 5 << 40
)

// Sentinel errors of multipart uploads.
var (
	// ErrInvalidPartPlan is returned by PlanParts for a size or part size
	// outside the limits above.
	ErrInvalidPartPlan = errors.New("invalid multipart plan")

	// ErrMultipartIncomplete is returned by CompleteMultipart when a part is
	// missing or has the wrong size. The upload stays open: the client uploads
	// the missing parts and completes again.
	ErrMultipartIncomplete = errors.New("multipart upload is incomplete")
)

// PlanParts validates a multipart plan and returns the part size and part
// count for an object of size bytes. requested is the part size the client
// asked for; 0 picks DefaultPartSize, raised to the smallest MiB multiple that
// keeps the part count within MaxParts.
func PlanParts(size, requested int64) (int64, int, error) {
	if size < 0 || size > MaxMultipartObjectBytes {
		return 0, 0, fmt.Errorf("%w: size %d is outside 0..%d", ErrInvalidPartPlan, size, MaxMultipartObjectBytes)
	}

	partSize := requested
	if partSize == 0 {
		partSize = DefaultPartSize

		if need := (size + MaxParts - 1) / MaxParts; need > partSize {
			const mib = 1 << 20

			partSize = (need + mib - 1) / mib * mib
		}
	}

	if partSize < MinPartSize || partSize > MaxPartSize {
		return 0, 0, fmt.Errorf("%w: part size %d is outside %d..%d", ErrInvalidPartPlan, partSize, MinPartSize, MaxPartSize)
	}

	count := PartCount(size, partSize)
	if count > MaxParts {
		return 0, 0, fmt.Errorf("%w: %d parts exceed the limit of %d", ErrInvalidPartPlan, count, MaxParts)
	}

	return partSize, count, nil
}

// PartCount is the number of parts of an object of size bytes split into
// partSize parts; an empty object still has one (empty) part.
func PartCount(size, partSize int64) int {
	if size <= 0 || partSize <= 0 {
		return 1
	}

	return int((size + partSize - 1) / partSize)
}

// PartLength is the exact size of part number (1-based) of an object of size
// bytes split into partSize parts. It is 0 for a number outside the plan.
func PartLength(size, partSize int64, number int) int64 {
	if number < 1 || number > PartCount(size, partSize) {
		return 0
	}

	return min(partSize, size-int64(number-1)*partSize)
}

// MultipartStagingStore is implemented by backends whose staging area accepts
// an upload in parts, so objects above the 5 GiB single-PUT limit can bypass
// the server. The staging object ends up exactly where StagingStore expects it
// (StatStaging, OpenStaging, PromoteStaging and DeleteStaging work on it as for
// a single PUT); CompleteMultipart is the step that creates it.
//
// Part sizes are fixed by the plan (PartLength): the presigned URL of a part
// signs its exact length, so the object store itself rejects any other size.
type MultipartStagingStore interface {
	// MultipartSupported reports whether the backend can stage multipart
	// uploads. The other methods return ErrPresignNotSupported when it cannot.
	MultipartSupported() bool

	// InitiateMultipart opens a multipart upload into the staging object id
	// (a server generated UUID) for an object of size bytes split into
	// partSize parts. The returned handle (the S3 upload id; empty for
	// backends that need none) is stored by the caller and passed back to the
	// other methods.
	InitiateMultipart(ctx context.Context, id string, size, partSize int64) (handle string, err error)

	// PresignStagingPart returns a URL the client PUTs part number (1-based)
	// to; length is the exact part size from PartLength.
	PresignStagingPart(ctx context.Context, id, handle string, number int, length int64, ttl time.Duration) (PresignedPut, error)

	// CompleteMultipart checks that every part 1..PartCount is present with
	// its planned size and assembles the staging object. It returns
	// ErrMultipartIncomplete, wrapped with the detail, when a part is missing
	// or has another size, and leaves the upload open. It is idempotent: when
	// the staging object already exists (a previous call assembled it) it
	// returns nil. After success the parts can no longer change the object.
	CompleteMultipart(ctx context.Context, id, handle string, size, partSize int64) error
}

// ErrSingleUploadTooLarge is returned by PresignStagingPut when the backend
// cannot take an object of that size in one request (Azure's single Put Blob
// limit is 5000 MiB); the client has to use a multipart upload.
var ErrSingleUploadTooLarge = errors.New("object is too large for a single-request upload")
