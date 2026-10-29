// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type NPMPackage struct {
	ID             string
	RepositoryID   string
	Scope          string
	Name           string
	FullName       string
	LatestVersion  string
	PackumentStale bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type NPMVersion struct {
	// ID is the id of the tarball package_files row (the artifact id).
	ID              string
	PackageID       string
	Version         string
	ManifestJSON    []byte
	Integrity       string
	Shasum          string
	BlobID          string
	TarballFilename string
	// BlobSHA256/BlobSizeBytes are resolved by the Get* reads (a JOIN with
	// blobs): BlobID above is a blobs.id UUID, not a storage key.
	BlobSHA256    string
	BlobSizeBytes int64
	CreatedAt     time.Time
}

// NPMVersionState is the part of a live npm version that decides the "latest"
// dist-tag: its number and whether its manifest carries a deprecation message.
type NPMVersionState struct {
	Version    string
	Deprecated bool
}

type NPMDistTag struct {
	ID        string
	PackageID string
	Tag       string
	Version   string
	UpdatedAt time.Time
}
