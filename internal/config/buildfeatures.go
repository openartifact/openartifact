// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"

	"github.com/openartifact/openartifact/internal/buildtags"
)

var (
	errAINotCompiled = errors.New(
		"ai.enabled is true but this binary was built without AI analysis (-tags noai); " +
			"rebuild without that tag or set ai.enabled to false")
	errMCPNotCompiled = errors.New(
		"mcp.enabled is true but this binary was built without the MCP server (-tags nomcp); " +
			"rebuild without that tag or set mcp.enabled to false")
)

// validateAIAndMCP rejects a configuration that switches on a feature compiled
// out of this binary, so the operator learns it at startup instead of finding
// the feature silently absent, then checks the MCP settings.
func (c *Config) validateAIAndMCP() error {
	if err := checkBuildFeatures(c, buildtags.AI, buildtags.MCP); err != nil {
		return err
	}

	return c.validateMCP()
}

// checkBuildFeatures is validateBuildFeatures with the compiled-in features
// passed explicitly.
func checkBuildFeatures(c *Config, ai, mcp bool) error {
	if c.AI.Enabled && !ai {
		return errAINotCompiled
	}

	if c.MCP.Enabled && !mcp {
		return errMCPNotCompiled
	}

	return nil
}
