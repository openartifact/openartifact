// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// NixPath is one store path of a Nix binary cache: its narinfo and the NAR
// file it points to. Components are named Name (the store path name without the
// hash) at version StoreHash.
type NixPath struct {
	ID           string
	RepositoryID string
	// StoreHash is the 32 character nixbase32 hash of the store path.
	StoreHash string
	Name      string
	// NarHash is the narinfo NarHash ("sha256:<nixbase32>") of the uncompressed NAR.
	NarHash string
	NarSize int64
	// NarFile is the file name below nar/ ("<nixbase32 file hash>.nar.xz").
	NarFile     string
	Compression string
	// References are the base names of the referenced store paths.
	References []string
	Deriver    string
	CA         string
	// Signatures are the Sig lines the uploader supplied ("name:base64").
	Signatures    []string
	DownloadCount int64
	BlobKey       string
	// SHA256 is the lowercase hex SHA-256 of the NAR file (the narinfo FileHash).
	SHA256 string
	// SizeBytes is the size of the NAR file (the narinfo FileSize).
	SizeBytes int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NixPendingNar is a NAR file uploaded before its narinfo.
type NixPendingNar struct {
	RepositoryID string
	// SHA256 is the lowercase hex SHA-256 of the file.
	SHA256    string
	NarFile   string
	BlobKey   string
	SizeBytes int64
	CreatedAt time.Time
}

// NixSigningKey is the narinfo signing key of one Nix repository.
type NixSigningKey struct {
	RepositoryID string
	KeyName      string
	// PublicKey is the Nix public key format "name:base64(32 bytes)".
	PublicKey string
	// PrivateKeyEnc is the internal/crypto sealed private key; it never reaches
	// API responses, backups or sidecars.
	PrivateKeyEnc []byte
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
