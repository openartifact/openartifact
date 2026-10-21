// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// RawArtifact mirrors the raw_artifacts table.
// BlobID is UUID FK to blobs table; SizeBytes is for response display.
type RawArtifact struct {
	ID           string
	RepositoryID string
	Path         string
	ContentType  string
	BlobID       string
	SizeBytes    int64
	SHA256       string
	BlobKey      string
	Metadata     map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PresignedUpload is a presigned upload awaiting its complete call. The object
// lives at the staging key staging/<ID> in the blob store.
type PresignedUpload struct {
	ID           string
	RepositoryID string
	Path         string
	SizeBytes    int64
	SHA256       string
	ContentType  string
	CreatedBy    string
	CreatedAt    time.Time
	// ExpiresAt is the complete deadline: after it the cleanup worker removes
	// the row and the staging object.
	ExpiresAt time.Time
}
