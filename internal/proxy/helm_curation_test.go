// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
)

// helmCurationIndex lists chart "app" in 1.0.0 and 2.0.0 (the latter created a
// day ago) and chart "other" in 1.0.0, each with its own file.
func helmCurationIndex(g *fakeChartRepo) {
	created := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	g.index = `apiVersion: v1
entries:
  app:
  - {name: app, version: 2.0.0, created: "` + created + `", digest: ` + sha256Hex([]byte("app2")) + `, urls: [app-2.0.0.tgz]}
  - {name: app, version: 1.0.0, digest: ` + g.digestOf("app-1.0.0.tgz") + `, urls: [app-1.0.0.tgz]}
  other:
  - {name: other, version: 1.0.0, digest: ` + sha256Hex([]byte("other")) + `, urls: [other-1.0.0.tgz]}
`
	g.files["app-2.0.0.tgz"] = []byte("app2")
	g.files["other-1.0.0.tgz"] = []byte("other")
}

func helmListed(t *testing.T, f *fixture) []string {
	t.Helper()

	w := f.do(http.MethodGet, "index.yaml")
	if w.Code != http.StatusOK {
		t.Fatalf("index = %d %s", w.Code, w.Body.String())
	}

	var out []string
	for key := range helmVersions(t, w.Body.Bytes()) {
		out = append(out, key)
	}

	slices.Sort(out)

	return out
}

func TestHelmCuration_IndexAndChartsFollowTheRules(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		rules  []curation.Rule
		listed []string
		files  map[string]int
	}{
		{
			name:   "denylist hides the version and refuses its file",
			mode:   curation.ModeDenylist,
			rules:  []curation.Rule{blockRule("app", "2.0.0")},
			listed: []string{"app@1.0.0", "other@1.0.0"},
			files:  map[string]int{"app-2.0.0.tgz": http.StatusForbidden, "app-1.0.0.tgz": http.StatusOK},
		},
		{
			name:   "chart glob",
			mode:   curation.ModeDenylist,
			rules:  []curation.Rule{blockRule("ap?", "")},
			listed: []string{"other@1.0.0"},
			files:  map[string]int{"app-1.0.0.tgz": http.StatusForbidden, "other-1.0.0.tgz": http.StatusOK},
		},
		{
			name:   "allowlist leaves the other charts out",
			mode:   curation.ModeAllowlist,
			rules:  []curation.Rule{allowRule("app", "1.*")},
			listed: []string{"app@1.0.0"},
			files: map[string]int{
				"other-1.0.0.tgz": http.StatusForbidden,
				"app-2.0.0.tgz":   http.StatusForbidden,
				"app-1.0.0.tgz":   http.StatusOK,
			},
		},
		{
			name:   "block beats allow",
			mode:   curation.ModeAllowlist,
			rules:  []curation.Rule{allowRule("app", ""), blockRule("app", "1.0.0")},
			listed: []string{"app@2.0.0"},
			files:  map[string]int{"app-1.0.0.tgz": http.StatusForbidden, "app-2.0.0.tgz": http.StatusOK},
		},
		{
			name:   "mode off leaves the rules inert",
			mode:   curation.ModeOff,
			rules:  []curation.Rule{blockRule("app", "")},
			listed: []string{"app@1.0.0", "app@2.0.0", "other@1.0.0"},
			files:  map[string]int{"app-2.0.0.tgz": http.StatusOK},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, g := helmFixture(t)
			helmCurationIndex(g)
			f.useCuration(tt.mode, tt.rules...)

			if got := helmListed(t, f); !slices.Equal(got, tt.listed) {
				t.Errorf("listed = %v, want %v", got, tt.listed)
			}

			for file, want := range tt.files {
				if w := f.do(http.MethodGet, "charts/"+file); w.Code != want {
					t.Errorf("%s = %d %q, want %d", file, w.Code, w.Body.String(), want)
				}
			}
		})
	}
}

func TestHelmCuration_RefusedChartNamesTheReasonAndIsNeverFetched(t *testing.T) {
	f, g := helmFixture(t)
	helmCurationIndex(g)
	f.useCuration(
		curation.ModeDenylist,
		curation.Rule{Action: curation.ActionBlock, Package: "app", Version: "2.0.0", Reason: "CVE-2099-4"},
	)

	w := f.do(http.MethodGet, "charts/app-2.0.0.tgz")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "CVE-2099-4") {
		t.Errorf("chart = %d %q, want 403 naming the reason", w.Code, w.Body.String())
	}

	if g.saw("/charts/app-2.0.0.tgz") {
		t.Error("a curated chart was fetched from the upstream")
	}
}

func TestHelmCuration_AllowExemptsAYoungChartFromTheMinimumAge(t *testing.T) {
	f, g := helmFixture(t)
	helmCurationIndex(g)
	f.repo.MinAgeHours = 24
	src := f.useCuration(curation.ModeDenylist, blockRule("other", ""))

	if got := helmListed(t, f); !slices.Equal(got, []string{"app@1.0.0"}) {
		t.Fatalf("without an allow rule: %v, want app 1.0.0 only (2.0.0 too new)", got)
	}

	src.set(allowRule("app", "2.0.0"))

	if got := helmListed(t, f); !slices.Equal(got, []string{"app@1.0.0", "app@2.0.0", "other@1.0.0"}) {
		t.Errorf("with an allow rule: %v", got)
	}

	if w := f.do(http.MethodGet, "charts/app-2.0.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("exempt chart = %d, want 200", w.Code)
	}
}

func TestHelmCuration_ValidatorFollowsRulesAndMode(t *testing.T) {
	f, g := helmFixture(t)
	helmCurationIndex(g)
	etag := func() string { return f.do(http.MethodGet, "index.yaml").Header().Get("ETag") }

	plain := etag()
	src := f.useCuration(curation.ModeDenylist, blockRule("app", "2.0.0"))
	first := etag()

	src.set(blockRule("app", "1.0.0"))
	second := etag()

	if plain == "" || plain == first || first == second {
		t.Errorf("validators %q %q %q; want three different ones", plain, first, second)
	}

	if w := f.doWith("index.yaml", map[string]string{"If-None-Match": second}); w.Code != http.StatusNotModified {
		t.Errorf("revalidating with the current validator = %d, want 304", w.Code)
	}

	f.repo.CurationMode = curation.ModeOff

	if off := etag(); off != plain {
		t.Errorf("curation off changed the validator: %q, want %q", off, plain)
	}
}

func TestHelmCuration_CachedChartIsRefusedOnceItsVersionIsCurated(t *testing.T) {
	f, g := helmFixture(t)
	helmCurationIndex(g)

	if w := f.do(http.MethodGet, "charts/app-2.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("chart with curation off = %d", w.Code)
	}

	f.useCuration(curation.ModeDenylist, blockRule("app", "2.0.0"))

	if w := f.do(http.MethodGet, "charts/app-2.0.0.tgz"); w.Code != http.StatusForbidden {
		t.Errorf("cached chart of a curated version = %d, want 403", w.Code)
	}

	if w := f.do(http.MethodGet, "charts/app-2.0.0.tgz.prov"); w.Code == http.StatusOK {
		t.Errorf("the provenance file of a curated version was served")
	}
}

func TestHelmPublished_DatesAVersionFromTheCachedIndex(t *testing.T) {
	f, g := helmFixture(t)
	helmCurationIndex(g)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "app", "2.0.0"); ok {
		t.Fatal("an index that is not cached dated a version")
	}

	_ = helmListed(t, f)

	got, ok := f.svc.PublishTime(ctx, f.repo, "app", "2.0.0")
	if !ok || time.Since(got) < 59*time.Minute || time.Since(got) > 61*time.Minute {
		t.Errorf("app 2.0.0 published = %v, %v; want about an hour ago", got, ok)
	}

	for _, v := range []string{"1.0.0", "9.9.9"} {
		if got, ok := f.svc.PublishTime(ctx, f.repo, "app", v); ok {
			t.Errorf("app %s dated %v; want no date for an undated or unknown version", v, got)
		}
	}
}
