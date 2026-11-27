// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// CRANFile is one stored R package file: a source tarball, a Windows binary
// zip or a macOS binary tgz of one package version.
type CRANFile struct {
	ID           string
	RepositoryID string
	// Package and Version are the package name and the version as written.
	Package string
	Version string
	// Path is the canonical repository path of the file, Filename its last
	// element, Tree the directory of the tree it belongs to (the tree's PACKAGES
	// lists it).
	Path     string
	Filename string
	Tree     string
	// Fields are the standard PACKAGES fields of the package's DESCRIPTION
	// (Depends, Imports, License, ...), folded to one line each.
	Fields map[string]string
	// MD5 is the file's MD5 sum, the PACKAGES MD5sum field.
	MD5 string
	// Title, Description, License, Maintainer, URL describe the version.
	Title       string
	Description string
	License     string
	Maintainer  string
	URL         string
	BlobKey     string
	Digest      string
	SizeBytes   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
