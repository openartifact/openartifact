// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build nomcp

package main

import (
	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/middleware"
)

// mcpFeature is what the MCP server adds to the features struct: nothing in a
// binary built with -tags nomcp. config.Validate has already refused
// mcp.enabled, so every hook below has nothing to do.
type mcpFeature struct{}

func (a *app) setupMCP() {}

func (a *app) addMCPMiddleware(*api.HTTPServer) {}

func (a *app) configureMCPAuth(*middleware.RBACConfig) {}

func (a *app) registerMCPMetadata(chi.Router) {}

func (a *app) mountMCP(chi.Router) {}
