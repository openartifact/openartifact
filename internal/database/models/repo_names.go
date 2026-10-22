// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "strings"

// IsReservedRepoName reports names that can never be repository names: they
// collide with Vue router pages (/repos/new, /repos/settings, /repos/trash) or
// with the isolated /repos/<marker>/{name} format namespaces (raw, maven, sbt,
// ivy), which would make the repository's own pages and native URLs
// unreachable. Every path that creates a repository (API, backup restore)
// applies it.
func IsReservedRepoName(name string) bool {
	switch strings.ToLower(name) {
	case "new", "settings", "trash", "raw", "maven", "sbt", "ivy":
		return true
	}

	return false
}
