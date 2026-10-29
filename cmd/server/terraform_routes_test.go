// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/repotype"
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

// sharedShapes returns the route shapes of every format served on the shared
// /repos/{name} subrouter: the routes of its handler and, for the formats that
// publish through it, their write routes.
func sharedShapes(t *testing.T, a *app) map[string]map[string]bool {
	t.Helper()

	shapes := map[string]map[string]bool{}

	for name, h := range a.web.hosted {
		if repotype.MountOf(name) != repotype.MountShared {
			continue
		}

		shapes[name] = routeShapes(t, func(r chi.Router) {
			h.RegisterRepoRoutes(r)

			if w, ok := h.(plugin.WriteRouted); ok {
				w.RegisterWriteRoutes(r)
			}
		})
	}

	return shapes
}

// TestSharedRouteClashesAreKnown: formats share the /repos/{name} subrouter
// and chi keeps one handler per node and method, so two formats registering a
// route of the same shape make the later one win for every repository whatever
// its format. The two clashes there are have always been resolved by registration
// order (plugin.All); a new one fails here: give the format a guard or a prefix
// of its own (Spec.Mount) instead.
func TestSharedRouteClashesAreKnown(t *testing.T) {
	shapes := sharedShapes(t, routeTestApp(t))

	if len(shapes["terraform"]) == 0 {
		t.Fatal("terraform registers no routes")
	}

	known := map[string]string{
		"GET /{}/{}":      "conda over npm",
		"GET /public.key": "deb over rpm",
	}

	owner := map[string]string{}
	got := map[string]string{}

	for _, f := range plugin.All() {
		name := f.Name()

		for shape := range shapes[name] {
			if first, dup := owner[shape]; dup {
				got[shape] = name + " over " + first
			}

			owner[shape] = name
		}
	}

	for shape, what := range got {
		if known[shape] != what {
			t.Errorf("route %q: %s, known clashes %v", shape, what, known)
		}
	}

	for shape, what := range known {
		if got[shape] != what {
			t.Errorf("known clash %q (%s) is gone or changed to %q: update the table", shape, what, got[shape])
		}
	}
}
