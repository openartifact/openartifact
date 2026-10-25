// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"

	"github.com/openartifact/openartifact/internal/ai"
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/worker"
)

// setupFeatures builds the optional services that sit on top of the workers:
// federation, AI analysis and the MCP server.
func (a *app) setupFeatures() error {
	if err := a.setupFederation(); err != nil {
		return err
	}

	a.setupAI()
	a.setupMCP()

	return nil
}

// setupFederation starts pull replication with peer instances
// (docs/operations/federation.md).
func (a *app) setupFederation() error {
	fed, err := setupFederation(context.Background(), a.cfg, a.db, a.store.blobs, a.log, a.workers.webhook, a.workers.maven, a.workers.npm)
	if err != nil {
		return a.fail("failed to start federation", err)
	}

	a.feat.fed = fed

	if fed.stop != nil {
		a.extraStops = append(a.extraStops, fed.stop)
	}

	return nil
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
		a.log.Infow("AI analysis enabled", "provider", aiSettings.Config.Provider, "model", aiSettings.Config.Model, "auto_analyze", aiSettings.AutoAnalyze)
	} else {
		a.log.Infow("AI analysis disabled")
	}

	// Uploads of every format announce themselves through the publisher; the
	// wrapper feeds them to the AI worker when auto-analysis is on.
	a.feat.uploadPublisher = worker.NewAIAutoAnalyzePublisher(a.workers.webhook, aiWorker)

	aiWorker.Start()

	a.extraStops = append(a.extraStops, aiWorker.Stop)
	a.feat.aiWorker = aiWorker
}

// setupMCP builds the MCP server when it is enabled.
func (a *app) setupMCP() {
	cfg := a.cfg

	if !cfg.MCP.Enabled {
		return
	}

	opts := aimcp.Options{
		Version:            version,
		BaseURL:            cfg.BaseURL,
		MaxBodyBytes:       cfg.MCP.MaxBodyBytes,
		RateLimitPerMinute: cfg.MCP.RateLimitPerMinute,
		AITriggerPerMinute: cfg.MCP.AITriggerPerMinute,
	}
	if cfg.BaseURL != "" {
		opts.ResourceMetadataURL = middleware.ProtectedResourceMetadataURL(cfg.BaseURL, cfg.MCP.HTTPPath)
	}

	a.feat.mcpServer = aimcp.NewServer(a.db, a.feat.aiWorker, a.log, opts)
	a.log.Infow("MCP server enabled", "path", cfg.MCP.HTTPPath)
}
