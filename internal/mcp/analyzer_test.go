// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp && !noai

package mcp

import (
	"testing"
	"time"

	"context"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// workerAnalyzer adapts the real AI worker to AIAnalyzer, as cmd/server does.
type workerAnalyzer struct{ w *worker.AIAnalysisWorker }

func (a workerAnalyzer) Enabled() bool { return a.w.Enabled() }

func (a workerAnalyzer) Queue(req AIAnalysisRequest) { a.w.Notify(worker.AIAnalysisRequest(req)) }

// testAnalyzerStore is the part of the database newTestAnalyzer needs.
type testAnalyzerStore interface {
	GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error)
	GetRepoComponentBlob(ctx context.Context, repoID string, id string) (*models.ComponentBlob, error)
	UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error
}

// newTestAnalyzer returns an enabled AI worker whose debounce keeps queued
// requests from running during the test.
func newTestAnalyzer(t *testing.T, db testAnalyzerStore, log *logger.Logger) AIAnalyzer {
	t.Helper()

	w := worker.NewAIAnalysisWorker(db, ai.NewClient(ai.Config{Provider: "custom", BaseURL: "http://127.0.0.1:1"}), log).
		WithDebounce(time.Hour)
	t.Cleanup(w.Stop)

	return workerAnalyzer{w}
}
