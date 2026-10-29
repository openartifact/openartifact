// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats_test

import (
	"context"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/formats"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/quarantine"
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

	slices.Sort(names)

	if want := repotype.Names(); !slices.Equal(names, want) {
		t.Fatalf("registered plugins %v, repotype.Names() %v", names, want)
	}

	for _, f := range plugin.All() {
		spec, _ := repotype.Lookup(f.Name())

		if got := f.Proxy().Enabled; got != spec.Proxy {
			t.Errorf("%s: plugin proxy = %v, Spec.Proxy = %v", spec.Name, got, spec.Proxy)
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

type repoCounter struct{ calls int }

func (c *repoCounter) NotifyStale(string) { c.calls++ }

type artifactCounter struct{ calls int }

func (c *artifactCounter) NotifyStale(_, _, _ string) { c.calls++ }

type collectCounter struct{ runs int }

func (c *collectCounter) CollectRemoved(context.Context, *models.Repository) error {
	c.runs++

	return nil
}

// TestRemovalRefreshesGeneratedMetadata: a cleanup run that removed rows
// refreshes the generated metadata of the formats that have some, and does
// nothing for the formats that compute their indexes live.
func TestRemovalRefreshesGeneratedMetadata(t *testing.T) {
	t.Parallel()

	// Formats whose Removed hook acts on a worker, with the worker they use.
	acting := map[string]bool{"apk": true, "deb": true, "maven": true, "pacman": true, "rpm": true, "docker": true}

	for _, f := range plugin.All() {
		t.Run(f.Name(), func(t *testing.T) {
			t.Parallel()

			var (
				repo   = &repoCounter{}
				art    = &artifactCounter{}
				gc     = &collectCounter{}
				marked []string
				ws     = lifecycle.NewWorkers()
			)

			// Every worker of a format gets the counter of its kind.
			for _, name := range repotype.Names() {
				switch name {
				case "maven":
					ws.Bind(name, art)
				case "docker":
					ws.Bind(name, gc)
				default:
					ws.Bind(name, repo)
				}
			}

			env := &lifecycle.Env{
				Workers:       ws,
				MarkRepoStale: func(_ context.Context, id string) error { marked = append(marked, id); return nil },
			}

			hooks, ok := lifecycle.Lookup(f.Name())
			if !ok {
				t.Fatal("no lifecycle registered")
			}

			if hooks.Removed != nil {
				hooks.Removed(t.Context(), env, lifecycle.Removal{
					Repo:  &models.Repository{ID: "r", Name: "n", Format: f.Name()},
					Names: []string{"org:lib"},
					Hard:  1,
				})
			}

			acted := repo.calls+art.calls+gc.runs > 0
			if acted != acting[f.Name()] {
				t.Errorf("Removed acted = %v, want %v", acted, acting[f.Name()])
			}

			if got := len(marked) > 0; got != (f.Name() == "rpm") {
				t.Errorf("marked the repository stale = %v, want only for rpm", got)
			}

			spec, _ := repotype.Lookup(f.Name())
			if spec.GeneratedGroupMetadata && !acted {
				t.Errorf("%s has generated group metadata but its cleanup refreshes nothing", f.Name())
			}
		})
	}
}

// TestKeyChangeResignsStoredSignatures: a GPG key change must re-sign the
// metadata of every format that stores a signature. terraform stores none: it
// signs SHA256SUMS and publishes the signing key on every request, so it
// follows a rotation without a refresh (internal/format/terraform
// TestProviderSigningFollowsKeyRotation).
func TestKeyChangeResignsStoredSignatures(t *testing.T) {
	t.Parallel()

	servedSigned := map[string]bool{"terraform": true}

	for _, name := range repotype.Names() {
		if !repotype.GPGSigned(name) {
			continue
		}

		hooks, _ := lifecycle.Lookup(name)
		if refreshes := hooks.Stale != nil; refreshes == servedSigned[name] {
			t.Errorf("%s: GPG-signed, Stale hook = %v, signs when served = %v", name, refreshes, servedSigned[name])
		}
	}
}

// dedicatedTables are the formats that keep dedicated tables instead of the
// generic package tables, and so have no storage descriptor: the generic engines
// handle them by name.
var dedicatedTables = []string{"docker"} //nolint:gochecknoglobals // read-only test table

// TestStorageDescriptorsMatchSpecs: every format but the dedicated-table ones
// registers a storage descriptor with a trash kind, and what the descriptor says
// agrees with what repotype says about the format.
func TestStorageDescriptorsMatchSpecs(t *testing.T) {
	t.Parallel()

	kinds := map[string]string{}

	for _, name := range repotype.Names() {
		spec, _ := repotype.Lookup(name)
		st, registered := storage.Lookup(spec.Name)

		if slices.Contains(dedicatedTables, spec.Name) {
			if registered {
				t.Errorf("%s keeps dedicated tables and must have no storage descriptor", spec.Name)
			}

			continue
		}

		if !registered {
			t.Errorf("%s: no storage descriptor registered", spec.Name)

			continue
		}

		if st.Trash.Kind == "" {
			t.Errorf("%s: the storage descriptor has no trash kind", spec.Name)
		}

		if first, dup := kinds[st.Trash.Kind]; dup && first != spec.Name {
			t.Errorf("%s and %s share the trash kind %q", first, spec.Name, st.Trash.Kind)
		}

		kinds[st.Trash.Kind] = spec.Name

		if federated := st.Federation != nil; federated != spec.Federation {
			t.Errorf("%s: descriptor federated = %v, Spec.Federation = %v", spec.Name, federated, spec.Federation)
		}

		if st.Detail.Decoder != "" && st.Detail.Deps == (storage.Field{}) {
			t.Errorf("%s: the detail names a dependency decoder but no source", spec.Name)
		}
	}

	for _, name := range dedicatedTables {
		if _, ok := repotype.Lookup(name); !ok {
			t.Errorf("dedicatedTables names %q, which is not a format", name)
		}
	}
}

// TestFederationQuarantineKeys: the spellings a scan of a replicated unit may
// have been recorded under are the ones the format's download handler checks.
func TestFederationQuarantineKeys(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		format, key string
		want        []string
	}{
		{"pypi", "demo/1.0/demo-1.0.tar.gz", quarantine.PathKeys("demo/1.0/demo-1.0.tar.gz", "demo-1.0.tar.gz")},
		{"helm", "nginx@1.2.3", quarantine.PathKeys("nginx@1.2.3", "nginx-1.2.3.tgz", "charts/nginx-1.2.3.tgz")},
		{"cargo", "serde@1.0.0", quarantine.PathKeys("serde@1.0.0", "serde-1.0.0.crate", "serde/1.0.0")},
		{"go", "example.com/m@v1.0.0", quarantine.PathKeys("example.com/m@v1.0.0",
			"example.com/m/@v/v1.0.0.zip", "example.com/m/@v/v1.0.0.mod", "example.com%2Fm@v1.0.0")},
		{"nuget", "Newtonsoft.Json@13.0.1", quarantine.PathKeys("Newtonsoft.Json@13.0.1", "newtonsoft.json.13.0.1.nupkg")},
	} {
		st, _ := storage.Lookup(tc.format)

		if got := st.Federation.QuarantineKeys(tc.key); !slices.Equal(got, tc.want) {
			t.Errorf("%s %q: keys %v, want %v", tc.format, tc.key, got, tc.want)
		}
	}
}
