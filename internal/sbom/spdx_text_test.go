// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strings"
	"testing"
)

// TestStripTextClose: a stripped "</text>" must not complete another one.
func TestStripTextClose(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"":                           "",
		"no markup":                  "no markup",
		"a </text> b":                "a  b",
		"</te</text>xt>":             "",
		"x</te</te</text>xt>xt>y":    "xy",
		"</text></text>":             "",
		"</tex</text>":               "</tex",
		"<text> and </Text> stay":    "<text> and </Text> stay",
		"</text" + "</text>" + ">z":  "z",
		"</text></te</text>xt></te>": "</te>",
	} {
		got := stripTextClose(in)
		if got != want || strings.Contains(got, "</text>") {
			t.Errorf("stripTextClose(%q) = %q, want %q", in, got, want)
		}
	}
}
