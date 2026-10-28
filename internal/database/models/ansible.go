// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// AnsibleCollection is one published version of an Ansible collection: the
// ns-name-version.tar.gz artifact and what its MANIFEST.json says about it.
type AnsibleCollection struct {
	ID           string
	RepositoryID string
	Namespace    string
	Name         string
	Version      string
	// Filename is the artifact file name, "<namespace>-<name>-<version>.tar.gz".
	Filename string

	BlobKey   string
	SizeBytes int64
	SHA256    string

	// Dependencies maps "namespace.name" to a version requirement.
	Dependencies map[string]string
	// Metadata is the collection_info object of MANIFEST.json.
	Metadata json.RawMessage
	// RequiresAnsible is the requires_ansible of meta/runtime.yml, "" when absent.
	RequiresAnsible string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AnsibleCollectionName is one collection (all its versions) in a repository.
type AnsibleCollectionName struct {
	Namespace string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}
