// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

func sortedVersions(d gatedPackument) []string {
	out := make([]string, 0, len(d.Versions))
	for v := range d.Versions {
		out = append(out, v)
	}

	sort.Strings(out)

	return out
}

func TestNPMCuration_PackumentAndTarballFollowTheRules(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		rules    []curation.Rule
		versions []string
		latest   string
		tarballs map[string]int // version -> status
	}{
		{
			name:     "denylist hides the blocked version and moves latest",
			mode:     curation.ModeDenylist,
			rules:    []curation.Rule{blockRule("lib", "2.0.0")},
			versions: []string{"0.9.0", "1.0.0", "1.1.0", "2.1.0-rc.1"},
			latest:   "1.1.0",
			tarballs: map[string]int{"2.0.0": http.StatusForbidden, "1.1.0": http.StatusOK},
		},
		{
			name:     "version glob blocks a whole line",
			mode:     curation.ModeDenylist,
			rules:    []curation.Rule{blockRule("li?", "1.*")},
			versions: []string{"0.9.0", "2.0.0", "2.1.0-rc.1"},
			latest:   "2.0.0",
			tarballs: map[string]int{"1.0.0": http.StatusForbidden, "1.1.0": http.StatusForbidden, "2.0.0": http.StatusOK},
		},
		{
			name:     "a version without a publish time is curated all the same",
			mode:     curation.ModeDenylist,
			rules:    []curation.Rule{blockRule("lib", "0.9.0")},
			versions: []string{"1.0.0", "1.1.0", "2.0.0", "2.1.0-rc.1"},
			latest:   "2.0.0",
			tarballs: map[string]int{"0.9.0": http.StatusForbidden},
		},
		{
			name:     "allowlist offers only the allowed versions",
			mode:     curation.ModeAllowlist,
			rules:    []curation.Rule{allowRule("lib", "1.*")},
			versions: []string{"1.0.0", "1.1.0"},
			latest:   "1.1.0",
			tarballs: map[string]int{"2.0.0": http.StatusForbidden, "1.0.0": http.StatusOK},
		},
		{
			name:     "block beats allow",
			mode:     curation.ModeAllowlist,
			rules:    []curation.Rule{allowRule("lib", ""), blockRule("lib", "1.0.0")},
			versions: []string{"0.9.0", "1.1.0", "2.0.0", "2.1.0-rc.1"},
			latest:   "2.0.0",
			tarballs: map[string]int{"1.0.0": http.StatusForbidden, "1.1.0": http.StatusOK},
		},
		{
			name:     "mode off leaves the rules inert",
			mode:     curation.ModeOff,
			rules:    []curation.Rule{blockRule("lib", "")},
			versions: []string{"0.9.0", "1.0.0", "1.1.0", "2.0.0", "2.1.0-rc.1"},
			latest:   "2.0.0",
			tarballs: map[string]int{"2.0.0": http.StatusOK},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := minAgeFixture(t, 0)
			f.useCuration(tt.mode, tt.rules...)

			w := f.do(http.MethodGet, "lib")
			if w.Code != http.StatusOK {
				t.Fatalf("packument = %d: %s", w.Code, w.Body.String())
			}

			d := decodeGated(t, w.Body.String())
			if got := sortedVersions(d); !slices.Equal(got, tt.versions) {
				t.Errorf("versions = %v, want %v", got, tt.versions)
			}

			if d.DistTags["latest"] != tt.latest {
				t.Errorf("latest = %q, want %q", d.DistTags["latest"], tt.latest)
			}

			for v, want := range tt.tarballs {
				if w := f.do(http.MethodGet, "lib/-/lib-"+v+".tgz"); w.Code != want {
					t.Errorf("tarball %s = %d %q, want %d", v, w.Code, w.Body.String(), want)
				}
			}
		})
	}
}

func TestNPMCuration_RefusedTarballNamesTheReasonAndIsNeverFetched(t *testing.T) {
	f, reg := minAgeFixture(t, 0)
	f.useCuration(
		curation.ModeDenylist,
		curation.Rule{Action: curation.ActionBlock, Package: "lib", Version: "2.0.0", Reason: "CVE-2099-1"},
	)

	w := f.do(http.MethodGet, "lib/-/lib-2.0.0.tgz")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "CVE-2099-1") {
		t.Errorf("tarball = %d %q, want 403 naming the reason", w.Code, w.Body.String())
	}

	if got := reg.tarballGets.Load(); got != 0 {
		t.Errorf("the upstream tarball was fetched %d times", got)
	}
}

func TestNPMCuration_ExcludedPackageLeavesNothingToServe(t *testing.T) {
	f, _ := minAgeFixture(t, 0)
	f.useCuration(curation.ModeAllowlist, allowRule("other", ""))

	for _, path := range []string{"lib", "lib/latest", "lib/1.0.0"} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (the package is not on the allowlist)", path, w.Code)
		}
	}

	if w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusForbidden {
		t.Errorf("tarball of an unlisted package = %d, want 403", w.Code)
	}
}

func TestNPMCuration_AllowExemptsTooNewVersionsFromTheMinimumAge(t *testing.T) {
	f, _ := minAgeFixture(t, npmMinAgeGate)
	f.useCuration(curation.ModeDenylist, allowRule("lib", "2.0.0"))

	d := decodeGated(t, f.do(http.MethodGet, "lib").Body.String())
	if want := []string{"0.9.0", "1.0.0", "1.1.0", "2.0.0"}; !slices.Equal(sortedVersions(d), want) {
		t.Errorf("versions = %v, want %v (2.0.0 exempt, 2.1.0-rc.1 still too new)", sortedVersions(d), want)
	}

	if d.DistTags["latest"] != "2.0.0" {
		t.Errorf("latest = %q, want 2.0.0", d.DistTags["latest"])
	}

	if w := f.do(http.MethodGet, "lib/-/lib-2.0.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("exempt tarball = %d, want 200", w.Code)
	}

	if w := f.do(http.MethodGet, "lib/-/lib-2.1.0-rc.1.tgz"); w.Code != http.StatusForbidden {
		t.Errorf("non-exempt young tarball = %d, want 403", w.Code)
	}
}

func TestNPMCuration_ValidatorFollowsRulesAndModeButNotAnInactiveGate(t *testing.T) {
	f, _ := minAgeFixture(t, 0)
	etag := func() string { return f.do(http.MethodGet, "lib").Header().Get("ETag") }

	plain := etag()
	src := f.useCuration(curation.ModeDenylist, blockRule("lib", "2.0.0"))
	first := etag()

	src.set(blockRule("lib", "1.0.0"))
	second := etag()

	src.set(allowRule("lib", "1.*"))
	f.repo.CurationMode = curation.ModeAllowlist
	third := etag()

	seen := map[string]bool{plain: true, first: true, second: true, third: true}
	if len(seen) != 4 || plain == "" {
		t.Errorf("validators %q %q %q %q; want four different ones", plain, first, second, third)
	}

	if w := f.doWith("lib", map[string]string{"If-None-Match": third}); w.Code != http.StatusNotModified {
		t.Errorf("revalidating with the current validator = %d, want 304", w.Code)
	}

	if w := f.doWith("lib", map[string]string{"If-None-Match": second}); w.Code != http.StatusOK {
		t.Errorf("revalidating with a validator of other rules = %d, want 200", w.Code)
	}

	f.repo.CurationMode = curation.ModeOff
	if off := etag(); off != plain {
		t.Errorf("curation off changed the validator: %q, want %q", off, plain)
	}
}

func TestNPMCuration_CachedTarballIsRefusedOnceItsVersionIsCurated(t *testing.T) {
	f, _ := minAgeFixture(t, 0)

	if w := f.do(http.MethodGet, "lib/-/lib-2.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("tarball with curation off = %d", w.Code)
	}

	f.useCuration(curation.ModeDenylist, blockRule("lib", "2.0.0"))

	if w := f.do(http.MethodGet, "lib/-/lib-2.0.0.tgz"); w.Code != http.StatusForbidden {
		t.Errorf("cached tarball of a curated version = %d, want 403", w.Code)
	}

	if w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("tarball of another version = %d, want 200", w.Code)
	}
}

func TestNPMCuration_AbbreviatedRequestIsAnsweredFromTheFullPackument(t *testing.T) {
	f, reg := minAgeFixture(t, 0)
	f.useCuration(curation.ModeDenylist, blockRule("lib", "2.0.0"))

	w := f.doWith("lib", map[string]string{"Accept": pkgnpm.AbbreviatedContentType})
	if got := reg.abbrServed.Load(); got != 0 {
		t.Errorf("the abbreviated document was fetched %d times; a curated repository must not use it", got)
	}

	if _, ok := decodeGated(t, w.Body.String()).Versions["2.0.0"]; ok {
		t.Errorf("abbreviated request leaked a curated version: %s", w.Body.String())
	}
}

func TestRenderNPM_CuratedScopedPackage(t *testing.T) {
	const doc = `{"dist-tags":{"latest":"2.0.0"},"versions":{"1.0.0":{},"2.0.0":{}}}`

	repo := &models.Repository{Format: "npm", RepoType: "proxy", CurationMode: curation.ModeDenylist}
	p := pkgnpm.Path{Kind: pkgnpm.KindPackument, Package: "@Acme/Core"}
	g := newGate(repo, []curation.Rule{blockRule("@acme/*", "2.*")}, time.Now())

	out, err := renderNPM([]byte(doc), p, "http://oa.test/repos/r", g)
	if err != nil {
		t.Fatal(err)
	}

	d := decodeGated(t, string(out))
	if !slices.Equal(sortedVersions(d), []string{"1.0.0"}) || d.DistTags["latest"] != "1.0.0" {
		t.Errorf("versions %v, tags %v; want only 1.0.0", sortedVersions(d), d.DistTags)
	}

	g = newGate(repo, []curation.Rule{blockRule("@acme/*", "")}, time.Now())
	if _, err := renderNPM([]byte(doc), p, "http://oa.test/repos/r", g); !errors.Is(err, errNotFound) {
		t.Errorf("every version curated: err = %v, want errNotFound", err)
	}
}

func TestNPMPublished_DatesFromTheCachedPackumentOnly(t *testing.T) {
	f, _ := minAgeFixture(t, 0)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "lib", "1.0.0"); ok {
		t.Fatal("a packument that is not cached dated a version")
	}

	if w := f.do(http.MethodGet, "lib"); w.Code != http.StatusOK {
		t.Fatalf("packument = %d", w.Code)
	}

	got, ok := f.svc.PublishTime(ctx, f.repo, "lib", "1.0.0")
	if !ok || time.Since(got) < 199*time.Hour || time.Since(got) > 201*time.Hour {
		t.Errorf("1.0.0 published = %v, %v; want about 200h ago", got, ok)
	}

	for _, version := range []string{"0.9.0", "9.9.9"} {
		if got, ok := f.svc.PublishTime(ctx, f.repo, "lib", version); ok {
			t.Errorf("%s dated %v; want no date for an undated or unknown version", version, got)
		}
	}
}
