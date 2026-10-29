// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/repodispatch"
	"github.com/openartifact/openartifact/internal/repotype"
)

// TestMountedRoutesPerFormat asserts every format is mounted where its Spec
// says: on its own /repos/<format>/{name} prefix (isolated), on the shared
// /repos/{name} subrouter, on the dispatcher (apk) or on the OCI /v2 registry,
// and that every format has a hosted router behind groups.
func TestMountedRoutesPerFormat(t *testing.T) {
	t.Parallel()

	a := routeTestApp(t)

	routers := a.hostedRouters()
	for _, name := range repotype.Names() {
		if routers[name] == nil {
			t.Errorf("format %q has no hosted router", name)
		}
	}

	if err := a.mountFormats(repodispatch.New(a.log, a.db, nil, nil, routers)); err != nil {
		t.Fatal(err)
	}

	var patterns []string

	err := chi.Walk(a.web.authed, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
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

	for _, name := range repotype.Names() {
		switch repotype.MountOf(name) {
		case repotype.MountIsolated:
			if !has("/repos/" + name + "/{name}/") {
				t.Errorf("format %q: no route under /repos/%s/{name}/", name, name)
			}
		case repotype.MountOCI:
			if !has("/v2/") {
				t.Errorf("format %q: no route under /v2/", name)
			}
		case repotype.MountShared, repotype.MountGuarded, repotype.MountDispatcher:
		}
	}

	if !has("/repos/{name}/") {
		t.Error("no routes under the shared /repos/{name}/ subrouter")
	}
}

// TestApkIsServedByTheDispatcher pins why apk has no routes on the shared
// /repos/{name} subrouter: its files are free-form <branch>/<repo>/<arch>/<file>
// paths plus /public.key, and the shared GET, PUT and DELETE catch-alls (GOPROXY,
// npm publish and unpublish) and deb's /public.key are already taken, so a hosted
// apk repository is handed to the apk router by repository format
// (repodispatch.Middleware). The URLs are the ones every other format has.
func TestApkIsServedByTheDispatcher(t *testing.T) {
	t.Parallel()

	a := routeTestApp(t)
	if err := a.mountFormats(repodispatch.New(a.log, a.db, nil, nil, a.hostedRouters())); err != nil {
		t.Fatal(err)
	}

	routes := func(h http.Handler) map[string]bool {
		t.Helper()

		got := map[string]bool{}

		err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			got[method+" "+route] = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	apk := routes(a.hostedRouters()["apk"])
	for _, want := range []string{"GET /*", "HEAD /*", "PUT /*", "DELETE /*"} {
		if !apk[want] {
			t.Errorf("the hosted apk router lacks %s", want)
		}
	}

	shared := routes(a.web.authed)
	for _, taken := range []string{"GET /repos/{name}/*", "PUT /repos/{name}/*", "DELETE /repos/{name}/*", "GET /repos/{name}/public.key"} {
		if !shared[taken] {
			t.Errorf("%s is no longer on the shared subrouter: apk could be mounted there, then drop its dispatcher exception", taken)
		}
	}
}
