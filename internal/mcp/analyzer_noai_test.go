// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp && noai

package mcp

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeAnalyzer is an enabled analyzer that accepts every request: the AI
// worker is not compiled into this build.
type fakeAnalyzer struct{}

func (fakeAnalyzer) Enabled() bool { return true }

func (fakeAnalyzer) Queue(AIAnalysisRequest) {}

func newTestAnalyzer(*testing.T, database.Database, *logger.Logger) AIAnalyzer {
	return fakeAnalyzer{}
}
