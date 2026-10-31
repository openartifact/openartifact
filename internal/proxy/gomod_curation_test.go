// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
)

// goCurationEnv is a go repository whose module example.com/Mod has v1.0.0,
// v1.2.3 and a newer v1.3.0 (which is also its @latest).
func goCurationEnv(t *testing.T) *goEnv {
	t.Helper()

	e := newGoEnv(t)
	e.setList("v1.0.0", goVersion1, "v1.3.0")
	e.setInfo("v1.3.0", time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC))
	e.up.set(goEscMod1+"/@latest", []byte(`{"Version":"v1.3.0","Time":"2024-03-01T00:00:00Z"}`))
	e.up.set(goGateBase+"v1.3.0.mod", []byte("module "+goMod1+"\n"))
	e.up.set(goGateBase+"v1.3.0.zip", []byte("zip"))

	return e
}

func TestGoCuration_DenylistHidesAVersionEverywhere(t *testing.T) {
	e := goCurationEnv(t)
	e.useCuration(curation.ModeDenylist, blockRule(goMod1, "v1.3.0"))

	list := e.get(goGateBase + "list")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "v1.3.0") || !strings.Contains(list.Body.String(), goVersion1) {
		t.Errorf("list: status %d, body %q", list.Code, list.Body.String())
	}

	latest := e.get(goEscMod1 + "/@latest")
	if latest.Code != http.StatusOK || !strings.Contains(latest.Body.String(), goVersion1) {
		t.Errorf("@latest did not move to %s: status %d, body %q", goVersion1, latest.Code, latest.Body.String())
	}

	for _, ext := range []string{".info", ".mod", ".zip"} {
		w := e.get(goGateBase + "v1.3.0" + ext)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "curation") {
			t.Errorf("v1.3.0%s: status %d, body %q; want 403 naming curation", ext, w.Code, w.Body.String())
		}

		if e.upstreamSaw("/v1.3.0" + ext) {
			t.Errorf("v1.3.0%s was requested upstream", ext)
		}
	}

	if w := e.get(goGateBase + goVersion1 + ".mod"); w.Code != http.StatusOK {
		t.Errorf("an unblocked version: status %d", w.Code)
	}
}

func TestGoCuration_NeedsNoPublishDates(t *testing.T) {
	e := goCurationEnv(t)
	e.useCuration(curation.ModeDenylist, blockRule(goMod1, "v1.3.0"))

	e.get(goGateBase + "list")
	e.get(goEscMod1 + "/@latest")

	// Only the @latest and the .info of its replacement are read; the list and
	// the versions below it are not probed for a date.
	for _, p := range e.up.paths {
		if strings.HasSuffix(p, "/v1.0.0.info") || strings.HasSuffix(p, "/v1.3.0.info") {
			t.Errorf("curation looked up %s", p)
		}
	}
}

func TestGoCuration_Allowlist(t *testing.T) {
	cases := []struct {
		name  string
		rules []curation.Rule
		path  string
		want  int
	}{
		{"an allowed version", []curation.Rule{allowRule(goMod1, goVersion1)}, goGateBase + goVersion1 + ".info", http.StatusOK},
		{"a version not allowed", []curation.Rule{allowRule(goMod1, goVersion1)}, goGateBase + "v1.3.0.zip", http.StatusForbidden},
		{
			"a module not allowed",
			[]curation.Rule{allowRule("example.org/other", "*")},
			goGateBase + goVersion1 + ".mod",
			http.StatusForbidden,
		},
		{
			"block beats allow",
			[]curation.Rule{allowRule(goMod1, "*"), blockRule(goMod1, "v1.3.0")},
			goGateBase + "v1.3.0.info",
			http.StatusForbidden,
		},
		{"a pattern covers the module", []curation.Rule{allowRule("example.com/*", "*")}, goGateBase + "v1.3.0.info", http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := goCurationEnv(t)
			e.useCuration(curation.ModeAllowlist, c.rules...)

			if w := e.get(c.path); w.Code != c.want {
				t.Errorf("status %d, want %d (%s)", w.Code, c.want, w.Body.String())
			}
		})
	}
}

func TestGoCuration_AllowlistLeavesNoLatestForAModuleNotListed(t *testing.T) {
	e := goCurationEnv(t)
	e.useCuration(curation.ModeAllowlist, allowRule("example.org/other", "*"))

	if w := e.get(goGateBase + "list"); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "" {
		t.Errorf("list: status %d, body %q; want an empty list", w.Code, w.Body.String())
	}

	if w := e.get(goEscMod1 + "/@latest"); w.Code != http.StatusNotFound {
		t.Errorf("@latest: status %d, want 404", w.Code)
	}
}

func TestGoCuration_AllowRuleExemptsAYoungVersionFromTheMinimumAge(t *testing.T) {
	e := goCurationEnv(t)
	e.repo.MinAgeHours = 24
	e.setInfo("v1.3.0", time.Now().Add(-time.Hour))

	e.useCuration(curation.ModeDenylist)

	if w := e.get(goGateBase + "v1.3.0.info"); w.Code != http.StatusForbidden {
		t.Fatalf("young version without a rule: status %d, want 403", w.Code)
	}

	e.repo.CurationMode = curation.ModeDenylist
	e.svc.WithCuration(&fakeRuleSource{rules: []curation.Rule{allowRule(goMod1, "v1.3.0")}})

	for _, ext := range []string{".info", ".mod"} {
		if w := e.get(goGateBase + "v1.3.0" + ext); w.Code != http.StatusOK {
			t.Errorf("v1.3.0%s with an allow rule: status %d, want 200 (%s)", ext, w.Code, w.Body.String())
		}
	}
}

func TestGoCuration_RuleChangeChangesTheListValidator(t *testing.T) {
	e := goCurationEnv(t)
	src := e.useCuration(curation.ModeDenylist)

	open := e.get(goGateBase + "list").Header().Get("ETag")

	src.set(blockRule(goMod1, "v1.3.0"))

	narrowed := e.get(goGateBase + "list").Header().Get("ETag")
	if open == "" || narrowed == open {
		t.Errorf("validator did not change with the rules: %q -> %q", open, narrowed)
	}

	src.set(blockRule(goMod1, "v1.3.0"))

	if again := e.get(goGateBase + "list").Header().Get("ETag"); again != narrowed {
		t.Errorf("same rules, different validator: %q vs %q", narrowed, again)
	}
}

func TestGoCuration_CachedFilesAreRefusedWhenTheRulesChange(t *testing.T) {
	e := goCurationEnv(t)

	if w := e.get(goGateBase + goVersion1 + ".zip"); w.Code != http.StatusOK {
		t.Fatalf("zip: status %d", w.Code)
	}

	src := e.useCuration(curation.ModeDenylist, blockRule(goMod1, goVersion1))

	before := len(e.up.paths)

	if w := e.get(goGateBase + goVersion1 + ".zip"); w.Code != http.StatusForbidden {
		t.Errorf("cached zip of a blocked version: status %d, want 403", w.Code)
	}

	src.set()

	if w := e.get(goGateBase + goVersion1 + ".zip"); w.Code != http.StatusOK {
		t.Errorf("cached zip once the rule is gone: status %d, want 200", w.Code)
	}

	if len(e.up.paths) != before {
		t.Error("a cached zip asked the upstream again")
	}
}

func TestGoCuration_UnreadableRulesFailClosed(t *testing.T) {
	e := goCurationEnv(t)
	e.repo.CurationMode = curation.ModeDenylist

	// No rule source: the repository's curation is on and cannot be read.
	if w := e.get(goGateBase + goVersion1 + ".info"); w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", w.Code)
	}
}

func TestGoPublished_ReadsTheCachedInfoOnly(t *testing.T) {
	e := goCurationEnv(t)
	ctx := context.Background()

	if _, ok := e.svc.PublishTime(ctx, e.repo, goMod1, goVersion1); ok {
		t.Fatal("an uncached .info dated the version")
	}

	if len(e.up.paths) != 0 {
		t.Fatalf("PublishTime reached the upstream: %v", e.up.paths)
	}

	if w := e.get(goGateBase + goVersion1 + ".info"); w.Code != http.StatusOK {
		t.Fatalf("info: status %d", w.Code)
	}

	got, ok := e.svc.PublishTime(ctx, e.repo, goMod1, goVersion1)
	if want := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("PublishTime = %v, %v; want %v", got, ok, want)
	}

	if _, ok := e.svc.PublishTime(ctx, e.repo, goMod1, "v9.9.9"); ok {
		t.Error("an unknown version was dated")
	}
}
