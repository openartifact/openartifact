// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats_test

import (
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/formats"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
)

func TestMain(m *testing.M) {
	formats.Register()
	m.Run()
}

// TestRegistrationsMatchSpecs: every format of the repotype registry has a
// plugin and the plugin agrees with the Spec on what the Spec states.
func TestRegistrationsMatchSpecs(t *testing.T) {
	t.Parallel()

	var names []string
	for _, f := range plugin.All() {
		names = append(names, f.Name())
	}

	if want := repotype.Names(); !slices.Equal(names, want) {
		t.Fatalf("registered plugins %v, repotype.Names() %v", names, want)
	}

	for _, f := range plugin.All() {
		spec, _ := repotype.Lookup(f.Name())

		if got := f.Proxy().Enabled; got != spec.Proxy {
			t.Errorf("%s: plugin proxy = %v, Spec.Proxy = %v", spec.Name, got, spec.Proxy)
		}

		if _, ok := proxy.Lookup(spec.Name); ok != spec.Proxy {
			t.Errorf("%s: proxy hooks registered = %v, Spec.Proxy = %v", spec.Name, ok, spec.Proxy)
		}

		if got := f.Group().Enabled; got != spec.Group {
			t.Errorf("%s: plugin group = %v, Spec.Group = %v", spec.Name, got, spec.Group)
		}

		if _, ok := group.Lookup(spec.Name); ok != (f.Group().Hooks != nil) {
			t.Errorf("%s: group hooks registered = %v, plugin has hooks = %v", spec.Name, ok, f.Group().Hooks != nil)
		}

		if _, ok := lifecycle.Lookup(spec.Name); !ok {
			t.Errorf("%s: lifecycle not registered", spec.Name)
		}

		if len(clientsetup.Templates(spec.Name, "hosted")) == 0 {
			t.Errorf("%s: no client setup snippets", spec.Name)
		}
	}
}

// TestHostedHandlersAgreeWithMounts: what a Spec says about where a format is
// mounted is backed by the handler the plugin builds.
func TestHostedHandlersAgreeWithMounts(t *testing.T) {
	t.Parallel()

	hosted, err := plugin.BuildHosted(&plugin.Deps{})
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range plugin.All() {
		spec, _ := repotype.Lookup(f.Name())
		h := hosted[spec.Name]

		if _, guarded := h.(plugin.Guarded); spec.Mount == repotype.MountGuarded && !guarded {
			t.Errorf("%s: mounted through a guard but its handler has none", spec.Name)
		}

		routes := chi.NewRouter()
		h.RegisterRepoRoutes(routes)

		if len(routes.Routes()) == 0 {
			t.Errorf("%s: the handler registers no route", spec.Name)
		}
	}
}
