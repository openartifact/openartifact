// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

// UploadRouteClass reports whether the middleware counts the route's uploads and
// whether the route is in the table at all. It exposes the upload route table to the external tests, which
// import the format packages (they import this package, so the test cannot
// live inside it).
func UploadRouteClass(pattern string) (bool, bool) {
	r, ok := uploadRoutes[pattern]

	return r.count == countedByMiddleware, ok
}

// UploadRoutePatterns lists the patterns of the upload route table.
func UploadRoutePatterns() []string {
	out := make([]string, 0, len(uploadRoutes))
	for p := range uploadRoutes {
		out = append(out, p)
	}

	return out
}
