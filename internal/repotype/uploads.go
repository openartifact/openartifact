// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repotype

import "net/http"

// The write routes of each format, classified for openartifact_uploads_total
// (Spec.Uploads). The patterns are what chi.Context.RoutePattern reports for the
// route as the server mounts it; internal/middleware reads them, and its route
// test fails for a write route that is missing here.

// Conan route patterns, shared by the v1 and v2 lists below.
const (
	conanV1Base              = "/repos/{name}/v1/conans/{pkg}/{version}/{user}/{channel}"
	conanV1UploadURLs        = conanV1Base + "/upload_urls"
	conanV1PackageUploadURLs = conanV1Base + "/packages/{pid}/upload_urls"
	conanV1PackagesDelete    = conanV1Base + "/packages/delete"
	conanV1RemoveFiles       = conanV1Base + "/remove_files"
	conanV1Export            = "/repos/{name}/v1/files/{pkg}/{version}/{user}/{channel}/{rrev}/export/*"
	conanV1Package           = "/repos/{name}/v1/files/{pkg}/{version}/{user}/{channel}/{rrev}/package/{pid}/{prev}/*"
	conanV2Recipe            = "/repos/{name}/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rrev}/files/*"
	conanV2Package           = "/repos/{name}/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rrev}/packages/{pid}" +
		"/revisions/{prev}/files/*"
)

// sharedPut is PUT /repos/{name}/*: npm publish (api.NPMUploadHandler) and the
// GOPROXY upload (counted by the server's wrapper) share the route, so neither
// can be counted from the route alone.
const sharedPut = "/repos/{name}/*"

func counted(pattern, method string) UploadRoute {
	return UploadRoute{Pattern: pattern, Count: CountedByMiddleware, Method: method}
}

func byHandler(pattern string) UploadRoute {
	return UploadRoute{Pattern: pattern, Count: CountedByHandler}
}

func notUpload(pattern string) UploadRoute { return UploadRoute{Pattern: pattern, Count: NotAnUpload} }

//nolint:gochecknoglobals // immutable route tables, read by the registry below
var (
	ansibleUploads = []UploadRoute{
		counted("/repos/{name}/api/v3/artifacts/collections", http.MethodPost),
	}
	apkUploads = []UploadRoute{
		byHandler("/api/v1/repositories/{name}/apks"),
	}
	cargoUploads = []UploadRoute{
		counted("/repos/{name}/api/v1/crates/new", http.MethodPut),
		notUpload("/repos/{name}/api/v1/crates/{crate}/{version}/unyank"),
	}
	conanUploads = []UploadRoute{
		counted(conanV1Export, http.MethodPut),
		counted(conanV1Package, http.MethodPut),
		counted(conanV2Recipe, http.MethodPut),
		counted(conanV2Package, http.MethodPut),
		notUpload(conanV1UploadURLs),
		notUpload(conanV1PackageUploadURLs),
		notUpload(conanV1PackagesDelete),
		notUpload(conanV1RemoveFiles),
	}
	condaUploads = []UploadRoute{
		counted("/repos/{name}/{platform}", http.MethodPost),
	}
	debUploads = []UploadRoute{
		byHandler("/api/v1/repositories/{name}/debs"),
	}
	dockerUploads = []UploadRoute{
		counted("/v2/*", ""),
	}
	goUploads = []UploadRoute{
		byHandler(sharedPut),
	}
	helmUploads = []UploadRoute{
		counted("/repos/{name}/charts", http.MethodPost),
	}
	ivyUploads = []UploadRoute{
		counted("/repos/ivy/{name}/*", http.MethodPut),
	}
	mavenUploads = []UploadRoute{
		byHandler("/repos/maven/{name}/*"),
	}
	npmUploads = []UploadRoute{
		byHandler(sharedPut),
		notUpload("/repos/{name}/-/package/{pkg}/dist-tags/{tag}"),
	}
	nugetUploads = []UploadRoute{
		counted("/repos/{name}/v3/package", http.MethodPut),
	}
	pacmanUploads = []UploadRoute{
		counted("/repos/{name}/os/{arch}/{filename}", http.MethodPut),
	}
	pypiUploads = []UploadRoute{
		counted("/repos/{name}/legacy", http.MethodPost),
	}
	rawUploads = []UploadRoute{
		counted("/repos/raw/{name}/*", http.MethodPut),
		// The presigned upload API of raw repositories: the completion counts
		// the upload, the rest is negotiation.
		byHandler("/api/v1/repositories/{name}/uploads/{id}/complete"),
		notUpload("/api/v1/repositories/{name}/uploads"),
		notUpload("/api/v1/repositories/{name}/uploads/{id}/parts"),
		notUpload("/api/v1/repositories/{name}/uploads/{id}/abort"),
	}
	rpmUploads = []UploadRoute{
		byHandler("/api/v1/repositories/{name}/packages"),
		notUpload("/api/v1/repositories/{name}/comps"),
	}
	rubygemsUploads = []UploadRoute{
		counted("/repos/{name}/api/v1/gems", http.MethodPost),
	}
	sbtUploads = []UploadRoute{
		counted("/repos/sbt/{name}/*", http.MethodPut),
	}
	terraformUploads = []UploadRoute{
		counted("/repos/{name}/_api/modules/{mod}/{system}/{version}", http.MethodPut),
		counted("/repos/{name}/_api/providers/{type}/{version}/{os}/{arch}", http.MethodPut),
	}
)

// UploadRoutes returns the classification of every write route of every
// format, keyed by chi route pattern, with Format set to the owning format. A
// pattern that two formats share (the shared PUT) has one class.
func UploadRoutes() map[string]UploadRoute {
	routes := make(map[string]UploadRoute)

	for i := range specs {
		for _, r := range specs[i].Uploads {
			r.Format = specs[i].Name
			routes[r.Pattern] = r
		}
	}

	return routes
}
