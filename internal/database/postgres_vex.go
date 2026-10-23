// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrVEXStatementNotFound is returned when a VEX statement does not exist in the repository.
var ErrVEXStatementNotFound = errors.New("VEX statement not found")

func vexFromRow(r dbsqlc.VexStatement) *models.VEXStatement {
	return &models.VEXStatement{
		ID:              r.ID,
		RepositoryID:    r.RepositoryID,
		ArtifactID:      r.ArtifactID,
		VulnID:          r.VulnID,
		Status:          r.Status,
		Justification:   r.Justification,
		ImpactStatement: r.ImpactStatement,
		ActionStatement: r.ActionStatement,
		CreatedBy:       r.CreatedBy,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

// UpsertVEXStatement records (or replaces) the decision for
// (RepositoryID, ArtifactID, VulnID) and fills the stored row back into s.
func (p *PostgresDB) UpsertVEXStatement(ctx context.Context, s *models.VEXStatement) error {
	row, err := p.q.UpsertVEXStatement(ctx, dbsqlc.UpsertVEXStatementParams{
		RepositoryID:    s.RepositoryID,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		CreatedBy:       s.CreatedBy,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert VEX statement: %w", err)
	}

	*s = *vexFromRow(row)

	return nil
}

// ListVEXStatements lists the decisions of a repository, optionally only those
// of one artifact (artifactID != "").
func (p *PostgresDB) ListVEXStatements(ctx context.Context, repoID, artifactID string) ([]*models.VEXStatement, error) {
	rows, err := p.q.ListVEXStatements(ctx, dbsqlc.ListVEXStatementsParams{RepositoryID: repoID, ArtifactID: artifactID})
	if err != nil {
		return nil, fmt.Errorf("failed to list VEX statements: %w", err)
	}

	out := make([]*models.VEXStatement, 0, len(rows))
	for _, r := range rows {
		out = append(out, vexFromRow(r))
	}

	return out, nil
}

// DeleteVEXStatement deletes a decision scoped to repoID; ErrVEXStatementNotFound when absent.
func (p *PostgresDB) DeleteVEXStatement(ctx context.Context, repoID, id string) error {
	n, err := p.q.DeleteVEXStatement(ctx, dbsqlc.DeleteVEXStatementParams{RepositoryID: repoID, ID: id})
	if err != nil {
		return fmt.Errorf("failed to delete VEX statement: %w", err)
	}

	if n == 0 {
		return ErrVEXStatementNotFound
	}

	return nil
}
