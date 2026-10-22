// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"strings"
	"time"
)

// SystemProjectID is the built-in system project: the "unassigned" project
// every repository lived in before projects took part in authorization. Its
// membership grants nothing, whatever its roles say; project-derived access
// applies to every other project.
const SystemProjectID = "00000000-0000-0000-0000-000000000001"

// Project member roles, lowest to highest. Groups may hold every role except
// owner (ownership stays personal).
const (
	ProjectRoleViewer     = "viewer"
	ProjectRoleDeveloper  = "developer"
	ProjectRoleMaintainer = "maintainer"
	ProjectRoleOwner      = "owner"
)

// Repository types a project may allow (Project.AllowedRepoTypes).
const (
	ProjectRepoTypeHosted = "hosted"
	ProjectRepoTypeProxy  = "proxy"
	ProjectRepoTypeGroup  = "group"
)

// ProjectRoleRank orders the roles (viewer 1 < developer 2 < maintainer 3 <
// owner 4); 0 for an unknown role.
func ProjectRoleRank(role string) int {
	switch role {
	case ProjectRoleViewer:
		return 1
	case ProjectRoleDeveloper:
		return 2
	case ProjectRoleMaintainer:
		return 3
	case ProjectRoleOwner:
		return 4
	default:
		return 0
	}
}

// Project owns repositories. Members (users and groups) hold a role on them.
type Project struct {
	ID          string
	Name        string
	DisplayName *string
	Description *string
	// OwnerID is the recorded owner; it always counts as an owner member.
	OwnerID *string
	// Slug is the URL-safe unique identifier derived from the name (ProjectSlug).
	Slug string
	// AllowedFormats limits the formats of self-service repositories; nil
	// allows every format.
	AllowedFormats []string
	// AllowedRepoTypes limits the repository types (hosted, proxy, group).
	AllowedRepoTypes []string
	// RepoNamePrefix is the prefix self-service repository names must carry.
	RepoNamePrefix string
	// AllowPublicRepos lets project repositories be anonymously readable.
	AllowPublicRepos bool
	// MaxRepositories caps the repositories of the project; 0 = unlimited.
	MaxRepositories int
	// TokenMaxTTLDays caps the lifetime of project tokens; 0 = no cap.
	TokenMaxTTLDays int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ProjectMember is a user's membership row.
type ProjectMember struct {
	ID        string
	ProjectID string
	UserID    string
	Username  string // read-only: filled by reads, ignored on writes
	Role      string // viewer, developer, maintainer, owner
	CreatedAt time.Time
}

// ProjectGroup attaches a group to a project with a role (viewer, developer
// or maintainer).
type ProjectGroup struct {
	ProjectID string
	GroupID   string
	GroupName string
	Role      string
	CreatedBy *string
	CreatedAt time.Time
}

// ProjectGrant is one role a user holds in a project: a membership row, the
// project's recorded owner (owner), or a group the user belongs to. A user can
// hold several grants per project; the highest rank wins.
type ProjectGrant struct {
	ProjectID string
	Role      string
}

// MaxProjectSlugLen is the length of projects.slug (varchar(64)).
const MaxProjectSlugLen = 64

// ProjectSlug derives a project's slug from its name: lowercased, every run
// of characters outside [a-z0-9] collapsed to a single '-', leading and
// trailing '-' trimmed. The result is empty when the name has no letter or
// digit; callers must also reject slugs longer than MaxProjectSlugLen.
func ProjectSlug(name string) string {
	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}

			dash = false

			b.WriteRune(r)

			continue
		}

		dash = true
	}

	return b.String()
}
