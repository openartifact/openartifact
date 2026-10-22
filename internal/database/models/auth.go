// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type User struct {
	ID                 string
	Username           string
	Email              *string
	PasswordHash       *string
	FullName           *string
	Disabled           bool
	MustChangePassword bool
	LastLoginAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ExternalID         *string
	ProviderSlug       *string
	SSOManagedGroups   bool
	// Kind is UserKindHuman or UserKindService. A service user is the account
	// behind one project token: no password, no login, one project.
	Kind string
	// ProjectID is the service user's project (nil for humans).
	ProjectID *string
}

// User kinds.
const (
	UserKindHuman   = "human"
	UserKindService = "service"
)

type Group struct {
	ID          string
	Name        string
	Description *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type UserGroup struct {
	UserID  string
	GroupID string
}

type Session struct {
	ID         string
	UserID     string
	TokenHash  string
	IPAddress  *string
	UserAgent  *string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
}

type Token struct {
	ID         string
	UserID     string
	Name       string
	Kind       string
	TokenHash  string
	Scopes     []string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
	RevokedAt  *time.Time

	// Project tokens (service accounts): the project, the user who minted the
	// token and its role (viewer or developer). All nil for personal tokens.
	ProjectID   *string
	CreatedBy   *string
	ProjectRole *string
	// ProjectName and CreatedByName are read-only: filled by reads.
	ProjectName   string
	CreatedByName string
}

type PermissionTarget struct {
	ID               string
	Name             string
	RepositoryFilter string
	PathPattern      string
	Actions          []string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type PermissionAssignment struct {
	ID                 string
	PermissionTargetID string
	PrincipalType      string
	PrincipalID        string
	CreatedAt          time.Time
}

// IsService reports whether the user is a project-token service account.
func (u *User) IsService() bool { return u.Kind == UserKindService }
