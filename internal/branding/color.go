// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"fmt"
	"math"
	"strconv"
)

// rgb is an sRGB colour with 0-255 channels.
type rgb struct{ r, g, b float64 }

// Surfaces of the two themes (web/src/style.css): the page background and the
// card surface. Primary text must hold WCAG AA (4.5:1) on both, and so must
// the "soft" tinted fill that active navigation uses.
//
//nolint:gochecknoglobals // immutable colour constants (structs cannot be const)
var (
	lightBg      = rgb{249, 250, 251}
	lightSurface = rgb{255, 255, 255}
	darkBg       = rgb{9, 11, 19}
	darkSurface  = rgb{17, 20, 32}
	darkText     = rgb{17, 24, 39} // --color-foreground (light), text on a light fill
	whiteText    = rgb{255, 255, 255}
)

const minContrast = 4.5

// Palette is the contrast-adjusted colour set of one theme, derived from the
// configured primary colour.
type Palette struct {
	Primary         string `json:"primary"`
	PrimaryContrast string `json:"primary_contrast"`
	PrimarySoft     string `json:"primary_soft"`
}

// Theme holds the palettes of both themes.
type Theme struct {
	Light Palette `json:"light"`
	Dark  Palette `json:"dark"`
}

func parseHex(s string) (rgb, bool) {
	if !colorRe.MatchString(s) {
		return rgb{}, false
	}

	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return rgb{}, false
	}

	return rgb{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}, true
}

func (c rgb) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", clamp8(c.r), clamp8(c.g), clamp8(c.b))
}

// triple renders the colour the way the CSS custom properties hold it
// ("79 70 229").
func (c rgb) triple() string {
	return fmt.Sprintf("%d %d %d", clamp8(c.r), clamp8(c.g), clamp8(c.b))
}

func clamp8(v float64) int {
	return int(math.Round(math.Min(255, math.Max(0, v))))
}

func linear(v float64) float64 {
	v /= 255
	if v <= 0.04045 {
		return v / 12.92
	}

	return math.Pow((v+0.055)/1.055, 2.4)
}

func luminance(c rgb) float64 {
	return 0.2126*linear(c.r) + 0.7152*linear(c.g) + 0.0722*linear(c.b)
}

// contrast is the WCAG contrast ratio of two colours.
func contrast(a, b rgb) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}

	return (la + 0.05) / (lb + 0.05)
}

// mix blends c towards target by t (0 keeps c, 1 yields target) and rounds to
// 8-bit channels, so the contrast checked is the contrast of the emitted colour.
func mix(c, target rgb, t float64) rgb {
	return rgb{
		math.Round(c.r + (target.r-c.r)*t),
		math.Round(c.g + (target.g-c.g)*t),
		math.Round(c.b + (target.b-c.b)*t),
	}
}

func readableOn(c rgb) rgb {
	if contrast(c, whiteText) >= contrast(c, darkText) {
		return whiteText
	}

	return darkText
}

// adjust moves primary towards target in 2% steps until it reads at least
// minContrast on every surface in against, and returns the first such colour.
// Mixing keeps the hue of the brand colour; a colour that already passes is
// returned unchanged.
func adjust(primary, target rgb, against func(rgb) float64) rgb {
	for i := range 51 {
		c := mix(primary, target, float64(i)*0.02)
		if against(c) >= minContrast {
			return c
		}
	}

	return target
}

func palette(primary, towards, bg, surface, softBase rgb, softWeight float64) Palette {
	soft := func(c rgb) rgb { return mix(softBase, c, softWeight) }

	p := adjust(primary, towards, func(c rgb) float64 {
		return math.Min(math.Min(contrast(c, bg), contrast(c, surface)), contrast(c, soft(c)))
	})

	return Palette{
		Primary:         p.hex(),
		PrimaryContrast: readableOn(p).hex(),
		PrimarySoft:     soft(p).hex(),
	}
}

// DeriveTheme computes the palettes for a #rrggbb primary colour: in the light
// theme the colour is darkened, in the dark theme lightened, only as far as
// needed for 4.5:1 text contrast on the page, the card surface and the soft
// fill. It returns false for anything that is not #rrggbb.
func DeriveTheme(primary string) (Theme, bool) {
	c, ok := parseHex(primary)
	if !ok {
		return Theme{}, false
	}

	return Theme{
		Light: palette(c, rgb{0, 0, 0}, lightBg, lightSurface, rgb{255, 255, 255}, 0.12),
		Dark:  palette(c, rgb{255, 255, 255}, darkBg, darkSurface, darkSurface, 0.22),
	}, true
}

// cssTriples renders the palette as the space-separated RGB triples of the
// --color-primary* custom properties.
func (p Palette) cssTriples() (string, string, string) {
	pr, _ := parseHex(p.Primary)
	ct, _ := parseHex(p.PrimaryContrast)
	so, _ := parseHex(p.PrimarySoft)

	return pr.triple(), ct.triple(), so.triple()
}
