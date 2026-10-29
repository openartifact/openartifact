// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// The *Stamp methods return a fingerprint of the rows a rendered metadata
// document is derived from. They scan one narrow index range of a single table
// (no blob join, no row decoding), so a revalidation can be answered, and a
// cached rendering looked up, without loading the rows themselves.

func (p *CondaStore) CondaRepodataStamp(ctx context.Context, repoID, platform string) (string, error) {
	stamp, err := p.q.CondaRepodataStamp(ctx, dbsqlc.CondaRepodataStampParams{RepositoryID: repoID, Platform: platform})
	if err != nil {
		return "", fmt.Errorf("failed to stamp conda repodata: %w", err)
	}

	return stamp, nil
}

// ListCondaPackageNames returns the distinct lowercase live package names of a
// conda repository in every platform.
func (p *CondaStore) ListCondaPackageNames(ctx context.Context, repoID string) ([]string, error) {
	names, err := p.q.ListCondaPackageNames(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list conda package names: %w", err)
	}

	return names, nil
}

// HasCondaPackageName reports whether a live conda package of this name exists
// in the repository, in any platform.
func (p *CondaStore) HasCondaPackageName(ctx context.Context, repoID, name string) (bool, error) {
	present, err := p.q.HasCondaPackageName(ctx, dbsqlc.HasCondaPackageNameParams{RepositoryID: repoID, Name: name})
	if err != nil {
		return false, fmt.Errorf("failed to check conda package name: %w", err)
	}

	return present, nil
}
