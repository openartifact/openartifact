// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !noai

package api

import "github.com/go-chi/chi/v5"

// registerAIRoutes registers the AI analysis routes the way the build serves
// them, for the OpenAPI route test.
func registerAIRoutes(r chi.Router, repoPrefix string) {
	NewAdminAIHandler(nil, nil, nil).Register(r)
	NewAIHandler(nil, nil, nil, nil).RegisterRoutes(r, repoPrefix)
}
