// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/ocipath"
)

// unmatchedPath is the path label of requests that match no route (404, 405):
// a fixed value, never the raw path, so cardinality stays bounded.
const unmatchedPath = "unmatched"

// ociWildcard is the route pattern of the whole OCI registry: Distribution
// names contain "/", so a single wildcard route serves every /v2/<name>/...
// request.
const ociWildcard = "/v2/*"

// PathLabel is the value of the path label of the HTTP metrics: the route
// template that served the request, never the raw URL.
//
// pattern is the chi route pattern ("" when no route matched). Templates
// ("/repos/{name}/{pkg}", "/repos/maven/{name}/*") are bounded by the route
// table, so repository names, package names, versions and digests never reach a
// label; a wildcard keeps its "/*" so every catch-all is one fixed bucket. The
// one exception is the OCI registry, whose single "/v2/*" route is split into
// the registry endpoints (see ociPathLabel). A request that matched no route is
// "unmatched".
func PathLabel(pattern string, r *http.Request) string {
	switch pattern {
	case "":
		return unmatchedPath
	case ociWildcard:
		return ociPathLabel(r.URL.Path)
	}

	return pattern
}

// ociPathLabel names the registry endpoint of a /v2/<name>/... path with the
// distribution-spec templates, so manifests, blobs and uploads are told apart
// without the (unbounded) image name, tag, digest or upload id.
func ociPathLabel(urlPath string) string {
	switch ocipath.Parse(strings.TrimPrefix(urlPath, "/v2/")).Kind {
	case ocipath.KindManifest:
		return "/v2/{name}/manifests/{reference}"
	case ocipath.KindBlob:
		return "/v2/{name}/blobs/{digest}"
	case ocipath.KindUploadStart:
		return "/v2/{name}/blobs/uploads"
	case ocipath.KindUpload:
		return "/v2/{name}/blobs/uploads/{uuid}"
	case ocipath.KindTags:
		return "/v2/{name}/tags/list"
	case ocipath.KindReferrers:
		return "/v2/{name}/referrers/{digest}"
	case ocipath.KindUnknown:
	}

	return ociWildcard
}
