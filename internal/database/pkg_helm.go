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
//
// Mapping of a chart onto the generic package tables (one chart version, one
// .tgz, is one version and one file; the chart name is the package):
//
//	packages          name = name_key = chart name, attrs {}
//	package_versions  version = version_key = chart version; metadata
//	                  {app_version, description, home, icon, keywords, sources,
//	                  maintainers, api_version, type}: empty values are omitted,
//	                  a missing api_version reads "v2" and a missing type
//	                  "application"
//	package_files     path name/version/name-version.tgz, filename
//	                  name-version.tgz, the chart blob (its sha256 is the chart
//	                  digest), role primary, unit_key name@version (federation)
//
// The id of a chart (the chart-list JSON) is package_files.id. A duplicate
// publish is a live version with a live file (ErrDuplicate).
type HelmStore struct{ *core }

// helmMetadata is package_versions.metadata of a chart.
type helmMetadata struct {
	AppVersion  string              `json:"app_version,omitempty"`
	Description string              `json:"description,omitempty"`
	Home        string              `json:"home,omitempty"`
	Icon        string              `json:"icon,omitempty"`
	Keywords    []string            `json:"keywords,omitempty"`
	Sources     []string            `json:"sources,omitempty"`
	Maintainers []map[string]string `json:"maintainers,omitempty"`
	APIVersion  string              `json:"api_version,omitempty"`
	Type        string              `json:"type,omitempty"`
}

// helmChartFilename is the file name of a chart archive.
func helmChartFilename(name, version string) string {
	return name + "-" + version + ".tgz"
}

// CreateHelmChart creates a new Helm chart.
func (p *HelmStore) CreateHelmChart(ctx context.Context, chart *models.HelmChart) error {
	filename := helmChartFilename(chart.Name, chart.Version)

	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:     chart.RepositoryID,
		Format:     "helm",
		Name:       chart.Name,
		NameKey:    chart.Name,
		Version:    chart.Version,
		VersionKey: chart.Version,
		VersionMeta: helmMetadata{
			AppVersion:  chart.AppVersion,
			Description: chart.Description,
			Home:        chart.Home,
			Icon:        chart.Icon,
			Keywords:    chart.Keywords,
			Sources:     chart.Sources,
			Maintainers: chart.Maintainers,
			APIVersion:  chart.APIVersion,
			Type:        chart.Type,
		},
		Exclusive: true,
		CreatedAt: chart.CreatedAt,
		Files: []FileWrite{{
			Path:     chart.Name + "/" + chart.Version + "/" + filename,
			Filename: filename,
			Blob: BlobWrite{
				SHA256: chart.Digest, Key: chart.BlobKey, MediaType: "application/gzip", Size: chart.SizeBytes,
			},
			UnitKey: chart.Name + "@" + chart.Version,
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create helm chart: %w", err)
	}

	chart.ID, chart.CreatedAt, chart.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

// helmChartRow lists the sqlc row types helmChartFromRow converts; they share the columns of GetHelmChartRow.
type helmChartRow interface {
	dbsqlc.GetHelmChartRow | dbsqlc.ListHelmChartsRow
}

func helmChartFromRow[R helmChartRow](in R) *models.HelmChart {
	row := dbsqlc.GetHelmChartRow(in)

	var meta helmMetadata

	_ = json.Unmarshal(row.Metadata, &meta)

	chart := &models.HelmChart{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Version:      row.Version,
		AppVersion:   meta.AppVersion,
		Description:  meta.Description,
		Keywords:     meta.Keywords,
		Home:         meta.Home,
		Sources:      meta.Sources,
		Maintainers:  meta.Maintainers,
		Icon:         meta.Icon,
		APIVersion:   meta.APIVersion,
		Type:         meta.Type,
		BlobKey:      row.BlobKey,
		Digest:       row.Digest,
		SizeBytes:    row.SizeBytes,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}

	if chart.APIVersion == "" {
		chart.APIVersion = "v2"
	}

	if chart.Type == "" {
		chart.Type = "application"
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

// DeleteHelmChart hard-deletes the live file of a chart version; the version
// and the package go with their last file.
func (p *HelmStore) DeleteHelmChart(ctx context.Context, repoID, name, version string) error {
	ids, err := p.q.ListHelmChartFileIDs(ctx, dbsqlc.ListHelmChartFileIDsParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("failed to delete helm chart: %w", err)
	}

	if _, err := p.deleteFiles(ctx, repoID, ids); err != nil {
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
