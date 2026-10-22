// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// Conda package file extensions.
const (
	CondaExtTarBz2 = ".tar.bz2"
	CondaExtConda  = ".conda"
)

// CondaPackage is one conda package file. A .tar.bz2 and a .conda with the
// same name, version and build are separate rows.
type CondaPackage struct {
	ID           string
	RepositoryID string
	Name         string
	Version      string
	BuildString  string
	Platform     string
	// Extension is CondaExtTarBz2 or CondaExtConda; empty is read as
	// CondaExtTarBz2 (rows written before the extension was recorded).
	Extension string
	// BuildNumber, Timestamp, Depends and Extra come from info/index.json.
	BuildNumber int
	// Timestamp is milliseconds since the epoch; 0 when the package declares none.
	Timestamp int64
	Depends   []string
	// Extra holds the remaining repodata fields (constrains, license,
	// license_family, noarch, arch, platform, features, track_features) as a
	// JSON object.
	Extra     json.RawMessage
	BlobKey   string
	SizeBytes int64
	SHA256    string
	MD5       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Filename returns the package file name: name-version-build plus extension.
func (p *CondaPackage) Filename() string {
	ext := p.Extension
	if ext == "" {
		ext = CondaExtTarBz2
	}

	return p.Name + "-" + p.Version + "-" + p.BuildString + ext
}
