// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"strings"
	"time"
)

// SystemOrgID is the built-in system organization: the legacy default
// organization every repository lived in before organizations took part in
// authorization. Its membership grants nothing, whatever its roles or
// MemberRole say; organization-derived access applies to every other
// organization.
const SystemOrgID = "00000000-0000-0000-0000-000000000001"

// Organization-wide access levels (Organization.MemberRole).
const (
	OrgAccessRead  = "read"
	OrgAccessWrite = "write"
	OrgAccessAdmin = "admin"
)

type Organization struct {
	ID          string
	Name        string
	DisplayName *string
	Description *string
	OwnerID     *string
	// MemberRole is the access level (read, write or admin) plain members get
	// on the organization's repositories. Owners and admins always get admin.
	MemberRole string
	// Slug is the URL-safe unique identifier derived from the name (OrgSlug).
	Slug      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type OrgMember struct {
	ID        string
	OrgID     string
	UserID    string
	Role      string // "owner", "admin", "member"
	CreatedAt time.Time
}

// OrgGrant is one organization membership of a user, as needed to authorize
// access to the organization's repositories. Role is the membership role
// ("owner", "admin" or "member"; the organization's recorded owner counts as
// "owner" even without a membership row) and MemberRole the organization's
// access level for plain members.
type OrgGrant struct {
	OrgID      string
	Role       string
	MemberRole string
}

// MaxOrgSlugLen is the length of organizations.slug (varchar(64)).
const MaxOrgSlugLen = 64

// OrgSlug derives an organization's slug from its name: lowercased, every run
// of characters outside [a-z0-9] collapsed to a single '-', leading and
// trailing '-' trimmed. The result is empty when the name has no letter or
// digit; callers must also reject slugs longer than MaxOrgSlugLen.
func OrgSlug(name string) string {
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
