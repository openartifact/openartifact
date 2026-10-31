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
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

// composerServedVersions is the versions of the p2 document served for acme/lib.
func composerServedVersions(t *testing.T, f *fixture) []string {
	t.Helper()

	w := f.do(http.MethodGet, "p2/acme/lib.json")
	if w.Code == http.StatusNotFound {
		return nil
	}

	if w.Code != http.StatusOK {
		t.Fatalf("p2 = %d %s", w.Code, w.Body.String())
	}

	var out []string
	for _, v := range composerServed(t, w.Body.Bytes()) {
		out = append(out, v.VersionString())
	}

	slices.Sort(out)

	return out
}

// composerDist is the path of the dist the upstream published for version, as a
// client that kept the URL would ask for it.
func composerDist(p *fakePackagist, version string) string {
	d := pkgcomposer.Dist{URL: p.dists.URL + "/zip/" + version, Reference: "ref-" + version}

	return "dists/acme/lib/" + version + "/" + d.ProxyRef() + ".zip"
}

func TestComposerCuration_MetadataAndDistFollowTheRules(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		rules []curation.Rule
		want  []string
		dists map[string]int
	}{
		{
			name:  "denylist hides the version and refuses its dist",
			mode:  curation.ModeDenylist,
			rules: []curation.Rule{blockRule("acme/lib", "1.1.0")},
			want:  []string{"1.0.0"},
			dists: map[string]int{"1.1.0": http.StatusForbidden, "1.0.0": http.StatusOK},
		},
		{
			name:  "package glob over vendor/name",
			mode:  curation.ModeDenylist,
			rules: []curation.Rule{blockRule("acme/*", "1.0.*")},
			want:  []string{"1.1.0"},
			dists: map[string]int{"1.0.0": http.StatusForbidden, "1.1.0": http.StatusOK},
		},
		{
			name:  "allowlist of another package leaves nothing",
			mode:  curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("other/lib", "")},
			want:  nil,
			dists: map[string]int{"1.0.0": http.StatusForbidden, "1.1.0": http.StatusForbidden},
		},
		{
			name:  "allowlist of one version",
			mode:  curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("acme/lib", "1.0.0")},
			want:  []string{"1.0.0"},
			dists: map[string]int{"1.1.0": http.StatusForbidden, "1.0.0": http.StatusOK},
		},
		{
			name:  "block beats allow",
			mode:  curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("acme/lib", ""), blockRule("acme/lib", "1.1.0")},
			want:  []string{"1.0.0"},
			dists: map[string]int{"1.1.0": http.StatusForbidden},
		},
		{
			name:  "mode off leaves the rules inert",
			mode:  curation.ModeOff,
			rules: []curation.Rule{blockRule("acme/lib", "")},
			want:  []string{"1.0.0", "1.1.0"},
			dists: map[string]int{"1.1.0": http.StatusOK},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakePackagist(t)
			f := composerFixture(t, p)
			f.useCuration(tt.mode, tt.rules...)

			if got := composerServedVersions(t, f); !slices.Equal(got, tt.want) {
				t.Errorf("versions = %v, want %v", got, tt.want)
			}

			for version, want := range tt.dists {
				if w := f.do(http.MethodGet, composerDist(p, version)); w.Code != want {
					t.Errorf("dist %s = %d %q, want %d", version, w.Code, w.Body.String(), want)
				}
			}
		})
	}
}

func TestComposerCuration_RefusedDistNamesTheReasonAndIsNeverFetched(t *testing.T) {
	p := newFakePackagist(t)
	f := composerFixture(t, p)
	f.useCuration(
		curation.ModeDenylist,
		curation.Rule{Action: curation.ActionBlock, Package: "acme/lib", Version: "1.1.0", Reason: "CVE-2099-3"},
	)

	w := f.do(http.MethodGet, composerDist(p, "1.1.0"))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "CVE-2099-3") {
		t.Errorf("dist = %d %q, want 403 naming the reason", w.Code, w.Body.String())
	}

	if p.distHits.Load() != 0 {
		t.Error("a curated dist was fetched from its host")
	}
}

func TestComposerCuration_AllowExemptsAYoungVersionFromTheMinimumAge(t *testing.T) {
	f, p := composerAgedFixture(t)
	src := f.useCuration(curation.ModeDenylist, blockRule("other/lib", ""))

	if got := composerServedVersions(t, f); !slices.Equal(got, []string{"1.0.0"}) {
		t.Fatalf("without an allow rule: %v, want only the old version", got)
	}

	src.set(allowRule("acme/lib", "1.1.0"))

	if got := composerServedVersions(t, f); !slices.Equal(got, []string{"1.0.0", "1.1.0"}) {
		t.Errorf("with an allow rule: %v, want both", got)
	}

	if w := f.do(http.MethodGet, composerDist(p, "1.1.0")); w.Code != http.StatusOK {
		t.Errorf("exempt dist = %d, want 200", w.Code)
	}
}

func TestComposerCuration_ValidatorFollowsRulesAndMode(t *testing.T) {
	f := composerFixture(t, newFakePackagist(t))
	etag := func() string { return f.do(http.MethodGet, "p2/acme/lib.json").Header().Get("ETag") }

	plain := etag()
	src := f.useCuration(curation.ModeDenylist, blockRule("acme/lib", "1.1.0"))
	first := etag()

	src.set(blockRule("acme/lib", "1.0.0"))
	second := etag()

	if plain == "" || plain == first || first == second {
		t.Errorf("validators %q %q %q; want three different ones", plain, first, second)
	}

	if w := f.doWith("p2/acme/lib.json", map[string]string{"If-None-Match": second}); w.Code != http.StatusNotModified {
		t.Errorf("revalidating with the current validator = %d, want 304", w.Code)
	}

	f.repo.CurationMode = curation.ModeOff

	if off := etag(); off != plain {
		t.Errorf("curation off changed the validator: %q, want %q", off, plain)
	}
}

func TestComposerCuration_CachedDistIsRefusedOnceItsVersionIsCurated(t *testing.T) {
	p := newFakePackagist(t)
	f := composerFixture(t, p)

	if w := f.do(http.MethodGet, composerDist(p, "1.1.0")); w.Code != http.StatusOK {
		t.Fatalf("dist with curation off = %d", w.Code)
	}

	f.useCuration(curation.ModeDenylist, blockRule("acme/lib", "1.1.0"))

	if w := f.do(http.MethodGet, composerDist(p, "1.1.0")); w.Code != http.StatusForbidden {
		t.Errorf("cached dist of a curated version = %d, want 403", w.Code)
	}
}

func TestComposerPublished_DatesAVersionFromTheCachedDocument(t *testing.T) {
	f, _ := composerAgedFixture(t)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "acme/lib", "1.0.0"); ok {
		t.Fatal("a document that is not cached dated a version")
	}

	if w := f.do(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusOK {
		t.Fatalf("p2 = %d", w.Code)
	}

	got, ok := f.svc.PublishTime(ctx, f.repo, "acme/lib", "1.0.0")
	if !ok || time.Since(got) < 239*time.Hour || time.Since(got) > 241*time.Hour {
		t.Errorf("1.0.0 published = %v, %v; want about 240h ago", got, ok)
	}

	if got, ok := f.svc.PublishTime(ctx, f.repo, "acme/lib", "9.9.9"); ok {
		t.Errorf("unknown version dated %v", got)
	}
}
