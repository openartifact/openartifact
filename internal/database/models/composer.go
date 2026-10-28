// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// ComposerPackage is one published version of a Composer package: the dist zip
// and the composer.json it carries.
type ComposerPackage struct {
	ID           string
	RepositoryID string
	// Name is the full lowercase "vendor/package".
	Name string
	// Version is the version as published, e.g. "v1.2.3" or "dev-main".
	Version string
	// VersionNormalized is Composer's VersionParser::normalize of Version.
	VersionNormalized string

	BlobKey   string
	SizeBytes int64
	SHA256    string
	// SHA1 is the hex sha1 of the zip (Composer dist.shasum).
	SHA1 string

	// ComposerJSON is the package's composer.json as uploaded.
	ComposerJSON json.RawMessage

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ComposerPackageName is one package (all its versions) in a repository.
type ComposerPackageName struct {
	Name string
	// Description is that of the newest live version, "" when absent.
	Description string
	UpdatedAt   time.Time
}
