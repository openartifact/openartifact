// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// VersionResolverStore is the part of the database VersionResolver needs.
type VersionResolverStore interface {
	GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error)
	GetNPMVersion(ctx context.Context, packageID string, version string) (*models.NPMVersion, error)
}

type VersionResolver struct {
	db VersionResolverStore
}

func NewVersionResolver(db VersionResolverStore) *VersionResolver {
	return &VersionResolver{db: db}
}

func (vr *VersionResolver) Resolve(ctx context.Context, packageID, target string) (string, error) {
	tags, err := vr.db.GetNPMDistTags(ctx, packageID)
	if err != nil {
		return "", fmt.Errorf("get dist-tags: %w", err)
	}

	if version, ok := tags[target]; ok {
		return version, nil
	}

	v, err := vr.db.GetNPMVersion(ctx, packageID, target)
	if err != nil {
		return "", fmt.Errorf("get npm version: %w", err)
	}

	if v == nil {
		return "", fmt.Errorf("version not found: %s", target) //nolint:err113 // dynamic error with runtime context
	}

	return target, nil
}
