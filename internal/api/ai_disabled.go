// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build noai

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// errAIUnavailable is the message of every AI route in a binary built with
// -tags noai.
const errAIUnavailable = "AI analysis is not compiled into this build (built with -tags noai)"

// RegisterAIUnavailable registers the AI analysis routes (per repository under
// repoPrefix and the administrative configuration ones) of a binary built with
// -tags noai. They stay routed so clients get an explicit 501
// feature_unavailable instead of a 404 or the SPA page.
func RegisterAIUnavailable(r chi.Router, repoPrefix string) {
	r.Route(repoPrefix, func(r chi.Router) {
		r.Get("/ai-analysis", aiUnavailable)
		r.Get("/ai-analysis/{artifactID}", aiUnavailable)
		r.Post("/ai-analysis/{artifactID}/trigger", aiUnavailable)
	})

	r.Get("/api/v1/admin/ai/config", aiUnavailable)
	r.Put("/api/v1/admin/ai/config", aiUnavailable)
	r.Post("/api/v1/admin/ai/test", aiUnavailable)
	r.Get("/api/v1/admin/ai/models", aiUnavailable)
}

func aiUnavailable(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "feature_unavailable", errAIUnavailable)
}
