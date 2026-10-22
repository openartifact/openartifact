// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// tokenColumns is the select list scanToken reads; project and creator names
// are left-joined (alias t for auth_tokens).
const tokenColumns = //nolint:gosec // SQL column list, not a credential
`t.id, t.user_id, t.name, t.kind, t.token_hash, t.scopes, t.expires_at, t.last_used_at, t.created_at, t.revoked_at,
	t.project_id, t.created_by, t.project_role, pr.name, cu.username`

const tokenFrom = //nolint:gosec // SQL, not a credential
`auth_tokens t
	LEFT JOIN projects pr ON pr.id = t.project_id
	LEFT JOIN auth_users cu ON cu.id = t.created_by`

func scanToken(row rowScanner) (*models.Token, error) {
	t := &models.Token{}

	var (
		scopesJSON                                 []byte
		expiresAt, lastUsedAt, revokedAt           sql.NullTime
		projectID, createdBy, projectRole, project sql.NullString
		creatorName                                sql.NullString
	)

	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Kind, &t.TokenHash, &scopesJSON, &expiresAt, &lastUsedAt, &t.CreatedAt, &revokedAt,
		&projectID, &createdBy, &projectRole, &project, &creatorName); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context
	}

	if err := json.Unmarshal(scopesJSON, &t.Scopes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal token scopes: %w", err)
	}

	if expiresAt.Valid {
		t.ExpiresAt = &expiresAt.Time
	}

	if lastUsedAt.Valid {
		t.LastUsedAt = &lastUsedAt.Time
	}

	if revokedAt.Valid {
		t.RevokedAt = &revokedAt.Time
	}

	if projectID.Valid {
		t.ProjectID = &projectID.String
	}

	if createdBy.Valid {
		t.CreatedBy = &createdBy.String
	}

	if projectRole.Valid {
		t.ProjectRole = &projectRole.String
	}

	t.ProjectName = project.String
	t.CreatedByName = creatorName.String

	return t, nil
}
