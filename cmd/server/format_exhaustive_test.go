// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestHostedRoutersCoverEveryFormat: the dispatcher serves hosted repositories
// and group members through hostedRouters, so a format without an entry would
// be unreachable behind a group.
func TestHostedRoutersCoverEveryFormat(t *testing.T) {
	t.Parallel()

	routers := routeTestApp(t).hostedRouters()

	for _, name := range repotype.Names() {
		if routers[name] == nil {
			t.Errorf("cmd/server.hostedRouters: format %q has no hosted router", name)
		}
	}

	for name := range routers {
		if !repotype.Valid(name) {
			t.Errorf("cmd/server.hostedRouters: %q is not a registered format", name)
		}
	}
}

// TestMountedRoutesPerFormat asserts every format is mounted either on its own
// /repos/<format>/{name} prefix (isolated), on the shared /repos/{name}
// subrouter, or on the OCI /v2 registry, and that this agrees with the registry.
func TestMountedRoutesPerFormat(t *testing.T) {
	t.Parallel()

	isolated := []string{"maven", "raw", "ivy", "sbt"}
	shared := []string{"rpm", "deb", "helm", "pypi", "nuget", "npm", "cargo", "rubygems", "conda", "pacman", "conan", "go", "terraform"}
	oci := []string{"docker"}
	apk := []string{"apk"}

	mountedIsolated := repotypetest.Classified(t, "cmd/server.mountIsolatedRepoRoutes",
		append(append([]string{}, isolated...), oci...), append(append([]string{}, shared...), apk...))
	repotypetest.Classified(t, "cmd/server.mountRepoRoutes", shared, append(append(append([]string{}, isolated...), oci...), apk...))

	// apk: served by hostedRouters and its API routes, but its repo routes are
	// reached through the dispatcher only, so no mount here.
	_ = mountedIsolated

	for _, spec := range repotypetest.Specs() {
		want := false
		for _, n := range isolated {
			want = want || n == spec.Name
		}

		if spec.Isolated != want {
			t.Errorf("cmd/server mount: format %q Isolated=%v in the registry but mounted isolated=%v", spec.Name, spec.Isolated, want)
		}
	}

	var patterns []string

	err := chi.Walk(uploadRouteTable(t), func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		patterns = append(patterns, route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	has := func(prefix string) bool {
		for _, p := range patterns {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		}

		return false
	}

	for _, name := range isolated {
		if !has("/repos/" + name + "/{name}/") {
			t.Errorf("format %q: no route under /repos/%s/{name}/", name, name)
		}
	}

	if !has("/repos/{name}/") {
		t.Error("no routes under the shared /repos/{name}/ subrouter")
	}

	if !has("/v2/") {
		t.Error("no routes under /v2/ for docker")
	}
}

// TestNotifyRepoFormats classifies notifyRepo only: rpm and maven need a
// database and the others need running workers, which the tests do not build.
func TestNotifyRepoFormats(t *testing.T) {
	t.Parallel()

	repotypetest.Classified(t, "cmd/server.notifyRepo",
		[]string{"rpm", "deb", "pacman", "apk", "maven"},
		[]string{
			"docker", "npm", "pypi", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan", "ivy", "sbt", "terraform",
		})
}
