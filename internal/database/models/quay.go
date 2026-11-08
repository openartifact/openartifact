// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// QuayRepository is one image path a Quay client created inside a hosted
// docker repository (the Quay organization). The two permission targets scope
// a principal to that image: read pulls, write pulls and pushes.
type QuayRepository struct {
	ID           string
	RepositoryID string
	// OrgName is the hosted docker repository's name: the Quay organization.
	OrgName       string
	Name          string // the image path, "<namespace>/<component>"
	Description   string
	ReadTargetID  string
	WriteTargetID string
	CreatedAt     time.Time
}

// QuayRobot is a Quay robot account: the service account behind it and its
// sealed token.
type QuayRobot struct {
	ID           string
	RepositoryID string
	OrgName      string
	Name         string // the short robot name, without the "<org>+" prefix
	Description  string
	UserID       string
	TokenID      string
	// TokenEnc is the robot's token sealed with internal/crypto.
	TokenEnc  string
	CreatedAt time.Time
}

// QuayTeam is a Quay team: the group behind it.
type QuayTeam struct {
	ID           string
	RepositoryID string
	Name         string
	GroupID      string
	CreatedAt    time.Time
}

// QuayNotification is a stored repository notification.
type QuayNotification struct {
	ID               string
	QuayRepositoryID string
	Title            string
	Event            string
	Method           string
	Config           json.RawMessage
	EventConfig      json.RawMessage
	CreatedAt        time.Time
}

// QuayUserPermission is a user or robot with access to one Quay repository.
type QuayUserPermission struct {
	UserID string
	// Username is the account's OpenArtifact username; RobotName is the short
	// Quay robot name, "" for a regular user.
	Username  string
	RobotName string
	Write     bool
}

// QuayTeamPermission is a repository a Quay team has access to. IsPublic is the
// visibility of the organization (hosted docker repository) that holds it.
type QuayTeamPermission struct {
	Repository string
	IsPublic   bool
	Write      bool
}

// QuayTag is one tag of a Quay repository.
type QuayTag struct {
	Name           string
	ManifestDigest string
	UpdatedAt      time.Time
}
