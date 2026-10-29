// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/repotype"
)

// uploadRoutes classifies every PUT/POST/PATCH route of the server by its chi
// route pattern (as chi.Context.RoutePattern reports it: no trailing slash). The
// table is the formats' repotype.Spec.Uploads, the single place that says which
// route is a hosted upload; the route test fails for a route that is missing
// there, so a new format cannot forget to count its uploads. The format label
// of a counted route is the Spec name, which is the metrics.Format* value.
var uploadRoutes = sync.OnceValue(repotype.UploadRoutes) //nolint:gochecknoglobals // computed once from the immutable registry

// uploadFormat reports the format label of a request that MetricsMiddleware
// counts as an upload. The decision depends only on the method and the route
// pattern (and, for the registry, the endpoint the path addresses).
func uploadFormat(r *http.Request, pattern string) (string, bool) {
	route, ok := uploadRoutes()[pattern]
	if !ok || route.Count != repotype.CountedByMiddleware {
		return "", false
	}

	if pattern == ociWildcard {
		return metrics.FormatDocker, isOCIUpload(r)
	}

	return route.Format, r.Method == route.Method
}

// isOCIUpload is true for the registry requests that finish pushing an object:
// a manifest PUT, the PUT that completes a chunked blob upload and the
// monolithic POST (?digest=) that uploads a blob in one request. The POST that
// starts an upload and the chunk PATCHes are not counted.
func isOCIUpload(r *http.Request) bool {
	req := ocipath.Parse(strings.TrimPrefix(r.URL.Path, "/v2/"))

	switch req.Kind {
	case ocipath.KindManifest:
		return r.Method == http.MethodPut
	case ocipath.KindUpload:
		return r.Method == http.MethodPut
	case ocipath.KindUploadStart:
		return r.Method == http.MethodPost && r.URL.Query().Get("digest") != ""
	case ocipath.KindBlob, ocipath.KindTags, ocipath.KindReferrers, ocipath.KindUnknown:
	}

	return false
}

// CountUploads wraps a handler that serves one hosted-upload format on a route
// it shares with other formats (so MetricsMiddleware cannot tell the format
// from the route), counting every request it serves in
// openartifact_uploads_total by response status.
func CountUploads(reg *metrics.Registry, format string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		defer func() { reg.CountUpload(format, metrics.UploadResult(rw.statusCode)) }()

		next(rw, r)
	}
}
