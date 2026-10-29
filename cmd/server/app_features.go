// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "context"

// setupFeatures builds the optional services that sit on top of the workers:
// federation, AI analysis and the MCP server. The last two live in
// feature_ai*.go and feature_mcp*.go, one file per build tag (-tags noai,
// nomcp). A worker process serves no routes, so it builds no MCP server.
func (a *app) setupFeatures() error {
	if err := a.setupFederation(); err != nil {
		return err
	}

	a.setupAI()

	if a.cfg.Server.ServesAPI() {
		a.setupMCP()
	}

	return nil
}

// setupFederation starts pull replication with peer instances
// (docs/operations/federation.md).
func (a *app) setupFederation() error {
	fed, err := setupFederation(context.Background(), a.cfg, a.db, a.store.blobs, a.log, a.workers.webhook, &a.workers)
	if err != nil {
		return a.fail("failed to start federation", err)
	}

	a.feat.fed = fed

	if fed.stop != nil {
		a.extraStops = append(a.extraStops, fed.stop)
	}

	return nil
}
