// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"fmt"
	"testing"
)

func TestDeriveTheme_Contrast(t *testing.T) {
	colours := []string{
		"#4f46e5", "#ffffff", "#000000", "#ffff00", "#00ff00", "#ff0000", "#0000ff",
		"#808080", "#7f7f7f", "#fde047", "#0f766e", "#f97316", "#111827", "#e5e7eb", "#ff00ff",
	}

	// Sweep the grid as well so no hue/lightness combination is missed.
	for r := 0; r < 256; r += 51 {
		for g := 0; g < 256; g += 51 {
			for b := 0; b < 256; b += 51 {
				colours = append(colours, fmt.Sprintf("#%02x%02x%02x", r, g, b))
			}
		}
	}

	for _, in := range colours {
		th, ok := DeriveTheme(in)
		if !ok {
			t.Fatalf("DeriveTheme(%s) not ok", in)
		}

		for name, c := range map[string]struct {
			p       Palette
			bg, srf rgb
		}{
			"light": {th.Light, lightBg, lightSurface},
			"dark":  {th.Dark, darkBg, darkSurface},
		} {
			pr, _ := parseHex(c.p.Primary)
			ct, _ := parseHex(c.p.PrimaryContrast)
			so, _ := parseHex(c.p.PrimarySoft)

			checks := map[string]float64{
				"primary on page":    contrast(pr, c.bg),
				"primary on surface": contrast(pr, c.srf),
				"primary on soft":    contrast(pr, so),
				"text on primary":    contrast(ct, pr),
			}

			for what, ratio := range checks {
				if ratio < minContrast-0.01 {
					t.Errorf("%s %s theme: %s = %.2f (< %.1f), palette %+v", in, name, what, ratio, minContrast, c.p)
				}
			}
		}
	}
}

func TestDeriveTheme_KeepsReadableColour(t *testing.T) {
	// A colour that already passes in the light theme is not touched there.
	th, _ := DeriveTheme("#4f46e5")
	if th.Light.Primary != "#4f46e5" {
		t.Errorf("light primary changed to %s", th.Light.Primary)
	}

	// A pale colour is darkened for light surfaces but only lightened (never
	// darkened) for dark ones.
	th, _ = DeriveTheme("#fde047")
	if th.Light.Primary == "#fde047" {
		t.Error("pale yellow must be darkened on light surfaces")
	}

	if th.Dark.Primary != "#fde047" {
		t.Errorf("pale yellow already reads on dark surfaces, got %s", th.Dark.Primary)
	}
}

func TestDeriveTheme_Invalid(t *testing.T) {
	for _, in := range []string{"", "red", "#fff", "#gggggg", "#4f46e5;"} {
		if _, ok := DeriveTheme(in); ok {
			t.Errorf("DeriveTheme(%q) accepted", in)
		}
	}
}
