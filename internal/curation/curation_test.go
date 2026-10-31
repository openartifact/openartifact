// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package curation

import (
	"strings"
	"testing"
	"time"
)

func TestGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"left-pad", "left-pad", true},
		{"left-pad", "left-pads", false},
		{"*", "", true},
		{"*", "anything/at:all", true},
		{"@types/*", "@types/node", true},
		{"@types/*", "@babel/core", false},
		{"org.apache.*:commons-*", "org.apache.commons:commons-lang3", true},
		{"1.?.0", "1.2.0", true},
		{"1.?.0", "1.12.0", false},
		{"?", "", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"*-rc*", "2.0.0-rc.1", true},
		{"**", "x", true},
		{"a*", "A", false},
		{"é?", "éx", true},
		{"", "", true},
		{"", "x", false},
	}

	for _, tt := range tests {
		if got := Glob(tt.pattern, tt.s); got != tt.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

func TestGlobIsLinearEnough(t *testing.T) {
	t.Parallel()

	start := time.Now()

	if Glob(strings.Repeat("a*", 40)+"b", strings.Repeat("a", 4000)) {
		t.Fatal("unexpected match")
	}

	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("pathological pattern took %v", d)
	}
}

func TestNormalizePackage(t *testing.T) {
	t.Parallel()

	tests := []struct{ format, in, want string }{
		{"pypi", "Django_REST.framework", "django-rest-framework"},
		{"pypi", "a--b__c", "a-b-c"},
		{"npm", "@Scope/Name", "@scope/name"},
		{"cargo", "Serde_Json", "serde-json"},
		{"nuget", "Newtonsoft.Json", "newtonsoft.json"},
		{"go", "github.com/Foo/Bar", "github.com/Foo/Bar"},
		{"maven", "org.Apache:Commons", "org.Apache:Commons"},
		{"helm", "Nginx", "Nginx"},
	}

	for _, tt := range tests {
		if got := NormalizePackage(tt.format, tt.in); got != tt.want {
			t.Errorf("NormalizePackage(%q, %q) = %q, want %q", tt.format, tt.in, got, tt.want)
		}
	}
}

func TestValidateRule(t *testing.T) {
	t.Parallel()

	good := Rule{Action: ActionBlock, Package: "left-pad", Version: "1.*", Reason: "supply chain incident"}
	if err := ValidateRule(&good); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}

	tests := map[string]func(r *Rule){
		"action":        func(r *Rule) { r.Action = "deny" },
		"empty package": func(r *Rule) { r.Package = "  " },
		"long package":  func(r *Rule) { r.Package = strings.Repeat("a", MaxPackagePattern+1) },
		"long version":  func(r *Rule) { r.Version = strings.Repeat("1", MaxVersionPattern+1) },
		"long reason":   func(r *Rule) { r.Reason = strings.Repeat("r", MaxReason+1) },
		"space":         func(r *Rule) { r.Package = "left pad" },
		"control":       func(r *Rule) { r.Version = "1.0\n" },
		"reason ctrl":   func(r *Rule) { r.Reason = "a\x00b" },
	}

	for name, mutate := range tests {
		r := good
		mutate(&r)

		if err := ValidateRule(&r); err == nil {
			t.Errorf("%s: invalid rule accepted", name)
		}
	}
}

func rules(rs ...Rule) []Rule { return rs }

func block(pkg, version string) Rule {
	return Rule{ID: "b-" + pkg + version, Action: ActionBlock, Package: pkg, Version: version, Reason: "no"}
}

func allow(pkg, version string) Rule {
	return Rule{ID: "a-" + pkg + version, Action: ActionAllow, Package: pkg, Version: version}
}

func TestSetEvaluate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		format      string
		mode        string
		rules       []Rule
		pkg, ver    string
		hidden      bool
		exemptByID  string
		blockedByID string
	}{
		{name: "off ignores rules", format: "npm", mode: ModeOff, rules: rules(block("*", "")), pkg: "x", ver: "1"},
		{name: "denylist blocks one version", format: "npm", mode: ModeDenylist, rules: rules(block("left-pad", "1.3.0")),
			pkg: "left-pad", ver: "1.3.0", hidden: true, blockedByID: "b-left-pad1.3.0"},
		{name: "denylist keeps other version", format: "npm", mode: ModeDenylist, rules: rules(block("left-pad", "1.3.0")),
			pkg: "left-pad", ver: "1.2.0"},
		{name: "empty version pattern blocks all", format: "npm", mode: ModeDenylist, rules: rules(block("left-pad", "")),
			pkg: "left-pad", ver: "9.9.9", hidden: true, blockedByID: "b-left-pad"},
		{name: "denylist glob package", format: "npm", mode: ModeDenylist, rules: rules(block("@evil/*", "")),
			pkg: "@evil/thing", ver: "1.0.0", hidden: true, blockedByID: "b-@evil/*"},
		{name: "block beats allow in denylist", format: "npm", mode: ModeDenylist,
			rules: rules(allow("left-pad", ""), block("left-pad", "1.3.0")), pkg: "left-pad", ver: "1.3.0",
			hidden: true, blockedByID: "b-left-pad1.3.0"},
		{name: "allow exempts in denylist", format: "npm", mode: ModeDenylist, rules: rules(allow("left-pad", "1.4.*")),
			pkg: "left-pad", ver: "1.4.1", exemptByID: "a-left-pad1.4.*"},
		{name: "allowlist hides unlisted", format: "npm", mode: ModeAllowlist, rules: rules(allow("left-pad", "")),
			pkg: "lodash", ver: "4.0.0", hidden: true},
		{name: "allowlist serves listed", format: "npm", mode: ModeAllowlist, rules: rules(allow("left-pad", "")),
			pkg: "left-pad", ver: "1.0.0", exemptByID: "a-left-pad"},
		{name: "allowlist empty hides all", format: "npm", mode: ModeAllowlist, pkg: "left-pad", ver: "1", hidden: true},
		{name: "block beats allow in allowlist", format: "npm", mode: ModeAllowlist,
			rules: rules(allow("left-pad", ""), block("left-pad", "1.3.0")), pkg: "left-pad", ver: "1.3.0",
			hidden: true, blockedByID: "b-left-pad1.3.0"},
		{name: "pypi normalizes name", format: "pypi", mode: ModeDenylist, rules: rules(block("Django_Rest", "")),
			pkg: "django-rest", ver: "1", hidden: true, blockedByID: "b-Django_Rest"},
		{name: "pypi normalizes candidate", format: "pypi", mode: ModeDenylist, rules: rules(block("django-rest", "")),
			pkg: "Django.REST", ver: "1", hidden: true, blockedByID: "b-django-rest"},
		{name: "go is case sensitive", format: "go", mode: ModeDenylist, rules: rules(block("github.com/Foo/*", "")),
			pkg: "github.com/foo/bar", ver: "v1.0.0"},
		{name: "go version keeps v", format: "go", mode: ModeDenylist, rules: rules(block("github.com/foo/bar", "v1.*")),
			pkg: "github.com/foo/bar", ver: "v1.2.3", hidden: true, blockedByID: "b-github.com/foo/barv1.*"},
		{
			name:        "maven coordinates",
			format:      "maven",
			mode:        ModeDenylist,
			rules:       rules(block("org.apache.logging.log4j:log4j-core", "2.14.*")),
			pkg:         "org.apache.logging.log4j:log4j-core",
			ver:         "2.14.1",
			hidden:      true,
			blockedByID: "b-org.apache.logging.log4j:log4j-core2.14.*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			v := NewSet(tt.format, tt.mode, tt.rules).Evaluate(tt.pkg, tt.ver)
			if v.Hidden != tt.hidden {
				t.Fatalf("hidden = %v, want %v (%+v)", v.Hidden, tt.hidden, v)
			}

			if tt.blockedByID != "" && (v.Rule == nil || v.Rule.ID != tt.blockedByID) {
				t.Errorf("blocking rule = %+v, want %s", v.Rule, tt.blockedByID)
			}

			if tt.exemptByID != "" && (!v.ExemptMinAge || v.AllowRule == nil || v.AllowRule.ID != tt.exemptByID) {
				t.Errorf("exemption = %+v, want rule %s", v, tt.exemptByID)
			}

			if tt.exemptByID == "" && v.ExemptMinAge {
				t.Errorf("unexpected exemption: %+v", v)
			}
		})
	}
}

func TestGateMinAgeAndCuration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour)
	old := now.Add(-100 * time.Hour)

	t.Run("min age only", func(t *testing.T) {
		t.Parallel()

		g := NewGate("npm", ModeOff, nil, 48, now)
		if hidden, h := g.Hidden("p", "1", fresh); !hidden || h.Source != SourceMinAge || !h.Until.Equal(fresh.Add(48*time.Hour)) {
			t.Errorf("fresh version: hidden=%v hold=%+v", hidden, h)
		}

		if hidden, _ := g.Hidden("p", "1", old); hidden {
			t.Error("old version hidden")
		}

		if hidden, _ := g.Hidden("p", "1", time.Time{}); hidden {
			t.Error("undated version hidden")
		}
	})

	t.Run("allow exempts the minimum age", func(t *testing.T) {
		t.Parallel()

		g := NewGate("npm", ModeDenylist, rules(allow("urgent", "")), 48, now)
		if hidden, _ := g.Hidden("urgent", "1", fresh); hidden {
			t.Error("allowed fresh version hidden by the minimum age")
		}

		if hidden, _ := g.Hidden("other", "1", fresh); !hidden {
			t.Error("fresh version without allow rule not hidden")
		}
	})

	t.Run("allow rules are inert in mode off", func(t *testing.T) {
		t.Parallel()

		g := NewGate("npm", ModeOff, rules(allow("urgent", "")), 48, now)
		if hidden, _ := g.Hidden("urgent", "1", fresh); !hidden {
			t.Error("mode off must not exempt")
		}
	})

	t.Run("block wins over exemption and reports a curation hold", func(t *testing.T) {
		t.Parallel()

		g := NewGate("npm", ModeDenylist, rules(allow("p", ""), block("p", "1")), 48, now)

		hidden, h := g.Hidden("p", "1", old)
		if !hidden || h.Source != SourceCuration || h.Rule == nil || !strings.Contains(h.Detail, "no") {
			t.Errorf("hidden=%v hold=%+v", hidden, h)
		}
	})

	t.Run("allowlist names the allowlist", func(t *testing.T) {
		t.Parallel()

		g := NewGate("npm", ModeAllowlist, rules(allow("p", "")), 0, now)

		hidden, h := g.Hidden("q", "1", time.Time{})
		if !hidden || h.Rule != nil || !strings.Contains(h.Detail, "allowlist") {
			t.Errorf("hidden=%v hold=%+v", hidden, h)
		}
	})
}

func TestGateStamp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	if got := NewGate("npm", ModeOff, nil, 0, now).Stamp(); got != "" {
		t.Errorf("inactive gate stamp = %q, want empty", got)
	}

	if NewGate("npm", ModeOff, nil, 0, now).Active() {
		t.Error("inactive gate reports Active")
	}

	age := NewGate("npm", ModeOff, nil, 24, now)
	if age.Stamp() == NewGate("npm", ModeOff, nil, 48, now).Stamp() {
		t.Error("stamp ignores the threshold")
	}

	if age.Stamp() != NewGate("npm", ModeOff, nil, 24, now.Add(StampBucket-time.Second)).Stamp() {
		t.Error("stamp changes inside a bucket")
	}

	if age.Stamp() == NewGate("npm", ModeOff, nil, 24, now.Add(StampBucket)).Stamp() {
		t.Error("stamp stays across buckets")
	}

	a := NewGate("npm", ModeDenylist, rules(block("p", "")), 0, now)
	b := NewGate("npm", ModeDenylist, rules(block("q", "")), 0, now)
	c := NewGate("npm", ModeAllowlist, rules(block("p", "")), 0, now)

	if a.Stamp() == "" || a.Stamp() == b.Stamp() || a.Stamp() == c.Stamp() {
		t.Errorf("curation stamps do not tell rule sets apart: %q %q %q", a.Stamp(), b.Stamp(), c.Stamp())
	}

	if a.Stamp() != NewGate("npm", ModeDenylist, rules(block("p", "")), 0, now.Add(time.Hour)).Stamp() {
		t.Error("curation-only stamp depends on time")
	}
}
