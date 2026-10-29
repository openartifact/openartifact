// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// nonNil returns s, or an empty slice for nil (a nil slice binds as NULL, and
// = ANY(NULL) never matches but obscures intent).
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// unifiedTokensCTE unions the three token stores into one row shape. $1 admin,
// $2 viewer id (” for none; compared as text so a missing viewer matches
// nothing), $3 project ids visible to a non-admin, $4 kinds, $5 expires-before,
// $6 name pattern, $7 exact project name (” = any). Revoked tokens are not
// listed. A federation token has no expiry.
//
//nolint:gosec // SQL over token metadata; no credential here
const unifiedTokensCTE = `
WITH u AS (
    SELECT t.id::text AS id, 'personal'::text AS type, t.name::text AS name, ou.username::text AS owner,
           ''::text AS project, ''::text AS repository, ''::text AS role, t.scopes::text AS scopes,
           t.created_at AS created_at, t.expires_at AS expires_at, t.last_used_at AS last_used_at
      FROM auth_tokens t JOIN auth_users ou ON ou.id = t.user_id
     WHERE t.project_id IS NULL AND t.revoked_at IS NULL AND ($1 OR t.user_id::text = $2)
    UNION ALL
    SELECT t.id::text, 'project', t.name::text, COALESCE(cu.username, '')::text,
           pr.name::text, '', COALESCE(t.project_role, '')::text, t.scopes::text,
           t.created_at, t.expires_at, t.last_used_at
      FROM auth_tokens t
      JOIN projects pr ON pr.id = t.project_id
      LEFT JOIN auth_users cu ON cu.id = t.created_by
     WHERE t.revoked_at IS NULL AND ($1 OR t.project_id = ANY($3::uuid[]))
    UNION ALL
    SELECT f.id::text, 'federation', f.name::text, f.created_by::text,
           COALESCE(pr.name, '')::text, r.name::text, '', '[]',
           f.created_at, NULL::timestamptz, f.last_used_at
      FROM federation_peer_tokens f
      JOIN repositories r ON r.id = f.repo_id
      LEFT JOIN projects pr ON pr.id = r.project_id
     WHERE f.revoked_at IS NULL AND r.deleted_at IS NULL AND ($1 OR r.project_id = ANY($3::uuid[]))
)
SELECT id, type, name, owner, project, repository, role, scopes, created_at, expires_at, last_used_at
  FROM u
 WHERE type = ANY($4::text[])
   AND ($5::timestamptz IS NULL OR (expires_at IS NOT NULL AND expires_at < $5))
   AND ($6 = '' OR name ILIKE $6 OR owner ILIKE $6)
   AND ($7 = '' OR project = $7)`

// ListUnifiedTokens returns one page of the unified token list (personal,
// project and federation tokens), newest first, and the number of tokens the
// caller may see that match the filters.
func (p *PostgresDB) ListUnifiedTokens(ctx context.Context, params models.TokenListParams) ([]*models.TokenListItem, int, error) {
	types := params.Types
	if len(types) == 0 {
		types = []string{models.TokenListPersonal, models.TokenListProject, models.TokenListFederation}
	}

	pattern := ""
	if params.Query != "" {
		pattern = "%" + escapeLike(params.Query) + "%"
	}

	args := []any{params.Admin, params.ViewerID, nonNil(params.ProjectIDs), types, params.ExpiresBefore, pattern, params.Project}

	var total int

	if err := p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+unifiedTokensCTE+") c", args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tokens: %w", err)
	}

	rows, err := p.db.QueryContext(ctx, unifiedTokensCTE+" ORDER BY created_at DESC, id LIMIT $8 OFFSET $9",
		append(args, params.Limit, (params.Page-1)*params.Limit)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tokens: %w", err)
	}

	defer func() { _ = rows.Close() }()

	out := []*models.TokenListItem{}

	for rows.Next() {
		t := &models.TokenListItem{}

		var scopes string

		if err := rows.Scan(
			&t.ID,
			&t.Type,
			&t.Name,
			&t.Owner,
			&t.Project,
			&t.Repository,
			&t.Role,
			&scopes,
			&t.CreatedAt,
			&t.ExpiresAt,
			&t.LastUsedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan token: %w", err)
		}

		if err := json.Unmarshal([]byte(scopes), &t.Scopes); err != nil {
			return nil, 0, fmt.Errorf("unmarshal token scopes: %w", err)
		}

		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list tokens: %w", err)
	}

	return out, total, nil
}

// ListProjectStats returns the member count, live usage and size ceiling of
// each given project in one query (projects that do not exist are absent).
func (p *PostgresDB) ListProjectStats(ctx context.Context, projectIDs []string) (map[string]*models.ProjectStats, error) {
	stats := make(map[string]*models.ProjectStats, len(projectIDs))
	if len(projectIDs) == 0 {
		return stats, nil
	}

	rows, err := p.db.QueryContext(ctx, `
SELECT pr.id::text,
       (SELECT COUNT(*) FROM project_members m WHERE m.project_id = pr.id)::int,
       COALESCE((SELECT SUM(sq.current_size_bytes) FROM repositories r JOIN storage_quotas sq ON sq.repository_id = r.id
                  WHERE r.project_id = pr.id), 0)::bigint,
       COALESCE((SELECT pq.max_size_bytes FROM project_quotas pq WHERE pq.project_id = pr.id), 0)::bigint
  FROM projects pr
 WHERE pr.id = ANY($1::uuid[])`, projectIDs)
	if err != nil {
		return nil, fmt.Errorf("list project stats: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id string
			st models.ProjectStats
		)

		if err := rows.Scan(&id, &st.MemberCount, &st.SizeBytes, &st.MaxSizeBytes); err != nil {
			return nil, fmt.Errorf("scan project stats: %w", err)
		}

		stats[id] = &st
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project stats: %w", err)
	}

	return stats, nil
}
