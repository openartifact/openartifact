// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/openartifact/openartifact/internal/database/models"
)

// LatestFinishedDependencyTrackPush returns the outcome of the most recent
// finished push of any artifact, nil when none finished yet.
func (p *PostgresDB) LatestFinishedDependencyTrackPush(ctx context.Context) (*models.DependencyTrackLastPush, error) {
	r, err := p.q.LatestFinishedDependencyTrackPush(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // no push yet
	}

	if err != nil {
		return nil, fmt.Errorf("latest dependency-track push: %w", err)
	}

	return &models.DependencyTrackLastPush{OK: r.State == models.DTrackPushDone, At: r.UpdatedAt}, nil
}
