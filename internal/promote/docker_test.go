// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"strings"
	"testing"
)

func TestDependentTagNames(t *testing.T) {
	hex := strings.Repeat("ab", 32)

	tests := []struct {
		name   string
		digest string
		want   string
	}{
		{"valid digest lists every dependent tag", "sha256:" + hex, "sha256-" + hex},
		{"malformed digest has none", "sha256:xyz", ""},
		{"empty digest has none", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dependentTagNames(tt.digest)
			if tt.want != "" && !strings.Contains(got, "sha256-"+hex+".sig") {
				t.Fatalf("dependentTagNames(%q) = %q, missing .sig", tt.digest, got)
			}

			if tt.want == "" && got != "" {
				t.Fatalf("dependentTagNames(%q) = %q, want empty", tt.digest, got)
			}

			if n := strings.Count(got, ","); tt.want != "" && n != 4 {
				t.Fatalf("dependentTagNames(%q) has %d separators, want 4", tt.digest, n)
			}
		})
	}
}
