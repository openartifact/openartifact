// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"encoding/json"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func rangesJSON(t *testing.T, typ string, events ...Event) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal([]Range{{Type: typ, Events: events}})
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func TestAffects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ecosystem string
		typ       string
		events    []Event
		versions  []string
		query     string
		affected  bool
		fixed     string
	}{
		{"npm below fix", "npm", "SEMVER", []Event{{Introduced: "0"}, {Fixed: "4.17.21"}}, nil, "4.17.20", true, "4.17.21"},
		{"npm at fix", "npm", "SEMVER", []Event{{Introduced: "0"}, {Fixed: "4.17.21"}}, nil, "4.17.21", false, ""},
		{"npm before introduced", "npm", "SEMVER", []Event{{Introduced: "2.0.0"}, {Fixed: "2.1.0"}}, nil, "1.9.0", false, ""},
		{"npm prerelease below fix", "npm", "SEMVER", []Event{{Introduced: "0"}, {Fixed: "1.0.0"}}, nil, "1.0.0-rc.1", true, "1.0.0"},
		{"npm two intervals second", "npm", "SEMVER",
			[]Event{{Introduced: "1.0.0"}, {Fixed: "1.2.0"}, {Introduced: "2.0.0"}, {Fixed: "2.3.0"}}, nil, "2.1.0", true, "2.3.0"},
		{"npm two intervals gap", "npm", "SEMVER",
			[]Event{{Introduced: "1.0.0"}, {Fixed: "1.2.0"}, {Introduced: "2.0.0"}, {Fixed: "2.3.0"}}, nil, "1.5.0", false, ""},
		{"npm unordered events", "npm", "SEMVER", []Event{{Fixed: "1.2.0"}, {Introduced: "0"}}, nil, "1.1.0", true, "1.2.0"},
		{"npm last affected inside", "npm", "SEMVER", []Event{{Introduced: "0"}, {LastAffected: "1.5.0"}}, nil, "1.5.0", true, ""},
		{"npm last affected above", "npm", "SEMVER", []Event{{Introduced: "0"}, {LastAffected: "1.5.0"}}, nil, "1.5.1", false, ""},
		{"npm no fix", "npm", "SEMVER", []Event{{Introduced: "1.0.0"}}, nil, "9.0.0", true, ""},
		{"npm unparseable never ranges", "npm", "SEMVER", []Event{{Introduced: "0"}}, nil, "not a version", false, ""},
		{"explicit version", "npm", "SEMVER", nil, []string{"1.2.3"}, "1.2.3", true, ""},
		{"explicit version miss", "npm", "SEMVER", nil, []string{"1.2.3"}, "1.2.4", false, ""},
		{"go strips v", "Go", "SEMVER", []Event{{Introduced: "0"}, {Fixed: "1.2.4"}}, nil, "v1.2.3", true, "1.2.4"},
		{"go pseudo version below fix", "Go", "SEMVER", []Event{{Introduced: "0"}, {Fixed: "1.0.0"}}, nil,
			"v0.0.0-20200101000000-abcdefabcdef", true, "1.0.0"},
		{"pypi post release", "PyPI", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "2.0"}}, nil, "2.0.post1", false, ""},
		{"pypi rc below", "PyPI", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "2.0"}}, nil, "2.0rc1", true, "2.0"},
		{"pypi epoch", "PyPI", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "1!1.0"}}, nil, "2.5", true, "1!1.0"},
		{"maven qualifier", "Maven", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "2.15.0"}}, nil, "2.14.1", true, "2.15.0"},
		{"maven release after rc", "Maven", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "2.15.0"}}, nil, "2.15.0", false, ""},
		{"maven rc below final", "Maven", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "2.15.0"}}, nil, "2.15.0-rc1", true, "2.15.0"},
		{"rubygems prerelease", "RubyGems", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "5.0.0"}}, nil, "5.0.0.rc1", true, "5.0.0"},
		{"rubygems fixed", "RubyGems", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "5.0.0"}}, nil, "5.0.1", false, ""},
		{"nuget four parts", "NuGet", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "1.2.3"}}, nil, "1.2.2.9", true, "1.2.3"},
		{"nuget trailing zero equal", "NuGet", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "1.2.3"}}, nil, "1.2.3.0", false, ""},
		{"crates semver ecosystem", "crates.io", "ECOSYSTEM", []Event{{Introduced: "0"}, {Fixed: "0.3.2"}}, nil, "0.3.1", true, "0.3.2"},
		{"git ranges ignored", "npm", "GIT", []Event{{Introduced: "0"}, {Fixed: "abcdef"}}, nil, "1.0.0", false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			aff := models.OSVAffected{Ecosystem: tc.ecosystem, Versions: tc.versions}
			if tc.events != nil {
				aff.Ranges = rangesJSON(t, tc.typ, tc.events...)
			}

			affected, fixed := Affects(aff, tc.query)
			if affected != tc.affected || fixed != tc.fixed {
				t.Fatalf("Affects(%q) = (%v, %q), want (%v, %q)", tc.query, affected, fixed, tc.affected, tc.fixed)
			}
		})
	}
}

func TestAffectsMalformedRangesOnlyExplicit(t *testing.T) {
	t.Parallel()

	aff := models.OSVAffected{Ecosystem: "npm", Ranges: json.RawMessage(`{not json`), Versions: []string{"1.0.0"}}

	if got, _ := Affects(aff, "1.0.0"); !got {
		t.Fatal("an explicit version must match even when the ranges are malformed")
	}

	if got, _ := Affects(aff, "1.0.1"); got {
		t.Fatal("malformed ranges must match nothing")
	}
}
