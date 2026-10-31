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
	"github.com/openartifact/openartifact/pkg/compactindex"
)

// The server of rubygems_minage_test.go serves lib 1.0.0, 1.1.0, 2.0.0,
// 2.0.0-java and 3.0.0 (info file only), fresh 1.0.0 and plain 1.0.0 (no API).

func TestRubyGemsCuration_DenylistHidesAVersionAndItsPlatforms(t *testing.T) {
	f, g := gemAgeFixture(t, 0)
	f.useCuration(curation.ModeDenylist, blockRule("lib", "2.*"))

	w := gemDo(f, http.MethodGet, "info/lib", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if got := strings.Join(gemInfoLines(w.Body.String()), " "); got != "1.0.0 1.1.0 3.0.0" {
		t.Errorf("versions = %q, want %q", got, "1.0.0 1.1.0 3.0.0")
	}

	body := w.Body.String()
	if want := `"` + compactindex.InfoMD5([]byte(body)) + `"`; w.Header().Get("ETag") != want {
		t.Errorf("ETag = %q, want the MD5 of the file served %q", w.Header().Get("ETag"), want)
	}

	for _, file := range []string{"gems/lib-2.0.0.gem", "gems/lib-2.0.0-java.gem"} {
		r := gemDo(f, http.MethodGet, file, nil)
		if r.Code != http.StatusForbidden || !strings.Contains(r.Body.String(), "curation") {
			t.Errorf("GET %s = %d %q, want 403 naming curation", file, r.Code, r.Body.String())
		}
	}

	if r := gemDo(f, http.MethodGet, "quick/Marshal.4.8/lib-2.0.0.gemspec.rz", nil); r.Code != http.StatusForbidden {
		t.Errorf("gemspec of a blocked version = %d, want 403", r.Code)
	}

	if g.gemGets.Load() != 0 || g.specGet.Load() != 0 {
		t.Errorf("the upstream was asked for a blocked file: %d gems, %d gemspecs", g.gemGets.Load(), g.specGet.Load())
	}

	if r := gemDo(f, http.MethodGet, "gems/lib-1.0.0.gem", nil); r.Code != http.StatusOK {
		t.Errorf("an unblocked gem = %d, want 200", r.Code)
	}

	if r := gemDo(f, http.MethodGet, "quick/Marshal.4.8/lib-1.0.0.gemspec.rz", nil); r.Code != http.StatusOK {
		t.Errorf("an unblocked gemspec = %d, want 200", r.Code)
	}
}

func TestRubyGemsCuration_NeedsNoPublishDates(t *testing.T) {
	f, g := gemAgeFixture(t, 0)
	f.useCuration(curation.ModeDenylist, blockRule("lib", "1.1.0"))

	// An upstream without the versions API still has its versions filtered.
	g.apiCode.Store(http.StatusInternalServerError)

	w := gemDo(f, http.MethodGet, "info/lib", nil)
	if w.Code != http.StatusOK || strings.Contains(strings.Join(gemInfoLines(w.Body.String()), " "), "1.1.0") {
		t.Errorf("info = %d %q, want 200 without 1.1.0", w.Code, w.Body.String())
	}

	if r := gemDo(f, http.MethodGet, "gems/lib-1.1.0.gem", nil); r.Code != http.StatusForbidden {
		t.Errorf("blocked gem = %d, want 403", r.Code)
	}

	if r := gemDo(f, http.MethodGet, "gems/lib-1.0.0.gem", nil); r.Code != http.StatusOK {
		t.Errorf("unblocked gem = %d, want 200", r.Code)
	}
}

func TestRubyGemsCuration_Allowlist(t *testing.T) {
	cases := []struct {
		name  string
		rules []curation.Rule
		path  string
		want  int
	}{
		{"an allowed gem", []curation.Rule{allowRule("lib", "*")}, "gems/lib-1.0.0.gem", http.StatusOK},
		{"a version not allowed", []curation.Rule{allowRule("lib", "1.0.0")}, "gems/lib-1.1.0.gem", http.StatusForbidden},
		{"another gem is not allowed", []curation.Rule{allowRule("lib", "*")}, "gems/plain-1.0.0.gem", http.StatusForbidden},
		{"another gem has no info", []curation.Rule{allowRule("lib", "*")}, "info/plain", http.StatusNotFound},
		{
			"block beats allow",
			[]curation.Rule{allowRule("lib", "*"), blockRule("lib", "1.0.0")},
			"gems/lib-1.0.0.gem",
			http.StatusForbidden,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := gemAgeFixture(t, 0)
			f.useCuration(curation.ModeAllowlist, c.rules...)

			if w := gemDo(f, http.MethodGet, c.path, nil); w.Code != c.want {
				t.Errorf("GET %s = %d, want %d (%s)", c.path, w.Code, c.want, w.Body.String())
			}
		})
	}
}

func TestRubyGemsCuration_AllowRuleExemptsAYoungVersionFromTheMinimumAge(t *testing.T) {
	f, _ := gemAgeFixture(t, gemAgeGate)
	f.useCuration(curation.ModeDenylist, allowRule("lib", "2.0.0"))

	w := gemDo(f, http.MethodGet, "info/lib", nil)
	if got := strings.Join(gemInfoLines(w.Body.String()), " "); got != "1.0.0 1.1.0 2.0.0 2.0.0-java" {
		t.Errorf("versions = %q, want the exempt 2.0.0 (and its platform) to stay", got)
	}

	if r := gemDo(f, http.MethodGet, "gems/lib-2.0.0.gem", nil); r.Code != http.StatusOK {
		t.Errorf("exempt young gem = %d, want 200", r.Code)
	}

	if r := gemDo(f, http.MethodGet, "gems/lib-3.0.0.gem", nil); r.Code != http.StatusForbidden {
		t.Errorf("young gem without a rule = %d, want 403", r.Code)
	}
}

func TestRubyGemsCuration_RuleChangeChangesTheServedFile(t *testing.T) {
	f, _ := gemAgeFixture(t, 0)
	src := f.useCuration(curation.ModeDenylist)

	open := gemDo(f, http.MethodGet, "info/lib", nil)

	src.set(blockRule("lib", "3.0.0"))

	narrowed := gemDo(f, http.MethodGet, "info/lib", nil)
	if narrowed.Header().Get("ETag") == open.Header().Get("ETag") || strings.Contains(narrowed.Body.String(), "3.0.0") {
		t.Errorf("the rule change did not show: ETag %q -> %q", open.Header().Get("ETag"), narrowed.Header().Get("ETag"))
	}

	f.repo.CurationMode = curation.ModeOff

	if off := gemDo(f, http.MethodGet, "info/lib", nil); off.Header().Get("ETag") != open.Header().Get("ETag") {
		t.Errorf("curation off: ETag %q differs from the ungated %q", off.Header().Get("ETag"), open.Header().Get("ETag"))
	}
}

func TestRubyGemsCuration_CachedGemIsRefusedWhenTheRulesChange(t *testing.T) {
	f, _ := gemAgeFixture(t, 0)

	if w := gemDo(f, http.MethodGet, "gems/lib-1.0.0.gem", nil); w.Code != http.StatusOK {
		t.Fatalf("fetch = %d", w.Code)
	}

	f.useCuration(curation.ModeDenylist, blockRule("lib", "1.0.0"))

	if w := gemDo(f, http.MethodGet, "gems/lib-1.0.0.gem", nil); w.Code != http.StatusForbidden {
		t.Errorf("cached gem of a blocked version = %d, want 403", w.Code)
	}
}

func TestRubyGemsCuration_UnreadableRulesFailClosed(t *testing.T) {
	f, _ := gemAgeFixture(t, 0)
	f.repo.CurationMode = curation.ModeDenylist

	for _, p := range []string{"info/lib", "gems/lib-1.0.0.gem", "quick/Marshal.4.8/lib-1.0.0.gemspec.rz"} {
		if w := gemDo(f, http.MethodGet, p, nil); w.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", p, w.Code)
		}
	}
}

func TestRubyGemsPublished_ReadsTheCachedAPIOnly(t *testing.T) {
	f, _ := gemAgeFixture(t, gemAgeGate)
	ctx := context.Background()

	if _, ok := f.svc.PublishTime(ctx, f.repo, "lib", "1.1.0"); ok {
		t.Fatal("an uncached API document dated the version")
	}

	if f.hits.Load() != 0 {
		t.Fatalf("PublishTime reached the upstream: %d requests", f.hits.Load())
	}

	if w := gemDo(f, http.MethodGet, "info/lib", nil); w.Code != http.StatusOK {
		t.Fatalf("info = %d", w.Code)
	}

	got, ok := f.svc.PublishTime(ctx, f.repo, "lib", "1.1.0")
	if want := time.Now().Add(-72 * time.Hour); !ok || got.Sub(want).Abs() > time.Minute {
		t.Errorf("PublishTime = %v, %v; want about %v", got, ok, want)
	}

	if _, ok := f.svc.PublishTime(ctx, f.repo, "lib", "3.0.0"); ok {
		t.Error("a version the API does not list was dated")
	}
}
