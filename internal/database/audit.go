// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// AuditStore owns the audit log.
type AuditStore struct{ *core }

var _ AuditRetentionStore = (*PostgresDB)(nil)

// AuditRetentionStore deletes audit entries past their retention window. It is
// implemented by *PostgresDB and found by the retention worker with a type
// assertion on Database.
type AuditRetentionStore interface {
	// PurgeAuditLogsBefore deletes up to limit audit entries created before
	// cutoff, oldest first, and returns how many it removed. A result below
	// limit means nothing older than cutoff is left.
	PurgeAuditLogsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

// PurgeAuditLogsBefore implements AuditRetentionStore.
func (p *AuditStore) PurgeAuditLogsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	n, err := p.q.PurgeAuditLogsBefore(ctx, dbsqlc.PurgeAuditLogsBeforeParams{
		Cutoff: cutoff,
		Batch:  toInt32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("purge audit logs: %w", err)
	}

	return n, nil
}

func (p *AuditStore) InsertAuditLog(ctx context.Context, event *models.AuditLog) error {
	var ip *netip.Addr
	if event.IPAddress != nil {
		addr, err := netip.ParseAddr(*event.IPAddress)
		if err == nil {
			ip = &addr
		}
	}
	// An unset id or time leaves the column default to the database.
	var (
		id        *string
		createdAt *time.Time
	)

	if event.ID != "" {
		id = &event.ID
	}

	if !event.CreatedAt.IsZero() {
		createdAt = &event.CreatedAt
	}

	if err := p.q.InsertAuditLog(ctx, dbsqlc.InsertAuditLogParams{
		ID:           id,
		CreatedAt:    createdAt,
		Action:       event.Action,
		ActorID:      event.ActorID,
		ActorType:    event.ActorType,
		DetailsJson:  event.DetailsJSON,
		ResourceType: event.ResourceType,
		ResourceID:   event.ResourceID,
		RepositoryID: event.RepositoryID,
		ProjectID:    event.ProjectID,
		IpAddress:    ip,
	}); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}

	return nil
}

// auditExactCountMax bounds the work spent on an audit total: tables the
// planner estimates at or below it are counted exactly, larger ones are not
// counted at all (see auditTotal).
const auditExactCountMax = 10_000

// ErrInvalidAuditLimit is returned by ListAuditLogs for a page size below 1.
var ErrInvalidAuditLimit = errors.New("audit page limit must be positive")

// auditFilterWhere builds the project/actor/action predicate shared by the page
// and count queries. Placeholders start at $1.
func auditFilterWhere(params models.AuditListParams) (string, []any) {
	projectID, actor, action := params.ProjectID, params.Actor, params.Action

	where := "WHERE 1=1"
	args := []any{}

	// A project's activity is every entry naming the project plus every entry
	// of a repository it owns now (repository events carry no project_id).
	// The repository branch is written as = ANY(ARRAY(subquery)): the owned
	// repository ids are evaluated once into an array, which lets the planner
	// BitmapOr the project_id and repository_id indexes (an IN (subquery)
	// becomes a semi-join and forces a scan of the whole table).
	if projectID != "" {
		args = append(args, projectID)
		where += fmt.Sprintf(
			" AND (project_id = $%[1]d::uuid OR repository_id = ANY(ARRAY(SELECT id FROM repositories WHERE project_id = $%[1]d::uuid)))",
			len(args),
		)
	}

	// One repository's own activity: its entries only, no project fold-in.
	if params.RepositoryID != "" {
		args = append(args, params.RepositoryID)
		where += fmt.Sprintf(" AND repository_id = $%d::uuid", len(args))
	}

	// Entries that record the package (and version) they concern, see
	// audit.Event.Package.
	if params.PackageName != "" {
		args = append(args, params.PackageName)
		where += fmt.Sprintf(" AND details_json->>'package' = $%d", len(args))
	}

	if params.PackageVersion != "" {
		args = append(args, params.PackageVersion)
		where += fmt.Sprintf(" AND details_json->>'version' = $%d", len(args))
	}

	// Project token mint/revoke events are for maintainers and above only.
	if params.HideProjectTokenEvents {
		where += ` AND action NOT LIKE 'project.token\_%'`
	}

	if action != "" {
		args = append(args, "%"+escapeLike(action)+"%")
		where += fmt.Sprintf(" AND action ILIKE $%d", len(args))
	}

	// The human-readable actor (username / token name) is stored in
	// details_json; the user UUID is in actor_id. Accept either.
	if actor != "" {
		args = append(args, "%"+escapeLike(actor)+"%")
		where += fmt.Sprintf(" AND (details_json->>'actor' ILIKE $%[1]d OR actor_id::text ILIKE $%[1]d)", len(args))
	}

	if params.Since != nil {
		args = append(args, *params.Since)
		where += fmt.Sprintf(" AND created_at >= $%d", len(args))
	}

	if params.Until != nil {
		args = append(args, *params.Until)
		where += fmt.Sprintf(" AND created_at < $%d", len(args))
	}

	return where, args
}

// ListAuditLogs returns one page of audit entries, newest first. It reads
// Limit+1 rows to learn whether more follow, so no COUNT(*) runs for keyset
// (After) paging, and offset paging only counts when the page itself cannot
// answer (see auditTotal).
func (p *AuditStore) ListAuditLogs(ctx context.Context, params models.AuditListParams) (*models.AuditPage, error) {
	if params.Limit < 1 {
		return nil, fmt.Errorf("list audit logs: %w, got %d", ErrInvalidAuditLimit, params.Limit)
	}

	where, args := auditFilterWhere(params)
	filtered := len(args) > 0
	offset := params.Offset

	if c := params.After; c != nil {
		// created_at <= $c is the index condition; the OR only trims the
		// rows tied with the cursor.
		args = append(args, c.CreatedAt, c.ID)
		where += fmt.Sprintf(" AND created_at <= $%[1]d AND (created_at < $%[1]d OR id > $%[2]d::uuid)", len(args)-1, len(args))
		offset = 0
	}

	args = append(args, params.Limit+1, offset)
	// The formatted parts are the predicate built by auditFilterWhere from
	// constant clauses and $n placeholders (values are always bound
	// arguments) and placeholder numbers; no caller input reaches the text.
	query := fmt.Sprintf( //nolint:gosec // G201: where is built from constant clauses with bound placeholders
		"SELECT id, action, project_id, repository_id, actor_id, actor_type, details_json::text, resource_type, resource_id, "+
			"host(ip_address) AS ip_address, created_at FROM audit_logs %s ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d",
		where,
		len(args)-1,
		len(args),
	)

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	logs := make([]*models.AuditLog, 0, params.Limit+1)

	for rows.Next() {
		l := &models.AuditLog{}

		var (
			detailsStr string
			ipStr      *string
		)

		if err := rows.Scan(
			&l.ID,
			&l.Action,
			&l.ProjectID,
			&l.RepositoryID,
			&l.ActorID,
			&l.ActorType,
			&detailsStr,
			&l.ResourceType,
			&l.ResourceID,
			&ipStr,
			&l.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}

		l.IPAddress = ipStr
		l.DetailsJSON = json.RawMessage(detailsStr)
		logs = append(logs, l)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}

	page := &models.AuditPage{Logs: logs}

	hasMore := len(logs) > params.Limit
	if hasMore {
		page.Logs = logs[:params.Limit]
		last := page.Logs[params.Limit-1]
		page.Next = &models.AuditCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if params.After != nil || params.NoTotal {
		return page, nil
	}

	// Count args are the filter args only (the cursor branch returned above).
	page.Total, page.TotalExact, err = p.auditTotal(ctx, where, args[:len(args)-2], filtered, offset, len(page.Logs), hasMore)
	if err != nil {
		return nil, err
	}

	return page, nil
}

// auditTotal reports the number of matching audit entries without a full
// COUNT(*) whenever the page already answers it. args are the filter
// arguments of where; offset and returned describe the page just read, and
// hasMore whether a further entry exists.
//
//   - Last page (or an empty first page): offset+returned is the exact total.
//   - Otherwise the table size decides. Small (planner estimate <= cap) or
//     never analysed: a count bounded to cap+1 rows (exact up to the cap, a
//     lower bound above it). Large: unfiltered reports the planner estimate,
//     filtered the lower bound offset+returned+1; neither touches the table.
//
// An empty page past the end of a large filtered result still needs the
// bounded count, because offset says nothing about the real total there.
func (p *AuditStore) auditTotal(
	ctx context.Context,
	where string,
	args []any,
	filtered bool,
	offset, returned int,
	hasMore bool,
) (int, bool, error) {
	seen := offset + returned
	if !hasMore && (returned > 0 || offset == 0) {
		return seen, true, nil
	}

	var estimate int64

	err := p.db.QueryRowContext(ctx, `SELECT reltuples::bigint FROM pg_class WHERE oid = 'audit_logs'::regclass`).Scan(&estimate)
	if err != nil {
		return 0, false, fmt.Errorf("estimate audit logs: %w", err)
	}

	if estimate > auditExactCountMax {
		switch {
		case !filtered && hasMore:
			return max(int(estimate), seen+1), false, nil
		case !filtered:
			return int(estimate), false, nil
		case hasMore:
			return seen + 1, false, nil
		}
	}

	var counted int

	countQuery := fmt.Sprintf( //nolint:gosec // G201: where is built from constant clauses with bound placeholders
		"SELECT count(*) FROM (SELECT 1 FROM audit_logs %s LIMIT $%d) s",
		where,
		len(args)+1,
	)
	if err := p.db.QueryRowContext(ctx, countQuery, append(args, auditExactCountMax+1)...).Scan(&counted); err != nil {
		return 0, false, fmt.Errorf("count audit logs: %w", err)
	}

	return counted, counted <= auditExactCountMax, nil
}

// ResolveAuditNames looks up the display names of the entities one audit page
// refers to, one query for all kinds. Entities that no longer exist are absent
// from the result.
func (p *AuditStore) ResolveAuditNames(ctx context.Context, refs models.AuditNameRefs) (*models.AuditNames, error) {
	names := models.NewAuditNames()

	if refs.Empty() {
		return names, nil
	}

	rows, err := p.q.ResolveAuditNames(ctx, dbsqlc.ResolveAuditNamesParams{
		UserIds:             nonNil(refs.UserIDs),
		GroupIds:            nonNil(refs.GroupIDs),
		RepositoryIds:       nonNil(refs.RepositoryIDs),
		ProjectIds:          nonNil(refs.ProjectIDs),
		PermissionTargetIds: nonNil(refs.PermissionTargetIDs),
		GpgKeyIds:           nonNil(refs.GPGKeyIDs),
		SsoProviderIds:      nonNil(refs.SSOProviderIDs),
		BundleIds:           nonNil(refs.BundleIDs),
		TokenIds:            nonNil(refs.TokenIDs),
	})
	if err != nil {
		return nil, fmt.Errorf("resolve audit names: %w", err)
	}

	byKind := map[string]map[string]string{
		"user": names.Users, "group": names.Groups, "repository": names.Repositories, "project": names.Projects,
		"permission_target": names.PermissionTargets, "gpg_key": names.GPGKeys, "sso_provider": names.SSOProviders,
		"bundle": names.Bundles, "token": names.Tokens,
	}

	for _, r := range rows {
		if m, ok := byKind[r.Kind]; ok {
			m[r.ID] = r.Name
		}
	}

	return names, nil
}
