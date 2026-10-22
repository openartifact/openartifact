// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// controlFieldOrder is the conventional field order of a Packages stanza.
func controlFieldOrder() []string {
	return []string{
		"Package", "Source", "Version", "Architecture", "Maintainer", "Installed-Size",
		"Pre-Depends", "Depends", "Recommends", "Suggests", "Enhances", "Breaks",
		"Conflicts", "Provides", "Replaces", "Built-Using", "Essential", "Multi-Arch",
		"Section", "Priority", "Homepage",
	}
}

// repoOwnedFields are always generated from the stored blob and must not be
// taken from the uploaded control stanza.
var repoOwnedFields = []string{ //nolint:gochecknoglobals // read-only
	"Package", "Version", "Architecture", "Installed-Size",
	"Filename", "Size", "MD5sum", "SHA1", "SHA256", "SHA512", "Description",
}

// canonicalField maps a control field name onto its canonical spelling when it
// is a field the repository knows or owns. apt matches field names
// case-insensitively, so "filename" or "sha256" in an uploaded stanza is the
// same field as "Filename"/"SHA256" and must not slip past the owned-field
// filter. Unknown fields keep their spelling.
func canonicalField(k string) string {
	for _, f := range controlFieldOrder() {
		if strings.EqualFold(k, f) {
			return f
		}
	}

	for _, f := range repoOwnedFields {
		if strings.EqualFold(k, f) {
			return f
		}
	}

	return k
}

func isRepoOwnedField(k string) bool {
	return slices.Contains(repoOwnedFields, canonicalField(k))
}

// canonicalFields rewrites every key to its canonical spelling. Spelling
// variants of one field collapse deterministically: the exact canonical key
// wins, otherwise the lexically smallest variant.
func canonicalFields(fields map[string]string) map[string]string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	out := make(map[string]string, len(fields))

	for _, k := range keys {
		c := canonicalField(k)
		if _, dup := out[c]; dup && k != c {
			continue
		}

		out[c] = fields[k]
	}

	return out
}

// ControlFieldsToStore returns the subset of control fields persisted at upload.
func ControlFieldsToStore(fields map[string]string) map[string]string {
	canon := canonicalFields(fields)
	out := make(map[string]string, len(canon))

	for k, v := range canon {
		if !isRepoOwnedField(k) || k == "Description" {
			out[k] = v
		}
	}

	return out
}

// FormatPackagesEntry renders a complete Packages stanza (terminated by a
// blank line) for the entry, including a pool-relative Filename.
func FormatPackagesEntry(e models.DebPackageEntry) string {
	var sb strings.Builder

	// Rows stored before field names were canonicalized may carry other spellings.
	fields := canonicalFields(e.ControlFields)

	write := func(k, v string) {
		if v == "" {
			return
		}

		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(v)
		sb.WriteString("\n")
	}

	write("Package", e.Name)

	for _, k := range controlFieldOrder()[1:] {
		if k == "Version" {
			write("Version", e.Version)
			continue
		}

		if k == "Architecture" {
			write("Architecture", e.Arch)
			continue
		}

		if k == "Installed-Size" {
			if e.InstalledSize > 0 {
				write(k, strconv.FormatInt(e.InstalledSize, 10))
			}

			continue
		}

		write(k, fields[k])
	}

	order := controlFieldOrder()
	known := make(map[string]bool, len(order))
	for _, k := range order {
		known[k] = true
	}

	extra := make([]string, 0)

	for k := range fields {
		if !known[k] && !isRepoOwnedField(k) {
			extra = append(extra, k)
		}
	}

	sort.Strings(extra)

	for _, k := range extra {
		write(k, fields[k])
	}

	write("Filename", GetPoolPath(e.ComponentName, e.Name, e.Version, e.Filename))
	write("Size", strconv.FormatInt(e.Size, 10))
	write("SHA256", e.SHA256)
	write("Description", fields["Description"])
	sb.WriteString("\n")

	return sb.String()
}
