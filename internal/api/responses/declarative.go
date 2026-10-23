// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package responses

import "time"

// DeclarativeObjectResponse is the ownership marker of one object managed by `oa apply`.
type DeclarativeObjectResponse struct {
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	ObjectID    string    `json:"object_id"`
	AppliedHash string    `json:"applied_hash"`
	AppliedAt   time.Time `json:"applied_at"`
}

// DeclarativeObjectsResponse lists ownership markers.
type DeclarativeObjectsResponse struct {
	Items []DeclarativeObjectResponse `json:"items"`
}

// PermissionAssignmentResponse is one principal a permission target is assigned to.
type PermissionAssignmentResponse struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	PrincipalName string `json:"principal_name"`
}
