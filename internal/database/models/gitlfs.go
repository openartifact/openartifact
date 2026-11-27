// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// GitLFSObject is one Git LFS object of a repository: a blob named by its
// SHA-256 object id.
type GitLFSObject struct {
	ID           string
	RepositoryID string
	// OID is the object id, the lower-case hex SHA-256 of the content (and the
	// digest of its blob).
	OID       string
	SizeBytes int64
	BlobKey   string
	// CreatedAt overrides the creation time of a restore or import; the zero
	// value stamps now.
	CreatedAt time.Time
	UpdatedAt time.Time
}
