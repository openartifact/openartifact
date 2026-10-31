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

// The feed of nuget_minage_test.go serves lib.a 1.0.0, 1.1.0, 2.0.0, 3.0.0-beta
// (registration) and 2.1.0 (flat container only).

func TestNuGetCuration_DenylistHidesAVersionFromMetadata(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)
	f.useCuration(curation.ModeDenylist, blockRule("Lib.A", "1.1.*"))

	versions, counts := nugetRegVersions(t, f.do(http.MethodGet, "v3/registration/lib.a/index.json").Body.Bytes())
	if want := []string{"1.0.0", "2.0.0", "3.0.0-beta"}; !slices.Equal(versions, want) {
		t.Errorf("registration versions = %v, want %v", versions, want)
	}

	if want := []int{1, 3}; !slices.Equal(counts, want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}

	list := nugetFlatVersions(t, f.do(http.MethodGet, "v3/package/Lib.A/index.json"))
	if want := []string{"1.0.0", "2.0.0", "2.1.0", "3.0.0-beta"}; !slices.Equal(list, want) {
		t.Errorf("flat versions = %v, want %v", list, want)
	}

	if w := f.do(http.MethodGet, "v3/registration/lib.a/1.1.0.json"); w.Code != http.StatusNotFound {
		t.Errorf("leaf of a blocked version = %d, want 404", w.Code)
	}

	if w := f.do(http.MethodGet, "v3/registration/lib.a/1.0.0.json"); w.Code != http.StatusOK {
		t.Errorf("leaf of another version = %d, want 200", w.Code)
	}
}

func TestNuGetCuration_DenylistRefusesFilesAndNeverFetchesThem(t *testing.T) {
	f, feed := nugetAgeFixture(t, 0)
	f.useCuration(curation.ModeDenylist, blockRule("lib.a", "1.1.0"))

	for _, p := range []string{
		"v3/package/lib.a/1.1.0/lib.a.1.1.0.nupkg",
		"v3/package/lib.a/1.1.0/lib.a.nuspec",
	} {
		w := f.do(http.MethodGet, p)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "curation") {
			t.Errorf("GET %s = %d %q, want 403 naming curation", p, w.Code, w.Body.String())
		}
	}

	if got := feed.nupkgGets.Load(); got != 0 {
		t.Errorf("the upstream .nupkg was fetched %d times", got)
	}

	if w := f.do(http.MethodGet, "v3/package/lib.a/1.0.0/lib.a.nuspec"); w.Code != http.StatusOK {
		t.Errorf("an unblocked nuspec = %d, want 200", w.Code)
	}
}

func TestNuGetCuration_NeedsNoPublishDates(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)
	f.useCuration(curation.ModeDenylist, blockRule("lib.a", "1.1.0"))

	// The flat list is judged by rules alone: no registration is fetched for it,
	// and the unlisted-and-young versions stay as the upstream lists them.
	f.do(http.MethodGet, "v3/package/lib.a/index.json")
	f.do(http.MethodGet, "v3/package/lib.a/1.0.0/lib.a.nuspec")

	if e, _ := f.db.GetProxyCacheEntry(context.Background(), f.repo.ID, "v3/registration/lib.a/index.json"); e != nil {
		t.Error("the registration was fetched just for curation")
	}

	if e, _ := f.db.GetProxyCacheEntry(context.Background(), f.repo.ID, "v3/registration/lib.a/1.0.0.json"); e != nil {
		t.Error("a registration leaf was fetched just for curation")
	}
}

func TestNuGetCuration_Allowlist(t *testing.T) {
	cases := []struct {
		name  string
		rules []curation.Rule
		path  string
		want  int
	}{
		{"an allowed package", []curation.Rule{allowRule("lib.a", "*")}, "v3/registration/lib.a/1.0.0.json", http.StatusOK},
		{
			"another package is not allowed",
			[]curation.Rule{allowRule("lib.a", "*")},
			"v3/registration/fresh/index.json",
			http.StatusNotFound,
		},
		{
			"a version not allowed",
			[]curation.Rule{allowRule("lib.a", "1.0.0")},
			"v3/package/lib.a/1.1.0/lib.a.nuspec",
			http.StatusForbidden,
		},
		{"a package not allowed", []curation.Rule{allowRule("lib.a", "*")}, "v3/package/big/1.0.0/big.nuspec", http.StatusForbidden},
		{
			"block beats allow",
			[]curation.Rule{allowRule("lib.a", "*"), blockRule("lib.a", "2.0.0")},
			"v3/package/lib.a/2.0.0/lib.a.nuspec",
			http.StatusForbidden,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := nugetAgeFixture(t, 0)
			f.useCuration(curation.ModeAllowlist, c.rules...)

			if w := f.do(http.MethodGet, c.path); w.Code != c.want {
				t.Errorf("GET %s = %d, want %d (%s)", c.path, w.Code, c.want, w.Body.String())
			}
		})
	}

	f, _ := nugetAgeFixture(t, 0)
	f.useCuration(curation.ModeAllowlist, allowRule("lib.a", "1.0.0"))

	if list := nugetFlatVersions(t, f.do(http.MethodGet, "v3/package/lib.a/index.json")); !slices.Equal(list, []string{"1.0.0"}) {
		t.Errorf("flat versions = %v, want only the allowed one", list)
	}
}

func TestNuGetCuration_AllowRuleExemptsAYoungVersionFromTheMinimumAge(t *testing.T) {
	f, _ := nugetAgeFixture(t, nugetAgeGate)
	f.useCuration(curation.ModeDenylist, allowRule("lib.a", "2.0.0"))

	versions, _ := nugetRegVersions(t, f.do(http.MethodGet, "v3/registration/lib.a/index.json").Body.Bytes())
	if want := []string{"1.0.0", "1.1.0", "2.0.0", "3.0.0-beta"}; !slices.Equal(versions, want) {
		t.Errorf("versions = %v, want the young 2.0.0 to stay %v", versions, want)
	}

	if w := f.do(http.MethodGet, "v3/package/lib.a/2.0.0/lib.a.nuspec"); w.Code != http.StatusOK {
		t.Errorf("young nuspec with an allow rule = %d, want 200", w.Code)
	}

	if w := f.do(http.MethodGet, "v3/package/big/3.0.0/big.nuspec"); w.Code != http.StatusForbidden {
		t.Errorf("young nuspec of another package = %d, want 403", w.Code)
	}
}

func TestNuGetCuration_RuleChangeChangesTheValidator(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)

	const doc = "v3/package/lib.a/index.json"

	etag := func() string { return f.do(http.MethodGet, doc).Header().Get("ETag") }

	ungated := etag()
	if ungated == "" || etag() != ungated {
		t.Fatalf("ungated ETag %q is not stable", ungated)
	}

	src := f.useCuration(curation.ModeDenylist, blockRule("lib.a", "1.1.0"))

	blocked := etag()
	if blocked == ungated || etag() != blocked {
		t.Fatalf("curation on: ETag %q (ungated %q) must differ and be stable", blocked, ungated)
	}

	src.set(blockRule("lib.a", "2.0.0"))

	if other := etag(); other == blocked || other == ungated {
		t.Errorf("ETag did not change with the rules: %q", other)
	}

	f.repo.CurationMode = curation.ModeOff

	if off := etag(); off != ungated {
		t.Errorf("curation off: ETag %q differs from the ungated %q", off, ungated)
	}
}

func TestNuGetCuration_CachedFilesAreRefusedWhenTheRulesChange(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)

	const file = "v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"

	if w := f.do(http.MethodGet, file); w.Code != http.StatusOK {
		t.Fatalf("fetch = %d", w.Code)
	}

	f.useCuration(curation.ModeDenylist, blockRule("lib.a", "1.0.0"))

	if w := f.do(http.MethodGet, file); w.Code != http.StatusForbidden {
		t.Errorf("cached nupkg of a blocked version = %d, want 403", w.Code)
	}
}

func TestNuGetCuration_UnreadableRulesFailClosed(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)
	f.repo.CurationMode = curation.ModeDenylist

	for _, p := range []string{"v3/package/lib.a/index.json", "v3/package/lib.a/1.0.0/lib.a.nuspec"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", p, w.Code)
		}
	}
}

func TestNuGetPublished_ReadsTheCachedRegistrationOnly(t *testing.T) {
	f, _ := nugetAgeFixture(t, 0)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "Lib.A", "1.1.0"); ok {
		t.Fatal("an uncached registration dated the version")
	}

	if f.hits.Load() != 0 {
		t.Fatalf("PublishTime reached the upstream: %d requests", f.hits.Load())
	}

	if w := f.do(http.MethodGet, "v3/registration/lib.a/index.json"); w.Code != http.StatusOK {
		t.Fatalf("registration = %d", w.Code)
	}

	got, ok := f.svc.PublishTime(ctx, f.repo, "Lib.A", "1.1.0")
	if want := time.Now().Add(-72 * time.Hour); !ok || got.Sub(want).Abs() > time.Minute {
		t.Errorf("PublishTime = %v, %v; want about %v", got, ok, want)
	}

	if _, ok := f.svc.PublishTime(ctx, f.repo, "lib.a", "3.0.0-beta"); ok {
		t.Error("an unlisted version (year 1900) was dated")
	}

	if _, ok := f.svc.PublishTime(ctx, f.repo, "lib.a", "9.9.9"); ok {
		t.Error("an unknown version was dated")
	}
}
