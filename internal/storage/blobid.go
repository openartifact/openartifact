// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"regexp"
)

// ErrInvalidBlobID is returned when a blob identifier is not a lowercase
// 64-character hex SHA-256 digest.
var ErrInvalidBlobID = errors.New("invalid blob id: must be 64 lowercase hex characters")

var blobIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateBlobID reports whether id is a well-formed content-addressed blob
// identifier (lowercase hex SHA-256). Every backend key helper calls it so
// untrusted identifiers can never influence a filesystem path or object key.
func ValidateBlobID(id string) error {
	if !blobIDRe.MatchString(id) {
		return ErrInvalidBlobID
	}

	return nil
}

// existsByID answers ExistsBySHA256 for a store that keys every blob by its
// sha256: the blob is found under that id or not at all.
func existsByID(ctx context.Context, s BlobStore, sha256 string) (string, bool, error) {
	if err := ValidateBlobID(sha256); err != nil {
		return "", false, err
	}

	exists, err := s.Exists(ctx, sha256)
	if err != nil || !exists {
		return "", false, err
	}

	return sha256, true, nil
}
