// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build noai

package main

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api"
)

// aiFeature is what AI analysis adds to the features struct: nothing in a
// binary built with -tags noai.
type aiFeature struct{}

// setupAI keeps the plain webhook dispatcher as the upload publisher; there is
// no AI worker to feed. config.Validate has already refused ai.enabled.
func (a *app) setupAI() {
	a.feat.uploadPublisher = a.workers.webhook
}

// registerAIRoutes answers the AI routes with 501 feature_unavailable.
func (a *app) registerAIRoutes(authedAPI chi.Router) {
	api.RegisterAIUnavailable(authedAPI, "/api/v1/repositories/{name}")
}

// aiEnabled is nil: AI analysis is never on in this build.
func (a *app) aiEnabled() func(context.Context) bool { return nil }
