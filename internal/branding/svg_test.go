// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"errors"
	"strings"
	"testing"
)

const svgHead = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10">`

func TestSanitizeSVG_Strips(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		absent  []string // must not appear in the output
		present []string // must appear in the output
	}{
		{
			name:    "script element",
			in:      svgHead + `<script>alert(1)</script><circle cx="5" cy="5" r="4" fill="#f00"/></svg>`,
			absent:  []string{"script", "alert"},
			present: []string{`<circle cx="5"`, `fill="#f00"`},
		},
		{
			name:   "script inside group",
			in:     svgHead + `<g><script type="text/javascript">document.cookie</script><path d="M0 0L5 5"/></g></svg>`,
			absent: []string{"script", "cookie"}, present: []string{`<path d="M0 0L5 5"`},
		},
		{
			name:   "event handlers",
			in:     svgHead + `<rect width="5" height="5" onclick="alert(1)" onload="x()" onmouseover="y()"/></svg>`,
			absent: []string{"onclick", "onload", "onmouseover", "alert"}, present: []string{`<rect width="5"`},
		},
		{
			name:   "onload on root",
			in:     `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><path d="M0 0"/></svg>`,
			absent: []string{"onload", "alert"}, present: []string{"<path"},
		},
		{
			name:   "foreignObject with html",
			in:     svgHead + `<foreignObject width="10" height="10"><body xmlns="http://www.w3.org/1999/xhtml"><iframe src="javascript:alert(1)"/></body></foreignObject><path d="M1 1"/></svg>`,
			absent: []string{"foreignObject", "iframe", "javascript", "xhtml"}, present: []string{`<path d="M1 1"`},
		},
		{
			name:   "external image",
			in:     svgHead + `<image href="https://evil.example/x.png" width="5" height="5"/><path d="M1 1"/></svg>`,
			absent: []string{"image", "evil.example"}, present: []string{"<path"},
		},
		{
			name:   "anchor with javascript href",
			in:     svgHead + `<a xlink:href="javascript:alert(1)"><text>x</text></a></svg>`,
			absent: []string{"<a", "javascript"},
		},
		{
			name:   "external use href is dropped",
			in:     svgHead + `<use xlink:href="https://evil.example/s.svg#a"/><use href="#local"/></svg>`,
			absent: []string{"evil.example"}, present: []string{`href="#local"`},
		},
		{
			name:   "animate set javascript",
			in:     svgHead + `<a><animate attributeName="href" values="javascript:alert(1)"/></a><set attributeName="onmouseover" to="alert(1)"/><path d="M1 1"/></svg>`,
			absent: []string{"animate", "<set", "javascript", "alert"}, present: []string{"<path"},
		},
		{
			name:   "unknown attribute and namespace are dropped",
			in:     svgHead + `<path d="M1 1" data-x="1" xlink:title="t" inkscape:label="x" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape"/></svg>`,
			absent: []string{"data-x", "inkscape", "xlink:title"}, present: []string{`d="M1 1"`},
		},
		{
			name:   "foreign namespace element",
			in:     svgHead + `<x:evil xmlns:x="urn:evil"><path d="M9 9"/></x:evil><path d="M1 1"/></svg>`,
			absent: []string{"evil", "M9 9"}, present: []string{`d="M1 1"`},
		},
		{
			name:   "comments dropped",
			in:     svgHead + `<!-- <script>alert(1)</script> --><path d="M1 1"/></svg>`,
			absent: []string{"<!--", "alert"},
		},
		{
			name:   "style with local url is kept",
			in:     svgHead + `<defs><linearGradient id="g"><stop offset="0" stop-color="#fff"/></linearGradient></defs><style>.a{fill:url(#g);stroke:#000}</style><path class="a" d="M1 1"/></svg>`,
			absent: nil, present: []string{"fill:url(#g)", `class="a"`, "<linearGradient"},
		},
		{
			name:   "xml declaration and bom are accepted",
			in:     "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" + svgHead + `<path d="M1 1"/></svg>`,
			absent: []string{"<?xml"}, present: []string{"<path"},
		},
		{
			name:   "prefixed svg elements are normalised",
			in:     `<s:svg xmlns:s="http://www.w3.org/2000/svg"><s:path d="M1 1"/></s:svg>`,
			absent: []string{"<s:", "</s:"}, present: []string{`<path d="M1 1"`, `xmlns="http://www.w3.org/2000/svg"`},
		},
		{
			name:   "text content is escaped",
			in:     svgHead + `<text>&lt;script&gt;alert(1)&lt;/script&gt;</text></svg>`,
			absent: []string{"<script"}, present: []string{"&lt;script&gt;"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := sanitizeSVG([]byte(tt.in))
			if err != nil {
				t.Fatalf("sanitizeSVG: %v", err)
			}

			got := string(out)
			if !strings.HasPrefix(got, `<svg xmlns="http://www.w3.org/2000/svg"`) {
				t.Errorf("output must start with the svg root, got %.80s", got)
			}

			for _, s := range tt.absent {
				if strings.Contains(strings.ToLower(got), strings.ToLower(s)) {
					t.Errorf("output still contains %q:\n%s", s, got)
				}
			}

			for _, s := range tt.present {
				if !strings.Contains(got, s) {
					t.Errorf("output lost %q:\n%s", s, got)
				}
			}
		})
	}
}

func TestSanitizeSVG_Rejects(t *testing.T) {
	deep := strings.Repeat("<g>", maxSVGDepth+2) + strings.Repeat("</g>", maxSVGDepth+2)
	many := strings.Repeat("<path/>", maxSVGElements+1)
	uses := strings.Repeat(`<use href="#a"/>`, maxSVGUse+1)

	tests := []struct {
		name string
		in   string
	}{
		{"not xml", `GIF89a not an svg`},
		{"html document", `<html><body><script>alert(1)</script></body></html>`},
		{"wrong root", `<div xmlns="http://www.w3.org/2000/svg"/>`},
		{"svg without namespace", `<svg><path d="M1 1"/></svg>`},
		{"wrong namespace", `<svg xmlns="urn:other"><path/></svg>`},
		{"truncated", svgHead + `<path d="M1 1"/>`},
		{"doctype with entity bomb", `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY a "aaaa"><!ENTITY b "&a;&a;&a;&a;">]>` + svgHead + `<text>&b;</text></svg>`},
		{"external doctype", `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://evil.example/x.dtd">` + svgHead + `</svg>`},
		{"undefined entity", svgHead + `<text>&xxe;</text></svg>`},
		{"stylesheet processing instruction", `<?xml-stylesheet href="http://evil.example/a.css"?>` + svgHead + `</svg>`},
		{"two roots", svgHead + `</svg>` + svgHead + `</svg>`},
		{"text after root", svgHead + `</svg>trailing`},
		{"style import", svgHead + `<style>@import url(https://evil.example/a.css);</style></svg>`},
		{"style external url", svgHead + `<style>.a{fill:url(https://evil.example/a.svg#x)}</style></svg>`},
		{"style expression", svgHead + `<style>.a{width:expression(alert(1))}</style></svg>`},
		{"style escape", svgHead + `<style>.a{background:\75rl(//evil.example)}</style></svg>`},
		{"style attribute url", svgHead + `<rect style="fill:url('//evil.example/x')"/></svg>`},
		{"style attribute javascript", svgHead + `<rect style="background:url(javascript:alert(1))"/></svg>`},
		{"fill url external", svgHead + `<rect fill="url(https://evil.example/x#a)"/></svg>`},
		{"javascript value", svgHead + `<text aria-label="javascript:alert(1)">x</text></svg>`},
		{"style type other than css", svgHead + `<style type="text/javascript">alert(1)</style></svg>`},
		{"too deep", svgHead + deep + `</svg>`},
		{"too many elements", svgHead + many + `</svg>`},
		{"too many use", svgHead + uses + `</svg>`},
		{"nul byte", svgHead + "\x00</svg>"},
		{"invalid utf8", svgHead + "\xff\xfe</svg>"},
		{"utf-16 declared", `<?xml version="1.0" encoding="UTF-16"?>` + svgHead + `</svg>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := sanitizeSVG([]byte(tt.in))
			if err == nil {
				t.Fatalf("accepted unsafe SVG, output:\n%s", out)
			}

			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}

func TestSanitizeSVG_Idempotent(t *testing.T) {
	in := svgHead + `<g fill="#123456"><circle cx="5" cy="5" r="3"/><text>Acme &amp; Co</text></g></svg>`

	first, err := sanitizeSVG([]byte(in))
	if err != nil {
		t.Fatal(err)
	}

	second, err := sanitizeSVG(first)
	if err != nil {
		t.Fatalf("clean output was rejected: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("sanitising twice changed the output:\n%s\n%s", first, second)
	}
}
