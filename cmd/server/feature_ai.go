// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !noai

package main

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/worker"
)

// aiBuilt reports that this binary contains AI analysis; the public system
// info advertises it as ai_available.
const aiBuilt = true

// aiFeature is what AI analysis adds to the features struct.
type aiFeature struct {
	envAI    ai.Settings
	aiWorker *worker.AIAnalysisWorker
}

// setupAI builds the AI analysis worker. The effective settings (env/YAML
// layered under the admin-saved ones) are applied at startup and again by the
// admin API after every change, so no restart is needed.
func (a *app) setupAI() {
	cfg := a.cfg.AI

	a.feat.envAI = ai.Settings{
		Enabled:     cfg.Enabled,
		AutoAnalyze: cfg.AutoAnalyze,
		Config: ai.Config{
			Provider:     cfg.Provider,
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			Model:        cfg.Model,
			MaxTokens:    cfg.MaxTokens,
			TimeoutSecs:  cfg.TimeoutSecs,
			SystemPrompt: cfg.SystemPrompt,
		},
	}

	aiSettings := ai.LoadSettings(context.Background(), a.db, a.feat.envAI)
	aiWorker := worker.NewAIAnalysisWorker(a.db, nil, a.log).WithBlobStore(a.store.blobs)
	aiWorker.Configure(aiSettings)

	if aiSettings.Enabled {
		a.log.Infow(
			"AI analysis enabled",
			"provider",
			aiSettings.Config.Provider,
			"model",
			aiSettings.Config.Model,
			"auto_analyze",
			aiSettings.AutoAnalyze,
		)
	} else {
		a.log.Infow("AI analysis disabled")
	}

	// Uploads of every format announce themselves through the publisher; the
	// wrapper feeds them to the AI worker when auto-analysis is on.
	a.feat.uploadPublisher = worker.NewAIAutoAnalyzePublisher(a.workers.webhook, aiWorker)

	a.start(aiWorker.Start)

	a.extraStops = append(a.extraStops, aiWorker.Stop)
	a.feat.aiWorker = aiWorker
}

// registerAIRoutes registers the per-repository AI analysis routes and the
// admin AI configuration ones.
func (a *app) registerAIRoutes(authedAPI chi.Router) {
	api.NewAdminAIHandler(a.db, a.log, a.web.evaluator).WithRuntime(a.feat.aiWorker, a.feat.envAI).Register(authedAPI)
	api.NewAIHandler(a.db, a.log, a.web.evaluator, a.feat.aiWorker).RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")
}

// aiEnabled reports whether AI analysis is effectively on: the admin-saved
// settings over the process environment.
func (a *app) aiEnabled() func(context.Context) bool {
	return func(ctx context.Context) bool {
		return ai.LoadSettings(ctx, a.db, a.feat.envAI).Enabled
	}
}
