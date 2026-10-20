// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
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
