// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// How an upload route is counted in openartifact_uploads_total.
type uploadCount int

const (
	// countedByMiddleware: MetricsMiddleware counts it, from the HTTP status.
	countedByMiddleware uploadCount = iota
	// countedByHandler: the handler counts it itself (metrics.Registry.CountUpload),
	// because the route is shared by several formats or the outcome is not the
	// HTTP status (the presigned completion).
	countedByHandler
	// notAnUpload: a write route that publishes no package (yank, delete
	// batches, upload negotiation, metadata).
	notAnUpload
)

// uploadRoute classifies one write route.
type uploadRoute struct {
	count uploadCount
	// method is the HTTP method that uploads (countedByMiddleware only); other
	// methods on the same route are not counted.
	method string
	// format is the format label (countedByMiddleware only).
	format string
}

// uploadRoutes classifies every PUT/POST/PATCH route of the server by its chi
// route pattern (as chi.Context.RoutePattern reports it: no trailing slash). The table is the single place that says which route is a
// hosted upload and under which metrics.Format* it is counted;
// TestUploadRoutesClassified fails for a route that is missing here, so a new
// format cannot forget to count its uploads.
//
//nolint:gochecknoglobals // immutable route table
var uploadRoutes = map[string]uploadRoute{
	// Native client protocols, counted from the response status.
	"/repos/{name}/api/v1/crates/new":  {countedByMiddleware, http.MethodPut, metrics.FormatCargo},
	"/repos/{name}/api/v1/gems":        {countedByMiddleware, http.MethodPost, metrics.FormatRubyGems},
	"/repos/{name}/charts":             {countedByMiddleware, http.MethodPost, metrics.FormatHelm},
	"/repos/{name}/legacy":             {countedByMiddleware, http.MethodPost, metrics.FormatPyPI},
	"/repos/{name}/v3/package":         {countedByMiddleware, http.MethodPut, metrics.FormatNuGet},
	"/repos/{name}/{platform}":         {countedByMiddleware, http.MethodPost, metrics.FormatConda},
	"/repos/raw/{name}/*":              {countedByMiddleware, http.MethodPut, metrics.FormatRaw},
	"/repos/ivy/{name}/*":              {countedByMiddleware, http.MethodPut, metrics.FormatIvy},
	"/repos/sbt/{name}/*":              {countedByMiddleware, http.MethodPut, metrics.FormatSBT},
	ociWildcard:                        {countedByMiddleware, "", metrics.FormatDocker},
	conanV1Export:                      {countedByMiddleware, http.MethodPut, metrics.FormatConan},
	conanV1Package:                     {countedByMiddleware, http.MethodPut, metrics.FormatConan},
	conanV2Recipe:                      {countedByMiddleware, http.MethodPut, metrics.FormatConan},
	conanV2Package:                     {countedByMiddleware, http.MethodPut, metrics.FormatConan},
	"/repos/maven/{name}/*":            {count: countedByHandler}, // api.MavenUploadHandler
	"/repos/{name}/*":                  {count: countedByHandler}, // npm publish (api.NPMUploadHandler) and GOPROXY upload (CountUploads in cmd/server)
	"/api/v1/repositories/{name}/debs": {count: countedByHandler}, // api.DebUploadHandler

	"/api/v1/repositories/{name}/packages":                 {count: countedByHandler}, // api.RPMUploadHandler
	"/api/v1/repositories/{name}/uploads/{id}/complete":    {count: countedByHandler}, // api.PresignedUploadHandler (raw)
	"/api/v1/repositories/{name}/comps":                    {count: notAnUpload},
	"/api/v1/repositories/{name}/uploads":                  {count: notAnUpload}, // presigned upload negotiation
	"/api/v1/repositories/{name}/uploads/{id}/parts":       {count: notAnUpload},
	"/api/v1/repositories/{name}/uploads/{id}/abort":       {count: notAnUpload},
	"/repos/{name}/-/package/{pkg}/dist-tags/{tag}":        {count: notAnUpload},
	"/repos/{name}/api/v1/crates/{crate}/{version}/unyank": {count: notAnUpload},
	conanV1UploadURLs:        {count: notAnUpload},
	conanV1PackageUploadURLs: {count: notAnUpload},
	conanV1PackagesDelete:    {count: notAnUpload},
	conanV1RemoveFiles:       {count: notAnUpload},
}

// Conan route patterns, shared with the table above.
const (
	conanV1Base              = "/repos/{name}/v1/conans/{pkg}/{version}/{user}/{channel}"
	conanV1UploadURLs        = conanV1Base + "/upload_urls"
	conanV1PackageUploadURLs = conanV1Base + "/packages/{pid}/upload_urls"
	conanV1PackagesDelete    = conanV1Base + "/packages/delete"
	conanV1RemoveFiles       = conanV1Base + "/remove_files"
	conanV1Export            = "/repos/{name}/v1/files/{pkg}/{version}/{user}/{channel}/{rrev}/export/*"
	conanV1Package           = "/repos/{name}/v1/files/{pkg}/{version}/{user}/{channel}/{rrev}/package/{pid}/{prev}/*"
	conanV2Recipe            = "/repos/{name}/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rrev}/files/*"
	conanV2Package           = "/repos/{name}/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rrev}/packages/{pid}/revisions/{prev}/files/*"
)

// uploadFormat reports the format label of a request that MetricsMiddleware
// counts as an upload. The decision depends only on the method and the route
// pattern (and, for the registry, the endpoint the path addresses).
func uploadFormat(r *http.Request, pattern string) (string, bool) {
	route, ok := uploadRoutes[pattern]
	if !ok || route.count != countedByMiddleware {
		return "", false
	}

	if pattern == ociWildcard {
		return metrics.FormatDocker, isOCIUpload(r)
	}

	return route.format, r.Method == route.method
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
