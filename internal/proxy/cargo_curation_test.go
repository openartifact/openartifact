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

const (
	cargoDemoDL10     = "api/v1/crates/demo/1.0.0/download"
	cargoDemoDL11     = "api/v1/crates/demo/1.1.0/download"
	cargoInflectorIdx = "index/in/fl/inflector"
)

func cargoIndexVersions(body string) []string {
	var out []string

	for _, v := range []string{"1.0.0", "1.1.0"} {
		if strings.Contains(body, `"vers":"`+v+`"`) {
			out = append(out, v)
		}
	}

	return out
}

func TestCargoCuration_IndexAndCrate(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		rules     []curation.Rule
		wantIndex []string // versions of demo left in the index
		wantDL10  int
		wantDL11  int
		wantInfl  int // Inflector's index
	}{
		{
			name:      "denylist hides the blocked version",
			mode:      curation.ModeDenylist,
			rules:     []curation.Rule{blockRule("demo", "1.1.0")},
			wantIndex: []string{"1.0.0"}, wantDL10: http.StatusOK, wantDL11: http.StatusForbidden, wantInfl: http.StatusOK,
		},
		{
			name:      "denylist of a whole crate answers 404 for its index",
			mode:      curation.ModeDenylist,
			rules:     []curation.Rule{blockRule("demo", "")},
			wantIndex: nil, wantDL10: http.StatusForbidden, wantDL11: http.StatusForbidden, wantInfl: http.StatusOK,
		},
		{
			name:      "crate names match lower-cased",
			mode:      curation.ModeDenylist,
			rules:     []curation.Rule{blockRule("inflector", "")},
			wantIndex: []string{"1.0.0", "1.1.0"}, wantDL10: http.StatusOK, wantDL11: http.StatusOK, wantInfl: http.StatusNotFound,
		},
		{
			name:      "allowlist leaves only the allowed crate",
			mode:      curation.ModeAllowlist,
			rules:     []curation.Rule{allowRule("demo", "")},
			wantIndex: []string{"1.0.0", "1.1.0"}, wantDL10: http.StatusOK, wantDL11: http.StatusOK, wantInfl: http.StatusNotFound,
		},
		{
			name:      "block beats allow",
			mode:      curation.ModeAllowlist,
			rules:     []curation.Rule{allowRule("demo", ""), blockRule("demo", "1.1.0")},
			wantIndex: []string{"1.0.0"}, wantDL10: http.StatusOK, wantDL11: http.StatusForbidden, wantInfl: http.StatusNotFound,
		},
		{
			name:      "mode off ignores the rules",
			mode:      curation.ModeOff,
			rules:     []curation.Rule{blockRule("demo", "")},
			wantIndex: []string{"1.0.0", "1.1.0"}, wantDL10: http.StatusOK, wantDL11: http.StatusOK, wantInfl: http.StatusOK,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, g := cargoFixture(t)
			f.useCuration(c.mode, c.rules...)

			w := f.do(http.MethodGet, cargoGateIndex)
			if len(c.wantIndex) == 0 {
				if w.Code != http.StatusNotFound {
					t.Errorf("demo index = %d, want 404", w.Code)
				}
			} else if got := cargoIndexVersions(w.Body.String()); w.Code != http.StatusOK || strings.Join(got, ",") != strings.Join(c.wantIndex, ",") {
				t.Errorf("demo index = %d %v, want %v", w.Code, got, c.wantIndex)
			}

			if got := f.do(http.MethodGet, cargoInflectorIdx).Code; got != c.wantInfl {
				t.Errorf("Inflector index = %d, want %d", got, c.wantInfl)
			}

			if got := f.do(http.MethodGet, cargoDemoDL10).Code; got != c.wantDL10 {
				t.Errorf("1.0.0 download = %d, want %d", got, c.wantDL10)
			}

			if got := f.do(http.MethodGet, cargoDemoDL11).Code; got != c.wantDL11 {
				t.Errorf("1.1.0 download = %d, want %d", got, c.wantDL11)
			}

			if c.wantDL11 == http.StatusForbidden && f.db.entry("repo-1", cargoDemoDL11) != nil {
				t.Error("a curated crate was cached")
			}

			if c.wantDL11 == http.StatusForbidden && slices.Contains(g.dlPaths, "/crates/demo/1.1.0/download") {
				t.Errorf("static host was asked for a curated crate: %v", g.dlPaths)
			}
		})
	}
}

func TestCargoCuration_RefusalNamesTheReason(t *testing.T) {
	f, _ := cargoFixture(t)
	f.useCuration(curation.ModeDenylist, curation.Rule{Action: curation.ActionBlock, Package: "demo", Version: "1.1.0", Reason: "cve-1"})

	w := f.do(http.MethodGet, cargoDemoDL11)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "cve-1") {
		t.Errorf("= %d %q, want 403 naming the reason", w.Code, w.Body.String())
	}
}

func TestCargoCuration_CachedCrateIsRefusedOnceBlocked(t *testing.T) {
	f, g := cargoFixture(t)
	src := f.useCuration(curation.ModeDenylist)

	if w := f.do(http.MethodGet, cargoDemoDL11); w.Code != http.StatusOK {
		t.Fatalf("crate while nothing is blocked = %d", w.Code)
	}

	if f.db.entry("repo-1", cargoDemoDL11) == nil {
		t.Fatal("crate was not cached")
	}

	src.set(blockRule("demo", "1.1.0"))

	dls := len(g.dlPaths)

	if w := f.do(http.MethodGet, cargoDemoDL11); w.Code != http.StatusForbidden {
		t.Errorf("cached crate after the block = %d, want 403", w.Code)
	}

	if len(g.dlPaths) != dls {
		t.Error("refusal reached the static host")
	}
}

func TestCargoCuration_AllowExemptsFromTheMinimumAge(t *testing.T) {
	f, g := cargoFixture(t)
	f.repo.MinAgeHours = 24

	g.pubtime = map[string]string{
		"demo/1.0.0": time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339),
		"demo/1.1.0": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}

	// Without the allow rule the young version is held back by the minimum age.
	f.useCuration(curation.ModeDenylist, blockRule("other", ""))

	if w := f.do(http.MethodGet, cargoDemoDL11); w.Code != http.StatusForbidden {
		t.Fatalf("young crate without an allow rule = %d, want 403", w.Code)
	}

	f.useCuration(curation.ModeDenylist, allowRule("demo", "1.1.0"))

	w := f.do(http.MethodGet, cargoGateIndex)
	if got := cargoIndexVersions(w.Body.String()); w.Code != http.StatusOK || len(got) != 2 {
		t.Errorf("index = %d %v, want both versions", w.Code, got)
	}

	if w := f.do(http.MethodGet, cargoDemoDL11); w.Code != http.StatusOK {
		t.Errorf("allowed young crate = %d, want 200", w.Code)
	}
}

func TestCargoCuration_ValidatorFollowsTheRules(t *testing.T) {
	f, _ := cargoFixture(t)
	src := f.useCuration(curation.ModeDenylist, blockRule("demo", "1.1.0"))

	first := f.do(http.MethodGet, cargoGateIndex).Header().Get("ETag")

	// The same rules and the same cached blob keep the validator.
	if again := f.do(http.MethodGet, cargoGateIndex).Header().Get("ETag"); first == "" || again != first {
		t.Fatalf("ETag %q then %q, want it stable", first, again)
	}

	src.set(blockRule("demo", "1.0.0"))

	if second := f.do(http.MethodGet, cargoGateIndex).Header().Get("ETag"); second == first {
		t.Errorf("ETag %q did not change with the rules", second)
	}

	// A conditional request with the old validator is not answered 304.
	req := f.doWith(cargoGateIndex, map[string]string{"If-None-Match": first})
	if req.Code != http.StatusOK {
		t.Errorf("stale validator = %d, want 200", req.Code)
	}

	// An inactive gate leaves the index verbatim: no rendering suffix.
	f.repo.CurationMode = curation.ModeOff

	if off := f.do(http.MethodGet, cargoGateIndex).Header().Get("ETag"); strings.Contains(off, "~") {
		t.Errorf("verbatim ETag %q carries a gate stamp", off)
	}
}

func TestCargoCuration_RulesFailClosed(t *testing.T) {
	f, _ := cargoFixture(t)
	f.repo.CurationMode = curation.ModeDenylist // no rule source configured

	for _, p := range []string{cargoGateIndex, cargoDemoDL10} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", p, w.Code)
		}
	}
}

func TestCargoCuration_PublishedReadsTheCachedIndexOnly(t *testing.T) {
	f, g := cargoFixture(t)
	g.pubtime = map[string]string{"demo/1.0.0": "2025-01-02T03:04:05Z"}

	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "demo", "1.0.0"); ok {
		t.Fatal("publish time known before the index was cached")
	}

	if f.hits.Load() != 0 {
		t.Fatalf("PublishTime reached the upstream: %d requests", f.hits.Load())
	}

	f.do(http.MethodGet, cargoGateIndex)

	hits := f.hits.Load()

	got, ok := f.svc.PublishTime(ctx, f.repo, "demo", "1.0.0")
	if want := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("PublishTime = %v, %v; want %v", got, ok, want)
	}

	if _, ok = f.svc.PublishTime(ctx, f.repo, "DEMO", "1.1.0"); ok {
		t.Error("an undated version has a publish time")
	}

	if _, ok = f.svc.PublishTime(ctx, f.repo, "demo", "9.9.9"); ok {
		t.Error("an unknown version has a publish time")
	}

	if f.hits.Load() != hits {
		t.Error("PublishTime reached the upstream")
	}
}
