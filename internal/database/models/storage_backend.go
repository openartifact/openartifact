// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// StorageBackend represents a configured storage backend.
type StorageBackend struct {
	ID            string
	Name          string
	Kind          string // filesystem, s3, gcs, azure
	Config        []byte // JSON: type-specific configuration
	Status        string // ok, degraded, error, unknown
	StatusMessage *string
	LastCheckedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
