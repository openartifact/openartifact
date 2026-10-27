// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// ApkPackage is one .apk file of an Alpine repository, addressed as
// Branch/Repo/Arch/Name-Version.apk of the mirror tree.
type ApkPackage struct {
	ID           string
	RepositoryID string
	Branch       string
	Repo         string
	// Arch is the architecture directory of the mirror tree; PkgArch is the
	// package's own architecture (PKGINFO arch, APKINDEX A:), noarch inside an
	// x86_64 directory.
	Arch    string
	PkgArch string
	Name    string
	Version string
	// Checksum is the APKINDEX "C:" value: "Q1" + base64(SHA-1 of the control segment).
	Checksum string
	// DataHash is the lowercase hex SHA-256 of the data segment.
	DataHash         string
	Description      string
	URL              string
	License          string
	Origin           string
	Maintainer       string
	Commit           string
	BuildTime        int64
	InstalledSize    int64
	Depends          []string
	Provides         []string
	InstallIf        []string
	ProviderPriority int
	BlobKey          string
	BlobSHA256       string
	SizeBytes        int64
	DownloadCount    int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ApkSigningKey is the index signing key of one Alpine repository.
type ApkSigningKey struct {
	RepositoryID string
	KeyName      string
	Fingerprint  string
	PublicKeyPEM string
	// PrivateKeyEnc is the internal/crypto sealed PKCS#8 PEM; it never reaches
	// API responses, backups or sidecars.
	PrivateKeyEnc []byte
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
