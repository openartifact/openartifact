// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
)

// heldVersionSources is "version:source" of every held-back version, in order.
func heldVersionSources(t *testing.T, held []HeldBack) []string {
	t.Helper()

	got := make([]string, 0, len(held))

	for _, h := range held {
		got = append(got, h.Version+":"+h.Hold.Source)

		if h.Hold.Source == curation.SourceMinAge && h.Hold.Until.IsZero() {
			t.Errorf("%s: min_age hold without an end", h.Version)
		}
	}

	return got
}

func TestRubyGemsHeldBackVersions_ListsFromTheCachedAPIOnly(t *testing.T) {
	f, _ := gemAgeFixture(t, gemAgeGate)
	f.useCuration(curation.ModeDenylist, blockRule("lib", "1.0.0"))

	ctx := context.Background()

	if held, supported, err := f.svc.HeldBackVersions(ctx, f.repo, "lib"); err != nil || supported || len(held) != 0 {
		t.Fatalf("nothing cached: held=%v supported=%v err=%v", held, supported, err)
	}

	if f.hits.Load() != 0 {
		t.Fatalf("HeldBackVersions reached the upstream: %d requests", f.hits.Load())
	}

	if w := gemDo(f, http.MethodGet, "info/lib", nil); w.Code != http.StatusOK {
		t.Fatalf("info = %d", w.Code)
	}

	hits := f.hits.Load()

	held, supported, err := f.svc.HeldBackVersions(ctx, f.repo, "lib")
	if err != nil || !supported {
		t.Fatalf("supported=%v err=%v", supported, err)
	}

	// 2.0.0 and its java platform are one version; 1.1.0 (72h) passes the 48h
	// gate; 3.0.0 is not in the API.
	if want := []string{"2.0.0:min_age", "1.0.0:curation"}; !slices.Equal(heldVersionSources(t, held), want) {
		t.Errorf("held back = %v, want %v", heldVersionSources(t, held), want)
	}

	if f.hits.Load() != hits {
		t.Error("HeldBackVersions reached the upstream")
	}
}

func TestGoHeldBackVersions_ListsFromTheCachedListOnly(t *testing.T) {
	e := goCurationEnv(t)
	e.setInfo("v1.3.0", time.Now().Add(-time.Hour))
	e.useCuration(curation.ModeDenylist, blockRule(goMod1, "v1.0.0"))

	ctx := context.Background()

	if held, supported, err := e.svc.HeldBackVersions(ctx, e.repo, goMod1); err != nil || supported || len(held) != 0 {
		t.Fatalf("nothing cached: held=%v supported=%v err=%v", held, supported, err)
	}

	if len(e.up.paths) != 0 {
		t.Fatalf("HeldBackVersions reached the upstream: %v", e.up.paths)
	}

	// Cache the list and the .info of two versions with the gate off; v1.0.0
	// never gets an .info.
	for _, p := range []string{"list", goVersion1 + ".info", "v1.3.0.info"} {
		if w := e.get(goGateBase + p); w.Code != http.StatusOK {
			t.Fatalf("%s = %d", p, w.Code)
		}
	}

	e.repo.MinAgeHours = 24
	seen := len(e.up.paths)

	held, supported, err := e.svc.HeldBackVersions(ctx, e.repo, goMod1)
	if err != nil || !supported {
		t.Fatalf("supported=%v err=%v", supported, err)
	}

	// v1.3.0 is too new; v1.2.3 is old; v1.0.0 has no time so only curation
	// holds it, and it sorts after the dated version.
	if want := []string{"v1.3.0:min_age", "v1.0.0:curation"}; !slices.Equal(heldVersionSources(t, held), want) {
		t.Errorf("held back = %v, want %v", heldVersionSources(t, held), want)
	}

	if last := held[len(held)-1]; !last.Published.IsZero() {
		t.Errorf("v1.0.0 published = %v, want zero", last.Published)
	}

	if len(e.up.paths) != seen {
		t.Error("HeldBackVersions reached the upstream")
	}
}

func TestCondaHeldBackVersions_ListsFromTheCachedRepodataOnly(t *testing.T) {
	f, up := condaGateFixture(t)
	f.useCuration(curation.ModeDenylist, blockRule("old", "1.0"))

	ctx := context.Background()

	if held, supported, err := f.svc.HeldBackVersions(ctx, f.repo, "demo"); err != nil || supported || len(held) != 0 {
		t.Fatalf("nothing cached: held=%v supported=%v err=%v", held, supported, err)
	}

	if up.hitsOf("linux-64/repodata.json.zst") != 0 || f.hits.Load() != 0 {
		t.Fatal("HeldBackVersions reached the upstream")
	}

	if w := f.do(http.MethodGet, "linux-64/repodata.json.zst"); w.Code != http.StatusOK {
		t.Fatalf("repodata = %d", w.Code)
	}

	hits := f.hits.Load()

	// The name matches without regard to case.
	held, supported, err := f.svc.HeldBackVersions(ctx, f.repo, "Demo")
	if err != nil || !supported {
		t.Fatalf("supported=%v err=%v", supported, err)
	}

	if want := []string{"1.0:min_age"}; !slices.Equal(heldVersionSources(t, held), want) {
		t.Errorf("demo held back = %v, want %v", heldVersionSources(t, held), want)
	}

	held, supported, err = f.svc.HeldBackVersions(ctx, f.repo, "old")
	if err != nil || !supported {
		t.Fatalf("supported=%v err=%v", supported, err)
	}

	if want := []string{"1.0:curation"}; !slices.Equal(heldVersionSources(t, held), want) {
		t.Errorf("old held back = %v, want %v", heldVersionSources(t, held), want)
	}

	if f.hits.Load() != hits {
		t.Error("HeldBackVersions reached the upstream")
	}
}

func TestAnsibleHeldBackVersions_ListsFromTheCachedVersionListOnly(t *testing.T) {
	f, _ := ansibleGateFixture(t)
	gate := f.repo.MinAgeHours
	f.repo.MinAgeHours = 0 // cache the list unfiltered: every version is held back otherwise
	f.useCuration(curation.ModeDenylist, blockRule("ns.demo", "1.0.0"))

	ctx := context.Background()

	if held, supported, err := f.svc.HeldBackVersions(ctx, f.repo, "ns.demo"); err != nil || supported || len(held) != 0 {
		t.Fatalf("nothing cached: held=%v supported=%v err=%v", held, supported, err)
	}

	if f.hits.Load() != 0 {
		t.Fatalf("HeldBackVersions reached the upstream: %d requests", f.hits.Load())
	}

	if w := ansibleGet(t, f, ansibleVersionsPath, nil); w.Code != http.StatusOK {
		t.Fatalf("versions = %d", w.Code)
	}

	f.repo.MinAgeHours = gate
	hits := f.hits.Load()

	held, supported, err := f.svc.HeldBackVersions(ctx, f.repo, "ns.demo")
	if err != nil || !supported {
		t.Fatalf("supported=%v err=%v", supported, err)
	}

	if want := []string{"2.0.0-rc.1:min_age", "1.1.0:min_age", "1.0.0:curation"}; !slices.Equal(heldVersionSources(t, held), want) {
		t.Errorf("held back = %v, want %v", heldVersionSources(t, held), want)
	}

	if held, supported, err = f.svc.HeldBackVersions(ctx, f.repo, "nodots"); err != nil || supported || len(held) != 0 {
		t.Errorf("a name that is no collection: held=%v supported=%v err=%v", held, supported, err)
	}

	if f.hits.Load() != hits {
		t.Error("HeldBackVersions reached the upstream")
	}
}
