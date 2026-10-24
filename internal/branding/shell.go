// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"bytes"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	manifestDescription = "Self-hosted multi-format artifact repository"
	maxShortNameRunes   = 12
)

var (
	titleRe      = regexp.MustCompile(`<title>[^<]*</title>`)
	appleTitleRe = regexp.MustCompile(`<meta\s+name="apple-mobile-web-app-title"[^>]*>`)
	iconLinkRe   = regexp.MustCompile(`<link\s+rel="icon"[^>]*>`)
	touchIconRe  = regexp.MustCompile(`<link\s+rel="apple-touch-icon"[^>]*>`)
)

// PatchIndex returns the SPA shell (index.html) with the effective branding
// applied before the first paint: title, home-screen title, favicon, the
// primary-colour custom properties of both themes and the branding payload
// (#oa-branding) the app reads synchronously at boot. Every inserted value is
// escaped for its context.
func PatchIndex(index []byte, s *Snapshot) []byte {
	p := s.Public
	out := index

	out = titleRe.ReplaceAllLiteral(out, []byte("<title>"+html.EscapeString(p.ProductName)+"</title>"))
	out = appleTitleRe.ReplaceAllLiteral(out,
		[]byte(`<meta name="apple-mobile-web-app-title" content="`+html.EscapeString(p.ProductName)+`" />`))

	if fav, ok := s.Images[Favicon]; ok {
		out = replaceIcons(out, p.FaviconURL, fav)
	}

	var head bytes.Buffer

	if p.Theme != nil {
		head.WriteString(`<style id="oa-brand-style">` + themeCSS(*p.Theme) + `</style>` + "\n")
	}

	payload, _ := json.Marshal(p) //nolint:errchkjson // plain strings and structs cannot fail to encode

	// json.Marshal escapes <, > and & as \u003c etc., so the payload cannot
	// close the script element.
	head.WriteString(`<script type="application/json" id="oa-branding">` + string(payload) + `</script>` + "\n")

	if i := bytes.Index(out, []byte("</head>")); i >= 0 {
		patched := make([]byte, 0, len(out)+head.Len())
		patched = append(patched, out[:i]...)
		patched = append(patched, head.Bytes()...)
		patched = append(patched, out[i:]...)

		return patched
	}

	return out
}

// replaceIcons swaps the shell's icon links for the custom favicon: the first
// <link rel="icon"> becomes the custom one, further ones are removed, and the
// apple-touch-icon follows the favicon when it is a PNG (iOS cannot use SVG)
// and is dropped otherwise rather than showing another brand's icon.
func replaceIcons(out []byte, url string, fav *Image) []byte {
	link := `<link rel="icon" type="` + fav.ContentType + `"`
	if fav.Width > 0 {
		link += ` sizes="` + strconv.Itoa(fav.Width) + `x` + strconv.Itoa(fav.Height) + `"`
	}

	link += ` href="` + html.EscapeString(url) + `" />`

	first := true
	out = iconLinkRe.ReplaceAllFunc(out, func([]byte) []byte {
		if first {
			first = false
			return []byte(link)
		}

		return nil
	})

	touch := ""
	if fav.ContentType == ContentTypePNG {
		touch = `<link rel="apple-touch-icon" href="` + html.EscapeString(url) + `" />`
	}

	return touchIconRe.ReplaceAllLiteral(out, []byte(touch))
}

func themeCSS(t Theme) string {
	block := func(sel string, p Palette) string {
		pr, ct, so := p.cssTriples()

		return sel + "{--color-primary:" + pr + ";--color-primary-contrast:" + ct + ";--color-primary-soft:" + so + "}"
	}

	return block(":root", t.Light) + block(".dark", t.Dark)
}

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

type webManifest struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	Description     string         `json:"description"`
	StartURL        string         `json:"start_url"`
	Scope           string         `json:"scope"`
	Display         string         `json:"display"`
	ThemeColor      string         `json:"theme_color"`
	BackgroundColor string         `json:"background_color"`
	Icons           []manifestIcon `json:"icons"`
}

// Manifest renders the PWA manifest with the branded name and, when a custom
// favicon is set, the favicon as the install icon. PNG favicons smaller than
// 192 px may not satisfy a browser's installability check; use an SVG or a
// 512 px PNG.
func Manifest(s *Snapshot) []byte {
	p := s.Public

	icons := []manifestIcon{
		{"/icons/icon-192.png", "192x192", ContentTypePNG, "any"},
		{"/icons/icon-512.png", "512x512", ContentTypePNG, "any"},
		{"/icons/icon-maskable-512.png", "512x512", ContentTypePNG, "maskable"},
	}

	if fav, ok := s.Images[Favicon]; ok {
		sizes := "any"
		if fav.Width > 0 {
			sizes = strconv.Itoa(fav.Width) + "x" + strconv.Itoa(fav.Height)
		}

		icons = []manifestIcon{{p.FaviconURL, sizes, fav.ContentType, "any"}}
	}

	b, _ := json.MarshalIndent(webManifest{ //nolint:errchkjson // plain strings cannot fail to encode
		ID:              "/",
		Name:            p.ProductName,
		ShortName:       shortName(p.ProductName),
		Description:     manifestDescription,
		StartURL:        "/",
		Scope:           "/",
		Display:         "standalone",
		ThemeColor:      "#ffffff",
		BackgroundColor: "#f9fafb",
		Icons:           icons,
	}, "", "  ")

	return append(b, '\n')
}

func shortName(name string) string {
	if utf8.RuneCountInString(name) <= maxShortNameRunes {
		return name
	}

	return strings.TrimSpace(string([]rune(name)[:maxShortNameRunes]))
}
