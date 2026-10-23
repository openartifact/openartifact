// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// GroupMetadataState is the regeneration bookkeeping of a deb or rpm group
// repository's merged metadata (group_metadata_state).
type GroupMetadataState struct {
	RepositoryID string
	// Stale is set when a member or the group changed since the metadata was
	// generated.
	Stale bool
	// UpdatedAt changes on every MarkGroupMetadataStale; a regeneration clears
	// Stale only if it is still the value it started from.
	UpdatedAt time.Time
	// GeneratedAt is when the metadata was last generated, nil before the first
	// generation.
	GeneratedAt *time.Time
	// RefreshAfter is when proxy members' upstream metadata may have changed
	// (their cache TTL), nil for a group without proxy members.
	RefreshAfter *time.Time
	// Dists are the Debian distributions served in addition to the ones the
	// hosted members hold.
	Dists []string
}
