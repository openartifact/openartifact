// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

// heldSummaryOf renders held-back versions as "version:source", in order.
func heldSummaryOf(t *testing.T, f *fixture, pkg string, wantSupported bool) []string {
	t.Helper()

	held, supported, err := f.svc.HeldBackVersions(context.Background(), f.repo, pkg)
	if err != nil || supported != wantSupported {
		t.Fatalf("HeldBackVersions(%s): supported=%v err=%v, want supported=%v", pkg, supported, err, wantSupported)
	}

	var out []string

	for _, h := range held {
		out = append(out, h.Version+":"+h.Hold.Source)

		if h.Hold.Source == curation.SourceMinAge && h.Hold.Until.IsZero() {
			t.Errorf("%s: min_age hold without an end", h.Version)
		}
	}

	return out
}

// requireUncached asserts the listing of an uncached package is unsupported,
// empty and never reaches the upstream.
func requireUncached(t *testing.T, f *fixture, pkg string) {
	t.Helper()

	if got := heldSummaryOf(t, f, pkg, false); len(got) != 0 {
		t.Fatalf("nothing cached: held=%v", got)
	}

	if f.hits.Load() != 0 {
		t.Fatalf("listing the held-back versions reached the upstream: %d requests", f.hits.Load())
	}
}

func requireNoNewHits(t *testing.T, f *fixture, hits int32) {
	t.Helper()

	if f.hits.Load() != hits {
		t.Error("HeldBackVersions reached the upstream")
	}
}

func TestPyPIHeldBackVersions_ListsFromTheCachedJSONPageOnly(t *testing.T) {
	f := curationPyPIFixture(t, 24)
	f.useCuration(curation.ModeDenylist, blockRule("requests", "2.31.0"))

	requireUncached(t, f, "requests")

	if w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON); w.Code != http.StatusOK {
		t.Fatalf("project page = %d", w.Code)
	}

	hits := f.hits.Load()

	// 2.33.0 (3h) is too new; 2.31.0 is curated; 2.32.0 passes and the undated
	// 2.34.0 passes the minimum age.
	got := heldSummaryOf(t, f, "Requests", true)
	if want := []string{"2.33.0:min_age", "2.31.0:curation"}; !slices.Equal(got, want) {
		t.Errorf("held back = %v, want %v", got, want)
	}

	requireNoNewHits(t, f, hits)
}

func TestPyPIHeldBackVersions_HTMLPageHasNoTimesAndIsUnsupported(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})
	f.repo.MinAgeHours = 24

	if w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON); w.Code != http.StatusOK {
		t.Fatalf("project page = %d", w.Code)
	}

	if got := heldSummaryOf(t, f, "requests", false); len(got) != 0 {
		t.Errorf("an HTML page listed %v", got)
	}
}

func TestComposerHeldBackVersions_ListsFromTheCachedDocumentOnly(t *testing.T) {
	f, _ := composerAgedFixture(t)

	requireUncached(t, f, "acme/lib")

	if w := f.do(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusOK {
		t.Fatalf("p2 = %d", w.Code)
	}

	// Blocked only now: blocked at fetch time, every version would be hidden
	// (1.1.0 is too new) and the document would not be cached.
	f.useCuration(curation.ModeDenylist, blockRule("acme/lib", "1.0.0"))

	hits := f.hits.Load()

	got := heldSummaryOf(t, f, "acme/lib", true)
	if want := []string{"1.1.0:min_age", "1.0.0:curation"}; !slices.Equal(got, want) {
		t.Errorf("held back = %v, want %v", got, want)
	}

	requireNoNewHits(t, f, hits)
}

func TestHelmHeldBackVersions_ListsFromTheCachedIndexOnly(t *testing.T) {
	f, g := helmFixture(t)
	helmCurationIndex(g)
	f.repo.MinAgeHours = 24
	f.useCuration(curation.ModeDenylist, blockRule("app", "1.0.0"))

	requireUncached(t, f, "app")

	if w := f.do(http.MethodGet, "index.yaml"); w.Code != http.StatusOK {
		t.Fatalf("index = %d", w.Code)
	}

	hits := f.hits.Load()

	// app 2.0.0 is an hour old; the undated 1.0.0 is curated; "other" is not asked.
	got := heldSummaryOf(t, f, "app", true)
	if want := []string{"2.0.0:min_age", "1.0.0:curation"}; !slices.Equal(got, want) {
		t.Errorf("held back = %v, want %v", got, want)
	}

	if got := heldSummaryOf(t, f, "other", true); len(got) != 0 {
		t.Errorf("other held back = %v, want none", got)
	}

	requireNoNewHits(t, f, hits)
}

func TestNuGetHeldBackVersions_ListsFromTheCachedRegistrationOnly(t *testing.T) {
	f, _ := nugetAgeFixture(t, nugetAgeGate)
	f.useCuration(curation.ModeDenylist, blockRule("lib.a", "1.0.0"))

	requireUncached(t, f, "Lib.A")

	if w := f.do(http.MethodGet, "v3/registration/lib.a/index.json"); w.Code != http.StatusOK {
		t.Fatalf("registration = %d", w.Code)
	}

	hits := f.hits.Load()

	// 2.0.0 (1h) is too new; 1.0.0 is curated; 1.1.0 (72h) and the unlisted
	// 3.0.0-beta (no date) pass.
	got := heldSummaryOf(t, f, "Lib.A", true)
	if want := []string{"2.0.0:min_age", "1.0.0:curation"}; !slices.Equal(got, want) {
		t.Errorf("held back = %v, want %v", got, want)
	}

	requireNoNewHits(t, f, hits)
}
