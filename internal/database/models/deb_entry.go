// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

// DebPackageEntry is a deb package joined with everything needed to render a
// Packages stanza: blob checksum/size, distribution/component names and the
// stored control fields.
type DebPackageEntry struct {
	ID             string
	Name           string
	Version        string
	Arch           string
	Filename       string
	DistributionID string
	Distribution   string
	ComponentName  string
	SHA256         string
	Size           int64
	InstalledSize  int64
	ControlFields  map[string]string
}
