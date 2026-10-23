// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import "testing"

func TestParseNPMKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key      string
		fullName string
		rest     string
		isTag    bool
		ok       bool
	}{
		{"left-pad@1.3.0", "left-pad", "1.3.0", false, true},
		{"@scope/name@1.0.0-beta.1+build.5", "@scope/name", "1.0.0-beta.1+build.5", false, true},
		{"left-pad#latest", "left-pad", "latest", true, true},
		{"@scope/name#next", "@scope/name", "next", true, true},
		{"@scope/name", "@scope", "", false, false},
		{"name@", "name", "", false, false},
		{"@1.0.0", "", "1.0.0", false, false},
		{"#latest", "", "latest", true, false},
		{"name#", "name", "", true, false},
		{"", "", "", false, false},
	}

	for _, tt := range tests {
		fullName, rest, isTag, ok := parseNPMKey(tt.key)
		if ok != tt.ok {
			t.Errorf("parseNPMKey(%q) ok = %v, want %v", tt.key, ok, tt.ok)

			continue
		}

		if !ok {
			continue
		}

		if fullName != tt.fullName || rest != tt.rest || isTag != tt.isTag {
			t.Errorf("parseNPMKey(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.key, fullName, rest, isTag, tt.fullName, tt.rest, tt.isTag)
		}
	}
}

func TestSplitNPMName(t *testing.T) {
	t.Parallel()

	tests := []struct{ full, scope, name string }{
		{"left-pad", "", "left-pad"},
		{"@scope/name", "@scope", "name"},
		{"@weird", "", "@weird"},
	}

	for _, tt := range tests {
		scope, name := splitNPMName(tt.full)
		if scope != tt.scope || name != tt.name {
			t.Errorf("splitNPMName(%q) = (%q, %q), want (%q, %q)", tt.full, scope, name, tt.scope, tt.name)
		}
	}
}

func TestNPMCodecRegistered(t *testing.T) {
	t.Parallel()

	c, err := CodecFor("npm")
	if err != nil {
		t.Fatalf("CodecFor(npm): %v", err)
	}

	if c.Format() != "npm" {
		t.Fatalf("Format() = %q", c.Format())
	}
}
