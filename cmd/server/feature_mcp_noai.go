// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp && noai

package main

import aimcp "github.com/openartifact/openartifact/internal/mcp"

// mcpAnalyzer is nil without AI analysis: trigger_ai_analysis answers that AI
// analysis is not enabled.
func (a *app) mcpAnalyzer() aimcp.AIAnalyzer { return nil }
