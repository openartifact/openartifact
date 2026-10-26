// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

// AuditLog mirrors the audit_logs table schema: structured columns for
// compliance-grade auditing.
type AuditLog struct {
	ID           string          `db:"id"`
	Action       string          `db:"action"`
	ProjectID    *string         `db:"project_id"`
	RepositoryID *string         `db:"repository_id"`
	ActorID      *string         `db:"actor_id"`
	ActorType    *string         `db:"actor_type"`
	DetailsJSON  json.RawMessage `db:"details_json"`
	ResourceType *string         `db:"resource_type"`
	ResourceID   *string         `db:"resource_id"`
	IPAddress    *string         `db:"ip_address"`
	CreatedAt    time.Time       `db:"created_at"`
}

// AuditCursor is a keyset position in the audit listing order
// (created_at DESC, id ASC).
type AuditCursor struct {
	CreatedAt time.Time
	ID        string
}

// AuditListParams selects a page of audit entries. Entries are ordered
// newest first, ties on created_at broken by ascending id.
type AuditListParams struct {
	Actor  string
	Action string
	// ProjectID, when set, restricts the listing to entries of that project:
	// those carrying its project_id plus those of any repository it owns now.
	ProjectID string
	// RepositoryID, when set, restricts the listing to entries carrying that
	// repository_id (the repository's own activity, unlike ProjectID which
	// also folds in every repository of a project).
	RepositoryID string
	// PackageName and PackageVersion, when set, keep only entries whose
	// details name that package (and version). Only entries that record the
	// package match, see audit.Event.Package.
	PackageName    string
	PackageVersion string
	// HideProjectTokenEvents drops project.token_* entries (token mint and
	// revoke), which only maintainers may see.
	HideProjectTokenEvents bool
	// Since and Until bound created_at: entries at or after Since and strictly
	// before Until. Nil leaves that side open.
	Since *time.Time
	Until *time.Time
	Limit int
	// Offset skips that many entries. Ignored when After is set.
	Offset int
	// After, when set, returns only entries strictly after this position
	// (keyset paging: no offset scan, no total).
	After *AuditCursor
	// NoTotal skips the offset-paging total (keyset callers never read it).
	NoTotal bool
}

// AuditPage is one page of audit entries.
type AuditPage struct {
	Logs []*AuditLog
	// Next is the position of the last entry in Logs when more entries
	// follow, nil on the last page.
	Next *AuditCursor
	// Total is the number of matching entries, computed for offset paging
	// only. It is exact when TotalExact is true; otherwise it is the planner
	// estimate (unfiltered) or a lower bound (filtered).
	Total      int
	TotalExact bool
}

// AuditNameRefs lists the identifiers of one audit page whose names must be
// resolved. Every slice holds UUID strings; ResolveAuditNames looks each kind
// up with a single query regardless of how many rows the page has.
type AuditNameRefs struct {
	UserIDs             []string
	GroupIDs            []string
	RepositoryIDs       []string
	ProjectIDs          []string
	PermissionTargetIDs []string
	GPGKeyIDs           []string
	SSOProviderIDs      []string
	BundleIDs           []string
	TokenIDs            []string
}

// Empty reports whether there is nothing to resolve.
func (r AuditNameRefs) Empty() bool {
	return len(r.UserIDs)+len(r.GroupIDs)+len(r.RepositoryIDs)+len(r.ProjectIDs)+len(r.PermissionTargetIDs)+
		len(r.GPGKeyIDs)+len(r.SSOProviderIDs)+len(r.BundleIDs)+len(r.TokenIDs) == 0
}

// AuditNames maps identifier to display name, one map per kind of AuditNameRefs.
// Identifiers whose row no longer exists are absent.
type AuditNames struct {
	Users             map[string]string
	Groups            map[string]string
	Repositories      map[string]string
	Projects          map[string]string
	PermissionTargets map[string]string
	GPGKeys           map[string]string
	SSOProviders      map[string]string
	Bundles           map[string]string
	Tokens            map[string]string
}

// NewAuditNames returns an AuditNames with every map allocated.
func NewAuditNames() *AuditNames {
	return &AuditNames{
		Users: map[string]string{}, Groups: map[string]string{}, Repositories: map[string]string{},
		Projects: map[string]string{}, PermissionTargets: map[string]string{}, GPGKeys: map[string]string{},
		SSOProviders: map[string]string{}, Bundles: map[string]string{}, Tokens: map[string]string{},
	}
}
