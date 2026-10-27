// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// TerraformModule is one published version of a Terraform/OpenTofu module. The
// registry namespace of a module is the name of its repository, so a module is
// addressed as REPO/Name/System.
type TerraformModule struct {
	ID           string
	RepositoryID string
	Name         string
	System       string
	Version      string
	// ArchiveExt is the stored archive kind, "zip" or "tar.gz".
	ArchiveExt string
	BlobKey    string
	// Checksum is the lowercase hex SHA-256 of the archive.
	Checksum      string
	SizeBytes     int64
	DownloadCount int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TerraformProvider is one platform zip of a provider version, addressed as
// REPO/Type with Version, OS and Arch.
type TerraformProvider struct {
	ID           string
	RepositoryID string
	Type         string
	Version      string
	OS           string
	Arch         string
	// Protocols are the plugin protocol versions the provider speaks ("5.0",
	// "6.0"); the same for every platform of a version.
	Protocols []string
	BlobKey   string
	// Checksum is the lowercase hex SHA-256 of the zip, the value of its line in
	// the version's SHA256SUMS file.
	Checksum      string
	SizeBytes     int64
	DownloadCount int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
