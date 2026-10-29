// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp && !noai

package main

import (
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/worker"
)

// workerAnalyzer lets the MCP trigger_ai_analysis tool queue work on the AI
// worker.
type workerAnalyzer struct{ w *worker.AIAnalysisWorker }

func (a workerAnalyzer) Enabled() bool { return a.w.Enabled() }

func (a workerAnalyzer) Queue(req aimcp.AIAnalysisRequest) { a.w.Notify(worker.AIAnalysisRequest(req)) }

// mcpAnalyzer is the AI analyzer handed to the MCP server.
func (a *app) mcpAnalyzer() aimcp.AIAnalyzer {
	return workerAnalyzer{a.feat.aiWorker}
}
