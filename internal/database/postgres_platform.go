// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
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
