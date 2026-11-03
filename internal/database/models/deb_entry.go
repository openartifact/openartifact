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

// DebPackageStamp is a DebPackageEntry together with the version of its
// control stanza row. Everything a Packages stanza is rendered from is the
// entry's fields and its control fields, and the control fields are fixed by
// the row version: two stamps that are equal (EqualStamp) render the same
// stanza, so a group keeps the rendered text under the stamp instead of
// reading the control fields again. ControlFields is empty unless the
// stamp was read with its controls.
type DebPackageStamp struct {
	DebPackageEntry

	// ControlRev is the version of the row holding the control fields.
	ControlRev int64
}

// EqualStamp reports whether a and b describe the same file in the same state
// (the control fields themselves are not compared; ControlRev stands for them).
func (a *DebPackageStamp) EqualStamp(b *DebPackageStamp) bool {
	return a.ControlRev == b.ControlRev &&
		a.ID == b.ID && a.Name == b.Name && a.Version == b.Version && a.Arch == b.Arch &&
		a.Filename == b.Filename && a.DistributionID == b.DistributionID && a.Distribution == b.Distribution &&
		a.ComponentName == b.ComponentName && a.SHA256 == b.SHA256 && a.Size == b.Size && a.InstalledSize == b.InstalledSize
}
