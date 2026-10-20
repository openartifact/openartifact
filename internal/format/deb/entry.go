// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
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

// fieldsOwnedByRepo are always generated from the stored blob and must not be
// taken from the uploaded control stanza.
func isRepoOwnedField(k string) bool {
	return map[string]bool{
		"Package": true, "Version": true, "Architecture": true, "Installed-Size": true,
		"Filename": true, "Size": true, "MD5sum": true, "SHA1": true, "SHA256": true,
		"SHA512": true, "Description": true,
	}[k]
}

// ControlFieldsToStore returns the subset of control fields persisted at upload.
func ControlFieldsToStore(fields map[string]string) map[string]string {
	out := make(map[string]string, len(fields))

	for k, v := range fields {
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

		write(k, e.ControlFields[k])
	}

	order := controlFieldOrder()
	known := make(map[string]bool, len(order))
	for _, k := range order {
		known[k] = true
	}

	extra := make([]string, 0)

	for k := range e.ControlFields {
		if !known[k] && !isRepoOwnedField(k) {
			extra = append(extra, k)
		}
	}

	sort.Strings(extra)

	for _, k := range extra {
		write(k, e.ControlFields[k])
	}

	write("Filename", GetPoolPath(e.ComponentName, e.Name, e.Version, e.Filename))
	write("Size", strconv.FormatInt(e.Size, 10))
	write("SHA256", e.SHA256)
	write("Description", e.ControlFields["Description"])
	sb.WriteString("\n")

	return sb.String()
}
