// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// BundleCheckStatus is the state an external check reports.
type BundleCheckStatus string

const (
	BundleCheckPending BundleCheckStatus = "pending"
	BundleCheckSuccess BundleCheckStatus = "success"
	BundleCheckFailure BundleCheckStatus = "failure"
	BundleCheckError   BundleCheckStatus = "error"
)

// BundleCheck is the latest result of one named external check reported
// against a release bundle.
type BundleCheck struct {
	ID         string            `db:"id"`
	BundleID   string            `db:"bundle_id"`
	Name       string            `db:"name"`
	Status     BundleCheckStatus `db:"status"`
	Summary    string            `db:"summary"`
	DetailsURL string            `db:"details_url"`
	ReportedBy string            `db:"reported_by"`
	// ExpiresAt, when set, is the moment a success stops counting.
	ExpiresAt *time.Time `db:"expires_at"`
	CreatedAt time.Time  `db:"created_at"`
	UpdatedAt time.Time  `db:"updated_at"`
}
