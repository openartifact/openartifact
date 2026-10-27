// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Signature sources of a PacmanPackage.
const (
	// PacmanSigUploader marks a signature the publisher uploaded.
	PacmanSigUploader = "uploader"
	// PacmanSigServer marks a signature made with the repository signing key.
	PacmanSigServer = "server"
)

// PacmanPackage is one package file (.pkg.tar.zst, .xz or .gz) of a pacman
// repository. A repository database lists the newest version of each Name;
// older rows stay downloadable until they are deleted.
type PacmanPackage struct {
	ID           string
	RepositoryID string
	Name         string
	Base         string
	// Version is "[epoch:]pkgver-pkgrel".
	Version string
	// Arch is the architecture of the package ("any" or a CPU architecture);
	// RepoArch the os/ARCH directory it was uploaded to.
	Arch     string
	RepoArch string
	// Filename is the published file name.
	Filename string

	BlobKey   string
	SizeBytes int64
	SHA256    string
	MD5       string

	Description   string
	URL           string
	Packager      string
	BuildDate     int64
	InstalledSize int64
	Licenses      []string
	Groups        []string
	Replaces      []string
	Conflicts     []string
	Provides      []string
	Depends       []string
	OptDepends    []string
	MakeDepends   []string
	CheckDepends  []string

	// FileList is the newline-separated file list of the files database; nil
	// while it has not been read from the package yet. It is only loaded by the
	// queries that say so.
	FileList *string

	// Signature is the base64 of the binary detached signature, "" when unsigned.
	Signature string
	// SignatureSource is PacmanSigUploader, PacmanSigServer or "".
	SignatureSource string
	// SignatureKey is the fingerprint of the repository key behind a server signature.
	SignatureKey string

	CreatedAt time.Time
	UpdatedAt time.Time
}
