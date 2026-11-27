// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// PubPackage is one published version of a Dart or Flutter package: the
// name-version.tar.gz archive and the pubspec.yaml it carries.
type PubPackage struct {
	ID           string
	RepositoryID string
	Name         string
	Version      string
	// Filename is the archive file name, "<name>-<version>.tar.gz".
	Filename string

	BlobKey   string
	SizeBytes int64
	SHA256    string

	// Pubspec is the pubspec.yaml of the archive as JSON.
	Pubspec json.RawMessage

	CreatedAt time.Time
	UpdatedAt time.Time
}
