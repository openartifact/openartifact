// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// HelmStore owns the Helm charts.
type HelmStore struct{ *core }

// CreateHelmChart creates a new Helm chart.
func (p *HelmStore) CreateHelmChart(ctx context.Context, chart *models.HelmChart) error {
	// Marshal maintainers to JSON
	var maintainersJSON []byte

	if len(chart.Maintainers) > 0 {
		data, err := json.Marshal(chart.Maintainers)
		if err != nil {
			return fmt.Errorf("failed to marshal maintainers: %w", err)
		}

		maintainersJSON = data
	}

	blobID, err := p.upsertBlobWithMediaType(ctx, chart.Digest, chart.BlobKey, "application/gzip", chart.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for helm chart: %w", err)
	}

	row, err := p.q.CreateHelmChart(ctx, dbsqlc.CreateHelmChartParams{
		CreatedAt:    createdAtOrNil(chart.CreatedAt),
		RepositoryID: chart.RepositoryID,
		Name:         chart.Name,
		Version:      chart.Version,
		AppVersion:   strPtr(chart.AppVersion),
		Description:  strPtr(chart.Description),
		Keywords:     chart.Keywords,
		Home:         strPtr(chart.Home),
		Sources:      chart.Sources,
		Maintainers:  maintainersJSON,
		Icon:         strPtr(chart.Icon),
		ApiVersion:   chart.APIVersion,
		Type:         chart.Type,
		BlobID:       blobID,
	})

	return finishCreate(insertedRow(row), err, "create helm chart", &chart.ID, &chart.CreatedAt, &chart.UpdatedAt)
}

// helmChartFromRow maps a sqlc helm chart row; the list rows convert to it.
// helmChartRow lists the sqlc row types helmChartFromRow converts; they share the columns of GetHelmChartRow.
type helmChartRow interface {
	dbsqlc.GetHelmChartRow | dbsqlc.ListHelmChartsRow
}

func helmChartFromRow[R helmChartRow](in R) *models.HelmChart {
	row := dbsqlc.GetHelmChartRow(in)

	chart := &models.HelmChart{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Version:      row.Version,
		AppVersion:   derefStr(row.AppVersion),
		Description:  derefStr(row.Description),
		Keywords:     row.Keywords,
		Home:         derefStr(row.Home),
		Sources:      row.Sources,
		Icon:         derefStr(row.Icon),
		APIVersion:   row.ApiVersion,
		Type:         row.Type,
		BlobKey:      row.BlobKey,
		Digest:       row.Digest,
		SizeBytes:    row.SizeBytes,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}

	if len(row.Maintainers) > 0 {
		_ = json.Unmarshal(row.Maintainers, &chart.Maintainers)
	}

	return chart
}

func (p *HelmStore) GetHelmChart(ctx context.Context, repoID, name, version string) (*models.HelmChart, error) {
	row, err := p.q.GetHelmChart(ctx, dbsqlc.GetHelmChartParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})

	return mapRow(row, err, "get helm chart", helmChartFromRow)
}

func (p *HelmStore) DeleteHelmChart(ctx context.Context, repoID, name, version string) error {
	err := p.q.DeleteHelmChart(ctx, dbsqlc.DeleteHelmChartParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("failed to delete helm chart: %w", err)
	}

	return nil
}

func (p *HelmStore) ListHelmCharts(ctx context.Context, repoID string) ([]*models.HelmChart, error) {
	rows, err := p.q.ListHelmCharts(ctx, repoID)

	return mapRowsOrNil(rows, err, "list helm charts", helmChartFromRow)
}

// HelmIndexStamp is a cheap validator of a repository's index.yaml: it
// changes exactly when a live chart is added or removed.
func (p *HelmStore) HelmIndexStamp(ctx context.Context, repoID string) (string, error) {
	stamp, err := p.q.HelmIndexStamp(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to stamp helm index: %w", err)
	}

	return stamp, nil
}
