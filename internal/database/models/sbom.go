// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type SBOMReport struct {
	ID           string
	RepositoryID string
	ArtifactID   string
	Format       string // "cyclonedx" or "spdx"
	SpecVersion  string // "1.4" or "2.3"
	ContentType  string
	SBOMData     map[string]any // for JSON SBOMs (cyclonedx); populated by the API layer from the generator, not persisted directly
	SBOMText     string         // for text SBOMs (spdx); ditto
	// BlobSHA256 is the sha256 of the blob store object holding the raw SBOM bytes; "" if not (yet) persisted. Set by the
	// caller before CreateSBOMReport; populated on reads via a JOIN with blobs.
	BlobSHA256 string
	// BlobSizeBytes is the size of that blob; only meaningful together with BlobSHA256, used by CreateSBOMReport to upsert
	// the blobs row.
	BlobSizeBytes  int64
	ComponentCount int
	GeneratedAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
