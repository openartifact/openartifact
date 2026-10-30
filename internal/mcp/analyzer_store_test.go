// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"context"

	"github.com/openartifact/openartifact/internal/database/models"
)

// testAnalyzerStore is the part of the database newTestAnalyzer needs. It lives
// outside the noai-tagged files because mockDBStore embeds it in every build.
type testAnalyzerStore interface {
	GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error)
	GetRepoComponentBlob(ctx context.Context, repoID string, id string) (*models.ComponentBlob, error)
	UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error
}
