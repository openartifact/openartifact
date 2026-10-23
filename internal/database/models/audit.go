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
