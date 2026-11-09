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
	// Projects keeps the repositories of these projects (name or slug), and
	// ProjectRoles those of the projects where the caller's effective role is one
	// of these. Both only narrow the readable set; see narrowToProjects.
	Projects     []string
	ProjectRoles []string
}

// projectScoped reports whether a project filter applies.
func (f SearchFilter) projectScoped() bool {
	return len(f.Projects) > 0 || len(f.ProjectRoles) > 0
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
		case "project":
			f.Projects = append(f.Projects, val)
		case "project_role":
			f.ProjectRoles = append(f.ProjectRoles, val)
		default:
			freeTextParts = append(freeTextParts, token)
		}
	}

	f.FreeText = strings.Join(freeTextParts, " ")

	return f
}
