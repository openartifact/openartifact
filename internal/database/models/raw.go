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
	// CompletingSince is the start of the complete call that currently holds
	// the upload's lease; nil when none does.
	CompletingSince *time.Time
	// CompletedAt is set once the artifact is recorded. ArtifactID is then the
	// id the complete call returned.
	CompletedAt *time.Time
	ArtifactID  string
	// PartSize is the planned size of every part but the last of a multipart
	// upload; 0 for a single-PUT upload.
	PartSize int64
	// MultipartID is the backend's handle of the open multipart upload.
	MultipartID string
}
