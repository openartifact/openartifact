// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DeclarativeObject is the ownership marker of an object created or adopted by
// `oa apply`. ObjectID is the server-side id at apply time.
type DeclarativeObject struct {
	Kind        string
	Name        string
	ObjectID    string
	AppliedHash string
	AppliedAt   time.Time
}

// PermissionAssignmentView is one assignment of a permission target with the
// principal's display name (username or group name).
type PermissionAssignmentView struct {
	PrincipalType string
	PrincipalID   string
	PrincipalName string
}
