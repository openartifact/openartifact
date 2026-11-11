// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

// PropertyMatch is one artifact found by a property search.
//
// ArtifactID is the id the properties are stored under. When it is the id of a live file
// of the same repository (the listing item id the web UI and oa use, package_files.id),
// Name, Version and Format describe that file's package version; when it is the id of a
// live docker tag of the same repository (docker_tags.id), they are the image name
// (repository/image), the tag and "docker", as the package listing shows them; otherwise
// they are nil.
type PropertyMatch struct {
	ArtifactID string
	Name       *string
	Version    *string
	Format     *string
}
