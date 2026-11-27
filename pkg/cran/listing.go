// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"html"
	"strings"
)

// Listing returns the link targets of an Apache-style directory index, in
// document order: what remotes::install_version reads out of
// src/contrib/Archive/<pkg>/ and what the hosted repository publishes at
// src/contrib/Archive/ as its package names. Absolute, query and parent links
// are not entries and are skipped.
func Listing(page []byte) []string {
	var out []string

	rest := string(page)

	for {
		i := strings.Index(rest, "href=\"")
		if i < 0 {
			return out
		}

		rest = rest[i+len("href=\""):]

		j := strings.IndexByte(rest, '"')
		if j < 0 {
			return out
		}

		target := html.UnescapeString(rest[:j])
		rest = rest[j+1:]

		if target == "" || strings.ContainsAny(target[:1], "/?#") || strings.HasPrefix(target, "..") || strings.Contains(target, "://") {
			continue
		}

		out = append(out, target)
	}
}
