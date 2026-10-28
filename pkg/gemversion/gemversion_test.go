// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gemversion

import "testing"

func TestCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want int
	}{
		{"10.0", "9.0", 1},
		{"9.0", "10.0", -1},
		{"1.0.a", "1.0", -1},
		{"1.0", "1.0.0", 0},
		{"1.0.a", "1.0.b", -1},
		{"1.2.3", "1.2.3", 0},
	}

	for _, tt := range tests {
		if got := Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
