// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// SwiftRelease is one published release of a Swift package: the source archive
// and what the publisher declared about it.
type SwiftRelease struct {
	ID           string
	RepositoryID string
	// Scope and Name are the identity as first published; lookups ignore case.
	Scope   string
	Name    string
	Version string
	// Filename is the archive file name, "<name>-<version>.zip".
	Filename string

	BlobKey   string
	SizeBytes int64
	// SHA256 is the lower-case hexadecimal SHA-256 of the archive, the checksum
	// SwiftPM verifies.
	SHA256 string

	// Metadata is the metadata JSON object the release was published with.
	Metadata json.RawMessage
	// RepositoryURLs are the declared repository URLs in normalized form.
	RepositoryURLs []string
	// Manifests are Package.swift (first) and the version-specific manifests.
	Manifests []SwiftManifest
	// SignatureFormat, Signature and MetadataSignature are the base64 signatures
	// the publisher sent; the server stores them and does not verify them.
	SignatureFormat   string
	Signature         string
	MetadataSignature string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SwiftManifest is a manifest extracted from a release's source archive.
type SwiftManifest struct {
	Filename     string `json:"filename"`
	SwiftVersion string `json:"swiftVersion,omitempty"` //nolint:tagliatelle // same names as the registry documents
	ToolsVersion string `json:"toolsVersion,omitempty"` //nolint:tagliatelle // same names as the registry documents
	Content      string `json:"content"`
}

// SwiftReleaseVersion is a release in the version list of a package.
type SwiftReleaseVersion struct {
	Version   string
	CreatedAt time.Time
}
