// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ListPermissionAssignmentsForTarget returns the assignments of one permission
// target with principal names, ordered by type then name.
func (p *PermissionStore) ListPermissionAssignmentsForTarget(
	ctx context.Context,
	targetID string,
) ([]models.PermissionAssignmentView, error) {
	rows, err := p.q.ListPermissionAssignmentsForTarget(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("list permission assignments for target: %w", err)
	}

	out := make([]models.PermissionAssignmentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.PermissionAssignmentView(r))
	}

	return out, nil
}
