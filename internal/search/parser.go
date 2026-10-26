// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import "strings"

type SearchFilter struct {
	FreeText     string
	Name         string
	Formats      []string
	Repos        []string
	SHA256Prefix string
	// Flagged keeps the packages with a critical or high vulnerability, a secret
	// finding or a quarantined scan result (database.FlaggedArtifactPredicate).
	Flagged bool
}

func ParseQuery(q string) SearchFilter {
	var (
		f             SearchFilter
		freeTextParts []string
	)

	for token := range strings.FieldsSeq(q) {
		colon := strings.IndexByte(token, ':')
		if colon <= 0 {
			freeTextParts = append(freeTextParts, token)
			continue
		}

		key := token[:colon]
		val := token[colon+1:]

		switch key {
		case "name":
			f.Name = val
		case "format":
			f.Formats = append(f.Formats, val)
		case "repo":
			f.Repos = append(f.Repos, val)
		case "sha256":
			f.SHA256Prefix = val
		default:
			freeTextParts = append(freeTextParts, token)
		}
	}

	f.FreeText = strings.Join(freeTextParts, " ")

	return f
}
