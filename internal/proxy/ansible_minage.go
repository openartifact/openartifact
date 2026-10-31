// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

// The minimum-age gate of an Ansible proxy (see minage.go).
//
// Galaxy dates a version with the "created_at" of its version entries and
// detail document, the time the version was uploaded ("updated_at" moves when
// the version is touched later, so it is not used). The version list a client
// sees leaves out the versions too young, so the collection's highest version
// is the newest one left (a collection with none left is not found); the detail
// document and the tarball of a too-young version answer errTooNew. A version
// without a readable created_at passes.

// galaxyPublished is the upload time a Galaxy created_at states, the zero time
// when it states none or one that does not parse.
func galaxyPublished(createdAt string) time.Time {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return time.Time{}
	}

	return t
}

// filterAnsibleVersions returns refs without the versions repo's minimum age
// holds back at now, in order. refs is not modified.
func filterAnsibleVersions(repo *models.Repository, refs []pkgansible.VersionRef, now time.Time) []pkgansible.VersionRef {
	if minAge(repo) == 0 {
		return refs
	}

	kept := make([]pkgansible.VersionRef, 0, len(refs))

	for _, v := range refs {
		if !tooNew(repo, galaxyPublished(v.CreatedAt), now) {
			kept = append(kept, v)
		}
	}

	return kept
}

// checkAnsibleAge is errTooNew when the version document doc is younger than
// repo's minimum age at now.
func checkAnsibleAge(repo *models.Repository, doc *pkgansible.VersionDoc, now time.Time) error {
	if tooNew(repo, galaxyPublished(doc.CreatedAt), now) {
		return fmt.Errorf("%s.%s %s: %w", doc.Namespace.Name, doc.Name, doc.Version, errTooNew)
	}

	return nil
}
