// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"time"

	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

// The version gate of an Ansible proxy (see gate.go): minimum age and curation.
//
// Galaxy dates a version with the "created_at" of its version entries and
// detail document, the time the version was uploaded ("updated_at" moves when
// the version is touched later, so it is not used). The version list a client
// sees leaves out the versions too young, so the collection's highest version
// is the newest one left (a collection with none left is not found); the detail
// document and the tarball of a too-young version answer 403. A version
// without a readable created_at passes the minimum age.
//
// Curation names a collection "namespace.name" and matches its versions as
// Galaxy writes them; it needs no created_at. A curated version is left out of
// the version list, so the collection's highest version is the newest one left,
// and its detail document and tarball answer 403.

// galaxyPublished is the upload time a Galaxy created_at states, the zero time
// when it states none or one that does not parse.
func galaxyPublished(createdAt string) time.Time {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return time.Time{}
	}

	return t
}

// ansibleCollection is the name curation rules match a collection by:
// "namespace.name" as Galaxy writes it (lower-case).
func ansibleCollection(p pkgansible.Path) string { return p.Namespace + "." + p.Name }

// filterAnsibleVersions returns refs of collection without the versions g holds
// back (too young, or curated away), in order. refs is not modified.
func filterAnsibleVersions(g versionGate, collection string, refs []pkgansible.VersionRef) []pkgansible.VersionRef {
	if !g.Active() {
		return refs
	}

	kept := make([]pkgansible.VersionRef, 0, len(refs))

	for _, v := range refs {
		if hidden, _ := g.Hidden(collection, v.Version, galaxyPublished(v.CreatedAt)); !hidden {
			kept = append(kept, v)
		}
	}

	return kept
}

// checkAnsibleGate is the gateError of version doc of collection when g holds it
// back.
func checkAnsibleGate(g versionGate, collection string, doc *pkgansible.VersionDoc) error {
	return checkGate(g, collection, doc.Version, galaxyPublished(doc.CreatedAt))
}
