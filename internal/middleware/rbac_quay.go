// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"strings"
)

// isQuayAPIPath reports whether urlPath belongs to the Quay-compatible API
// facade (internal/quayapi).
func isQuayAPIPath(urlPath string, cfg RBACConfig) bool {
	return cfg.QuayAPIPath != "" && (urlPath == cfg.QuayAPIPath || strings.HasPrefix(urlPath, cfg.QuayAPIPath+"/"))
}

// classifyRouteFor is classifyRoute with the facade's prefix recognized: every
// request to it needs the global "manage" permission (the same bar as the
// management API, which deploy tokens, and so robot accounts, never reach).
// Left to classifyRoute, its first path segment would be read as a repository.
func classifyRouteFor(r *http.Request, cfg RBACConfig) routeScope {
	if isQuayAPIPath(r.URL.Path, cfg) {
		return routeScope{action: "manage"}
	}

	return classifyRoute(r)
}
