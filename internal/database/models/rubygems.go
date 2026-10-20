// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type RubyGem struct {
	ID             string
	RepositoryID   string
	Name           string
	Version        string
	Platform       string
	Description    string
	Summary        string
	Homepage       string
	Authors        []string
	Licenses       []string
	Dependencies   []byte // JSON array
	MetadataJSON   []byte
	BlobKey        string
	ChecksumSHA256 string
	Yanked         bool
	DownloadCount  int
	Size           int64 // gem file size in bytes
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
