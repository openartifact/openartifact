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

// ListDebPackageStamps returns the live package files of a hosted deb
// repository without their control fields, in the order of ListDebPackageEntries
// (distribution, component, architecture, name, version). A deb group keeps the
// stanza it rendered for a file under its stamp and reads the controls of the
// files it has no stanza for with ListDebPackagesWithControls.
func (p *DebStore) ListDebPackageStamps(ctx context.Context, repoID string) ([]models.DebPackageStamp, error) {
	rows, err := p.q.ListDebPackageStamps(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("list deb package stamps: %w", err)
	}

	out := make([]models.DebPackageStamp, len(rows))

	for i, row := range rows {
		out[i] = models.DebPackageStamp{
			ID: row.ID, Name: row.Name, Version: row.Version, Arch: row.Arch, Filename: row.Filename,
			DistributionID: row.DistributionID, Distribution: row.Distribution, ComponentName: row.ComponentName,
			SHA256: row.Sha256, Size: row.Size, InstalledSize: row.InstalledSize,
			ControlRev: row.ControlRev,
		}
	}

	return out, nil
}

// ListDebPackagesWithControls returns the live package files of the repository
// among ids with their control fields, in no particular order. A file that was
// deleted since the caller listed it is absent.
func (p *DebStore) ListDebPackagesWithControls(ctx context.Context, repoID string, ids []string) ([]models.DebPackageStamp, error) {
	rows, err := p.q.ListDebPackagesWithControls(ctx, dbsqlc.ListDebPackagesWithControlsParams{RepositoryID: repoID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("list deb package controls: %w", err)
	}

	out := make([]models.DebPackageStamp, len(rows))

	for i, row := range rows {
		s := models.DebPackageStamp{
			ID: row.ID, Name: row.Name, Version: row.Version, Arch: row.Arch, Filename: row.Filename,
			DistributionID: row.DistributionID, Distribution: row.Distribution, ComponentName: row.ComponentName,
			SHA256: row.Sha256, Size: row.Size, InstalledSize: row.InstalledSize,
			ControlRev: row.ControlRev,
		}

		if len(row.ControlFields) > 0 {
			if err := json.Unmarshal(row.ControlFields, &s.ControlFields); err != nil {
				return nil, fmt.Errorf("decode control fields for %s: %w", s.ID, err)
			}
		}

		out[i] = s
	}

	return out, nil
}
