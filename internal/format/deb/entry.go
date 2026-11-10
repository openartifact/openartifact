// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"strings"

	pkgdeb "github.com/M0Rf30/pkgformat/deb"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Control fields are stored in their deb822 wire form: continuation lines keep
// their leading space and an empty line is " .". pkgformat's ControlFile holds
// the logical value instead, so the values are converted at the storage
// boundary; rows written before pkgformat are already in the wire form.

// ControlFieldsToStore returns the subset of the logical control fields of a
// package that is persisted at upload, in wire form.
func ControlFieldsToStore(fields map[string]string) map[string]string {
	kept := pkgdeb.ControlFieldsToStore(fields)
	out := make(map[string]string, len(kept))

	for k, v := range kept {
		out[k] = encodeValue(v)
	}

	return out
}

// IndexEntry converts a stored package entry into the entry pkgformat renders.
func IndexEntry(e models.DebPackageEntry) pkgdeb.IndexEntry {
	control := make(map[string]string, len(e.ControlFields))
	for k, v := range e.ControlFields {
		control[k] = decodeValue(v)
	}

	return pkgdeb.IndexEntry{
		Name:          e.Name,
		Version:       e.Version,
		Arch:          e.Arch,
		InstalledSize: e.InstalledSize,
		Filename:      GetPoolPath(e.ComponentName, e.Name, e.Version, e.Filename),
		Size:          e.Size,
		SHA256:        e.SHA256,
		Control:       control,
	}
}

// FormatPackagesEntry renders a complete Packages stanza (terminated by a
// blank line) for the entry, including a pool-relative Filename.
func FormatPackagesEntry(e models.DebPackageEntry) string {
	return pkgdeb.FormatPackagesEntry(IndexEntry(e))
}

// encodeValue turns a logical field value into its wire form.
func encodeValue(v string) string {
	first, rest, multi := strings.Cut(v, "\n")
	if !multi {
		return v
	}

	var sb strings.Builder

	sb.WriteString(first)

	for line := range strings.SplitSeq(rest, "\n") {
		sb.WriteByte('\n')

		if line == "" {
			line = "."
		}

		sb.WriteByte(' ')
		sb.WriteString(line)
	}

	return sb.String()
}

// decodeValue turns a stored wire-form value into the logical value.
func decodeValue(v string) string {
	first, rest, multi := strings.Cut(v, "\n")
	if !multi {
		return v
	}

	var sb strings.Builder

	sb.WriteString(first)

	for line := range strings.SplitSeq(rest, "\n") {
		sb.WriteByte('\n')

		if line != "" && (line[0] == ' ' || line[0] == '\t') {
			line = line[1:]
		}

		if line == "." {
			line = ""
		}

		sb.WriteString(line)
	}

	return sb.String()
}
