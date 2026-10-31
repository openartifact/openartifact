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
	// Kind is UserKindHuman, UserKindService or UserKindServiceAccount. A
	// service user is the account behind one project token: no password, no
	// login, one project. A service account is an administrator-managed
	// non-human principal: no password, no login, no project.
	Kind string
	// ProjectID is the service user's project (nil for humans).
	ProjectID *string
}

// User kinds.
const (
	UserKindHuman          = "human"
	UserKindService        = "service"
	UserKindServiceAccount = "service_account"
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
	// SSOManaged marks memberships an SSO login derives from IdP claims.
	SSOManaged bool
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

// SessionSSO links a session to the SSO provider that created it. IDToken is
// stored encrypted; it is empty for providers without an ID token.
type SessionSSO struct {
	ProviderSlug string
	IDToken      string
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

// IsService reports whether the user is the account behind a project token.
func (u *User) IsService() bool { return u.Kind == UserKindService }

// IsServiceAccount reports whether the user is an administrator-managed
// service account.
func (u *User) IsServiceAccount() bool { return u.Kind == UserKindServiceAccount }

// IsHuman reports whether the user is a person (the kind of every row that is
// neither a project-token account nor a service account): the only kind that
// has a password, a session and a place in the users API.
func (u *User) IsHuman() bool { return !u.IsService() && !u.IsServiceAccount() }

// OIDCCondition is one claim condition of an OIDCTrustPolicy: the token's claim
// must match at least one of Patterns (a glob where "*" matches any run of
// characters, "/" included, and "?" one character).
type OIDCCondition struct {
	Claim    string   `json:"claim"`
	Patterns []string `json:"patterns"`
}

// OIDCTrustPolicy is an administrator's rule for exchanging an ID token of an
// external issuer (a CI system) for a short-lived token of a service account.
type OIDCTrustPolicy struct {
	ID               string
	Name             string
	Issuer           string
	Audience         string
	Conditions       []OIDCCondition
	ServiceAccountID string
	TokenTTLSeconds  int
	Enabled          bool
	CreatedBy        string
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// ServiceAccountName and ServiceAccountDisabled are read-only: filled by reads.
	ServiceAccountName     string
	ServiceAccountDisabled bool
}
