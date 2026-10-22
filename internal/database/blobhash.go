// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrInvalidBlobSHA256 is returned when a blob row would be keyed by anything
// other than a lowercase 64-character hex SHA-256. blobs.sha256 is UNIQUE and
// every upsert resolves conflicts on it, so a single malformed value (the
// empty string in particular) would make unrelated artifacts share one row and
// serve each other's bytes.
var ErrInvalidBlobSHA256 = errors.New("invalid blob sha256: must be 64 lowercase hex characters")

var blobSHA256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IsValidBlobSHA256 reports whether s is a well-formed blobs.sha256 value.
// It mirrors storage.ValidateBlobID, which this package cannot import
// (storage depends on database).
func IsValidBlobSHA256(s string) bool {
	return blobSHA256Re.MatchString(s)
}

func validateBlobSHA256(s string) error {
	if !IsValidBlobSHA256(s) {
		return fmt.Errorf("%w (got %q)", ErrInvalidBlobSHA256, truncateForError(s))
	}

	return nil
}

func truncateForError(s string) string {
	const maxLen = 80
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}

	return s
}
