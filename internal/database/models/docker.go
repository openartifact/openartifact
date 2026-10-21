// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DockerRepository holds Docker-specific config for a repository.
type DockerRepository struct {
	ID           string    `db:"id"`
	RepositoryID string    `db:"repository_id"`
	CreatedAt    time.Time `db:"created_at"`
}

// DockerBlob represents a content-addressable blob (layer or config).
type DockerBlob struct {
	ID           string    `db:"id"`
	RepositoryID string    `db:"repository_id"`
	Digest       string    `db:"digest"` // sha256:<hex>
	MediaType    string    `db:"media_type"`
	Size         int64     `db:"size"`
	BlobSHA256   string    `db:"blob_sha256"` // blobstore key
	CreatedAt    time.Time `db:"created_at"`
}

// DockerUpload tracks an in-progress chunked blob upload.
type DockerUpload struct {
	ID           string    `db:"id"`
	RepositoryID string    `db:"repository_id"`
	UploadUUID   string    `db:"upload_uuid"`
	Offset       int64     `db:"offset"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// DockerManifest stores an OCI or Docker v2 image manifest.
type DockerManifest struct {
	ID            string `db:"id"`
	RepositoryID  string `db:"repository_id"`
	Image         string `db:"image"`  // image name inside the repository; "" is the default image
	Digest        string `db:"digest"` // sha256:<hex>
	MediaType     string `db:"media_type"`
	BlobID        string `db:"blob_id"`
	Size          int64  `db:"size"`
	SubjectDigest string `db:"subject_digest"` // OCI referrers
	// BlobSHA256 is populated only by ListDockerManifestsByRepo (a JOIN with
	// blobs, used for backup export) since BlobID above is a live blobs.id
	// UUID that doesn't survive a restore into a fresh DB.
	BlobSHA256 string `db:"-"`
	// Refs, when set, is recorded together with the manifest in one
	// transaction (see CreateDockerManifest). Nil leaves the manifest
	// unindexed, to be indexed later by registry GC.
	Refs      *DockerManifestRefs `db:"-"`
	CreatedAt time.Time           `db:"created_at"`
	UpdatedAt time.Time           `db:"updated_at"`
}

// DockerManifestRefs lists what a manifest body references: blob digests
// (config and layers) and child manifest digests (an index).
type DockerManifestRefs struct {
	Blobs     []string
	Manifests []string
}

// DockerGCManifest is a manifest as seen by registry GC.
type DockerGCManifest struct {
	ID            string
	Image         string
	Digest        string // sha256:<hex>
	SubjectDigest string
	Size          int64
	BlobSHA256    string // blob-store key of the manifest body
	// Tagged: some tag of the same image points at the manifest.
	Tagged bool
	// Young: pushed or re-pushed within the grace period.
	Young bool
	// Children are the digests of the manifests an index references.
	Children []string
}

// DockerGCBlob is a blob record of a repository as seen by registry GC.
type DockerGCBlob struct {
	Digest     string // sha256:<hex>
	BlobSHA256 string // blob-store key
	Size       int64
}

// DockerTag is a named reference to a manifest.
type DockerTag struct {
	ID             string    `db:"id"`
	RepositoryID   string    `db:"repository_id"`
	Image          string    `db:"image"` // image name inside the repository; "" is the default image
	Name           string    `db:"name"`
	ManifestDigest string    `db:"manifest_digest"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}
