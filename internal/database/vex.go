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

// VEXStore owns VEX statements.
type VEXStore struct{ *core }

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
func (p *VEXStore) UpsertVEXStatement(ctx context.Context, s *models.VEXStatement) error {
	row, err := p.q.UpsertVEXStatement(ctx, vexStatementParams(s))

	return storeRow(s, row, err, "upsert VEX statement", vexFromRow)
}

func vexStatementParams(s *models.VEXStatement) dbsqlc.UpsertVEXStatementParams {
	return dbsqlc.UpsertVEXStatementParams{
		RepositoryID:    s.RepositoryID,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		CreatedBy:       s.CreatedBy,
	}
}

// ListVEXStatements lists the decisions of a repository, optionally only those
// of one artifact (artifactID != "").
func (p *VEXStore) ListVEXStatements(ctx context.Context, repoID, artifactID string) ([]*models.VEXStatement, error) {
	rows, err := p.q.ListVEXStatements(ctx, dbsqlc.ListVEXStatementsParams{RepositoryID: repoID, ArtifactID: artifactID})

	return mapRows(rows, err, "list VEX statements", vexFromRow)
}

// DeleteVEXStatement deletes a decision scoped to repoID; ErrVEXStatementNotFound when absent.
func (p *VEXStore) DeleteVEXStatement(ctx context.Context, repoID, id string) error {
	n, err := p.q.DeleteVEXStatement(ctx, dbsqlc.DeleteVEXStatementParams{RepositoryID: repoID, ID: id})
	if err != nil {
		return fmt.Errorf("failed to delete VEX statement: %w", err)
	}

	if n == 0 {
		return ErrVEXStatementNotFound
	}

	return nil
}
