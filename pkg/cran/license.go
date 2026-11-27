// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import "strings"

// Licenses splits the License field of a DESCRIPTION into the licenses it
// offers: alternatives are separated by "|", and a "+ file LICENSE" (or
// LICENCE) suffix, which points at a file of the package rather than naming a
// license, is dropped. "GPL-2 | file LICENSE" yields ["GPL-2"].
func Licenses(field string) []string {
	var out []string

	for alt := range strings.SplitSeq(field, "|") {
		alt = strings.TrimSpace(alt)

		if i := strings.Index(alt, "+ file "); i >= 0 {
			alt = strings.TrimSpace(alt[:i])
		}

		if strings.HasPrefix(alt, "file ") || alt == "" {
			continue
		}

		out = append(out, alt)
	}

	return out
}
