// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rpmextra is the persisted form of the part of an rpm.Package that
// the scalar columns of a package row do not hold: dependencies, files and
// changelogs, and the sizes that older versions stored as 0.
package rpmextra

import (
	"encoding/json"
	"fmt"

	"github.com/M0Rf30/pkgformat/rpm"
)

// Stored is a package loaded from its row. WeakDepsParsed and SizesParsed say
// whether the persisted data already carries what older versions left out.
type Stored struct {
	rpm.Package

	// WeakDepsParsed is true when the persisted data carries weak dependencies
	// (recommends, suggests, supplements and enhances).
	WeakDepsParsed bool
	// SizesParsed is true when the persisted data carries the installed and
	// archive sizes read from the header.
	SizesParsed bool
}

// data is the persisted JSON.
type data struct {
	Provides    []rpm.Dependency `json:"provides,omitempty"`
	Requires    []rpm.Dependency `json:"requires,omitempty"`
	Conflicts   []rpm.Dependency `json:"conflicts,omitempty"`
	Obsoletes   []rpm.Dependency `json:"obsoletes,omitempty"`
	Recommends  []rpm.Dependency `json:"recommends,omitempty"`
	Suggests    []rpm.Dependency `json:"suggests,omitempty"`
	Supplements []rpm.Dependency `json:"supplements,omitempty"`
	Enhances    []rpm.Dependency `json:"enhances,omitempty"`
	// WeakDeps marks rows persisted by a version that stored weak dependencies.
	// Rows without it are re-read from their blob when metadata is regenerated.
	WeakDeps bool `json:"weak_deps,omitempty"`
	// SizesParsed marks rows whose installed/archive sizes were read from the
	// header correctly: older versions stored 0 (installed_size/archive_size
	// columns), so rows without it are re-read from their blob when metadata
	// is regenerated and the values below take precedence over the columns.
	SizesParsed   bool                 `json:"sizes_parsed,omitempty"`
	InstalledSize int64                `json:"installed_size,omitempty"`
	ArchiveSize   int64                `json:"archive_size,omitempty"`
	Files         []rpm.FileEntry      `json:"files,omitempty"`
	Changelogs    []rpm.ChangelogEntry `json:"changelogs,omitempty"`
}

// Marshal encodes dependencies, sizes, files and changelogs of p for
// persistence.
func Marshal(p *rpm.Package) ([]byte, error) {
	return json.Marshal(data{
		Provides: p.Provides, Requires: p.Requires, Conflicts: p.Conflicts, Obsoletes: p.Obsoletes,
		Recommends: p.Recommends, WeakDeps: true, Suggests: p.Suggests, Supplements: p.Supplements, Enhances: p.Enhances,
		SizesParsed: true, InstalledSize: p.InstalledSize, ArchiveSize: p.ArchiveSize,
		Files: p.Files, Changelogs: p.Changelogs,
	})
}

// Unmarshal restores what Marshal wrote into s. Empty input leaves s alone.
func (s *Stored) Unmarshal(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}

	var d data
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("rpm: decoding extra data: %w", err)
	}

	s.Provides, s.Requires, s.Conflicts, s.Obsoletes = d.Provides, d.Requires, d.Conflicts, d.Obsoletes
	s.Recommends, s.WeakDepsParsed = d.Recommends, d.WeakDeps
	s.Suggests, s.Supplements, s.Enhances = d.Suggests, d.Supplements, d.Enhances
	s.Files, s.Changelogs = d.Files, d.Changelogs

	s.SizesParsed = d.SizesParsed
	if d.SizesParsed {
		s.InstalledSize, s.ArchiveSize = d.InstalledSize, d.ArchiveSize
	}

	return nil
}
