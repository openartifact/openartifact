// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package responses

// TrashItemResponse is one soft-deleted package row or repository.
type TrashItemResponse struct {
	ID string `json:"id"`
	// Kind is the trash kind: "repository" or the kind of the deleted row
	// (maven_artifact, npm_version, docker_tag, ...).
	Kind         string `json:"kind"`
	RepositoryID string `json:"repository_id"`
	// Repository is the owning repository's name (the trashed repository
	// itself for kind "repository").
	Repository string `json:"repository"`
	Format     string `json:"format"`
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	DeletedAt  string `json:"deleted_at"`
	DeletedBy  string `json:"deleted_by"`
	// PurgeAt is when the retention purge removes the item for good.
	PurgeAt string `json:"purge_at"`
}

// TrashListResponse is a page of trashed items.
type TrashListResponse struct {
	Items []TrashItemResponse `json:"items"`
	Total int                 `json:"total"`
	Page  int                 `json:"page"`
	Limit int                 `json:"limit"`
}

// TrashEmptyResponse is the result of emptying a repository's trash.
type TrashEmptyResponse struct {
	// Purged is how many items this request destroyed.
	Purged int `json:"purged"`
	// Remaining is how many trashed items are left (the request purges a capped number).
	Remaining int `json:"remaining"`
}
