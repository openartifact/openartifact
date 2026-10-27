// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestTerraformRoutesAreNotShadowed resolves the registry and publishing
// requests of a Terraform client against the real /repos/{name} subrouter. All
// formats share it, and chi keeps one handler per node and method, so a
// same-shaped route of another format registered later would silently take a
// terraform URL over (conda's /{platform}/{filename} does for the 2-segment
// one, which LatestGuard exists for).
func TestTerraformRoutesAreNotShadowed(t *testing.T) {
	router, ok := uploadRouteTable(t).(*chi.Mux)
	if !ok {
		t.Fatal("the route table is not a chi.Mux")
	}

	const repo = "/repos/{name}"

	for _, tc := range []struct {
		method, path, want string
	}{
		{http.MethodPut, "/repos/r/_api/modules/vpc/aws/1.0.0", repo + "/_api/modules/{mod}/{system}/{version}"},
		{http.MethodPut, "/repos/r/_api/providers/aws/1.0.0/linux/amd64", repo + "/_api/providers/{type}/{version}/{os}/{arch}"},
		{http.MethodDelete, "/repos/r/_api/modules/vpc/aws/1.0.0", repo + "/_api/modules/{mod}/{system}/{version}"},
		{http.MethodDelete, "/repos/r/_api/providers/aws/1.0.0/linux/amd64", repo + "/_api/providers/{type}/{version}/{os}/{arch}"},
		{http.MethodDelete, "/repos/r/_api/providers/aws/1.0.0", repo + "/_api/providers/{type}/{version}"},
		{http.MethodGet, "/repos/r/vpc/aws/versions", repo + "/{mod}/{system}/versions"},
		{http.MethodGet, "/repos/r/vpc/aws/download", repo + "/{mod}/{system}/download"},
		{http.MethodGet, "/repos/r/vpc/aws/1.0.0/download", repo + "/{mod}/{system}/{version}/download"},
		{http.MethodGet, "/repos/r/vpc/aws/1.0.0", repo + "/{mod}/{system}/{version}"},
		{http.MethodGet, "/repos/r/aws/versions", repo + "/{type}/versions"},
		{http.MethodGet, "/repos/r/aws/1.2.3/download/linux/amd64", repo + "/{type}/{version}/download/{os}/{arch}"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := router.Find(chi.NewRouteContext(), tc.method, tc.path); got != tc.want {
				t.Fatalf("routed to %q, want %q", got, tc.want)
			}
		})
	}
}

var routeParam = regexp.MustCompile(`\{[^}]*\}`)

// routeShapes lists the method and route of everything register adds to a
// fresh router, parameter names erased: two routes with the same shape are the
// same chi node, and the later registration wins.
func routeShapes(t *testing.T, register func(chi.Router)) map[string]bool {
	t.Helper()

	r := chi.NewRouter()
	register(r)

	shapes := map[string]bool{}

	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		shapes[method+" "+routeParam.ReplaceAllString(route, "{}")] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return shapes
}

// TestTerraformRoutesDoNotShadowOtherFormats: terraform registers after most
// formats on the shared subrouter, so a route of the same shape as one of
// theirs would replace it for every repository, whatever its format.
func TestTerraformRoutesDoNotShadowOtherFormats(t *testing.T) {
	a := routeTestApp(t)
	f := &a.web.formats

	terraform := routeShapes(t, f.terraform.RegisterRepoRoutes)
	if len(terraform) == 0 {
		t.Fatal("terraform registers no routes")
	}

	others := map[string]func(chi.Router){
		"rpm": f.rpm.RegisterRepoRoutes, "deb": f.deb.RegisterRepoRoutes, "helm": f.helm.RegisterRepoRoutes,
		"pypi": f.pypi.RegisterRepoRoutes, "nuget": f.nuget.RegisterRepoRoutes, "npm": f.npm.RegisterRepoRoutes,
		"cargo": f.cargo.RegisterRepoRoutes, "rubygems": f.rubygems.RegisterRepoRoutes, "conda": f.conda.RegisterRepoRoutes,
		"conan": f.conan.RegisterRepoRoutes, "go": f.gomod.RegisterRepoRoutes, "npm upload": a.web.uploads.npm.RegisterRepoRoutes,
	}

	for name, register := range others {
		for shape := range routeShapes(t, register) {
			if terraform[shape] {
				t.Errorf("%s registers %q, the shape of a terraform route", name, shape)
			}
		}
	}
}
