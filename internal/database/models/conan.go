// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"strings"
	"time"
)

// ConanPackage is one stored file of a Conan recipe revision (PackageID empty)
// or of a package revision of it. Conan 2 addresses everything by revision: the
// recipe revision is the hash of the recipe's manifest, the package revision
// that of the package's manifest.
type ConanPackage struct {
	ID           string
	RepositoryID string
	Name         string
	Version      string
	// UserChannel is "user/channel"; "_/_" for a reference without either.
	UserChannel     string
	RecipeRevision  string
	PackageID       string
	PackageRevision string
	// Filename is the path of the file inside the revision (conanfile.py,
	// conanmanifest.txt, conan_package.tgz, metadata/...). Empty only for rows
	// stored before files were tracked.
	Filename  string
	BlobKey   string
	SHA256    string
	SizeBytes int64
	// MD5 and SHA1 are the lowercase hex digests Conan 1 clients ask for
	// (snapshots, checksum deploy); empty for files stored before they were
	// tracked.
	MD5       string
	SHA1      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ConanFileKey identifies one stored file of a repository.
type ConanFileKey struct {
	Name            string
	Version         string
	UserChannel     string
	RecipeRevision  string
	PackageID       string
	PackageRevision string
	Filename        string
}

// ConanRecipe names a recipe (every revision of name/version@user/channel).
type ConanRecipe struct {
	Name        string
	Version     string
	UserChannel string
}

// ParseConanReference splits name/version@user/channel#revision.
func ParseConanReference(ref string) (ConanFileKey, bool) {
	name, rest, ok := strings.Cut(ref, "/")
	if !ok {
		return ConanFileKey{}, false
	}

	version, rest, ok := strings.Cut(rest, "@")
	if !ok {
		return ConanFileKey{}, false
	}

	userChannel, rrev, ok := strings.Cut(rest, "#")
	if !ok || name == "" || version == "" {
		return ConanFileKey{}, false
	}

	return ConanFileKey{Name: name, Version: version, UserChannel: userChannel, RecipeRevision: rrev}, true
}
