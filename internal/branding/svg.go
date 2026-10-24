// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	svgNS   = "http://www.w3.org/2000/svg"
	xlinkNS = "http://www.w3.org/1999/xlink"
	xmlNS   = "http://www.w3.org/XML/1998/namespace"

	maxSVGDepth    = 32
	maxSVGElements = 4000
	maxSVGUse      = 100
)

// svgElements is the allowlist of elements that survive sanitising. Anything
// else (script, foreignObject, image, a, animate*, set, filter and fe*, style
// sheets in other namespaces, metadata, unknown vocabularies) is removed
// together with its subtree.
var svgElements = map[string]bool{ //nolint:gochecknoglobals // read-only allowlist
	"svg": true, "g": true, "defs": true, "symbol": true, "use": true,
	"path": true, "circle": true, "ellipse": true, "rect": true, "line": true,
	"polyline": true, "polygon": true, "text": true, "tspan": true,
	"linearGradient": true, "radialGradient": true, "stop": true,
	"clipPath": true, "mask": true, "title": true, "desc": true, "style": true,
}

// svgAttrs is the allowlist of attributes (geometry and presentation). Event
// handlers, namespaced attributes (except xlink:href and xml:space), filter
// references and everything unknown are dropped.
var svgAttrs = map[string]bool{ //nolint:gochecknoglobals // read-only allowlist
	"id": true, "class": true, "style": true, "viewBox": true, "width": true, "height": true,
	"x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true, "fx": true, "fy": true,
	"d": true, "points": true, "transform": true, "transform-origin": true,
	"fill": true, "fill-opacity": true, "fill-rule": true,
	"stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true,
	"stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true, "stroke-opacity": true,
	"opacity": true, "clip-path": true, "clip-rule": true, "mask": true, "color": true,
	"display": true, "visibility": true, "offset": true, "stop-color": true, "stop-opacity": true,
	"gradientUnits": true, "gradientTransform": true, "spreadMethod": true,
	"clipPathUnits": true, "maskUnits": true, "maskContentUnits": true,
	"preserveAspectRatio": true, "font-family": true, "font-size": true, "font-weight": true,
	"font-style": true, "text-anchor": true, "dominant-baseline": true, "letter-spacing": true,
	"dx": true, "dy": true, "version": true, "role": true, "aria-label": true,
	"aria-hidden": true, "aria-labelledby": true, "focusable": true, "vector-effect": true,
	"shape-rendering": true, "text-rendering": true, "paint-order": true, "href": true,
}

var (
	cssURLRe       = regexp.MustCompile(`url\(\s*(?:"|')?\s*([^)"']*)`)
	cssCommentRe   = regexp.MustCompile(`(?s)/\*.*?\*/`)
	errSVGUnsafe   = invalidf("the SVG contains content that cannot be made safe")
	errSVGMalform  = invalidf("the file is not a well-formed SVG")
	errSVGTooDeep  = invalidf("the SVG is too complex")
	errSVGNotSVG   = invalidf("the file is not an SVG image")
	errSVGTrailing = invalidf("the SVG has content after the root element")
)

// cssSafe reports whether a CSS fragment (a <style> body or a style attribute)
// is free of anything that can load a resource or run code: at-rules
// (@import, @font-face), escapes, expression/behaviour hooks, and url()
// references other than local fragments (url(#id)).
func cssSafe(css string) bool {
	css = strings.ToLower(cssCommentRe.ReplaceAllString(css, " "))

	if strings.ContainsAny(css, "@\\<") {
		return false
	}

	for _, bad := range []string{"expression", "javascript", "vbscript", "behavior", "binding", "image-set", "src(", "attr(", "image(", "data:"} {
		if strings.Contains(css, bad) {
			return false
		}
	}

	for _, m := range cssURLRe.FindAllStringSubmatch(css, -1) {
		if !strings.HasPrefix(strings.TrimSpace(m[1]), "#") {
			return false
		}
	}

	return true
}

// attrSafe reports whether an attribute value is free of script and external
// references. url(...) must point at a fragment of the same document.
func attrSafe(value string) bool {
	v := strings.ToLower(strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}

		return r
	}, value))

	for _, bad := range []string{"javascript:", "vbscript:", "<"} {
		if strings.Contains(v, bad) {
			return false
		}
	}

	for _, m := range cssURLRe.FindAllStringSubmatch(strings.ToLower(value), -1) {
		if !strings.HasPrefix(strings.TrimSpace(m[1]), "#") {
			return false
		}
	}

	return true
}

type svgWriter struct {
	buf      bytes.Buffer
	styleBuf strings.Builder
	inStyle  bool
}

// sanitizeSVG rewrites an SVG document from an allowlist and returns the clean
// bytes. It strips scripts, event handlers, external references and
// foreignObject (and every other element or attribute not on the allowlist),
// and returns ErrInvalid-wrapped errors when the document cannot be made safe:
// not well-formed XML, a DOCTYPE or entity declaration, processing
// instructions other than the XML declaration, a root that is not <svg> in the
// SVG namespace, unsafe CSS, or excessive depth or size.
func sanitizeSVG(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errSVGMalform
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true

	var s sanitizer

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, errSVGMalform
		}

		if err := s.token(tok); err != nil {
			return nil, err
		}
	}

	if !s.rootDone {
		return nil, errSVGMalform
	}

	return s.w.buf.Bytes(), nil
}

// sanitizer is the state of one sanitizeSVG pass.
type sanitizer struct {
	w        svgWriter
	depth    int // open allowlisted elements
	skip     int // open elements of a stripped subtree
	elements int
	uses     int
	sawRoot  bool
	rootDone bool
}

func (s *sanitizer) token(tok xml.Token) error {
	switch t := tok.(type) {
	case xml.ProcInst:
		if t.Target != "xml" || s.sawRoot {
			return errSVGUnsafe
		}
	case xml.Directive:
		return errSVGUnsafe
	case xml.StartElement:
		return s.startElement(t)
	case xml.EndElement:
		return s.endElement(t)
	case xml.CharData:
		return s.text(t)
	}

	return nil // comments are dropped
}

func (s *sanitizer) startElement(t xml.StartElement) error {
	if s.rootDone {
		return errSVGTrailing
	}

	s.elements++
	if s.elements > maxSVGElements {
		return errSVGTooDeep
	}

	if !s.sawRoot && (t.Name.Local != "svg" || t.Name.Space != svgNS) {
		return errSVGNotSVG
	}

	if s.skip > 0 {
		s.skip++
		return nil
	}

	if t.Name.Space != svgNS || !svgElements[t.Name.Local] {
		s.skip = 1
		return nil
	}

	s.depth++
	if s.depth > maxSVGDepth {
		return errSVGTooDeep
	}

	if t.Name.Local == "use" {
		s.uses++
		if s.uses > maxSVGUse {
			return errSVGTooDeep
		}
	}

	if err := s.w.start(t, !s.sawRoot); err != nil {
		return err
	}

	s.sawRoot = true

	return nil
}

func (s *sanitizer) endElement(t xml.EndElement) error {
	if s.skip > 0 {
		s.skip--
		return nil
	}

	if err := s.w.end(t.Name.Local); err != nil {
		return err
	}

	s.depth--
	if s.depth == 0 {
		s.rootDone = true
	}

	return nil
}

func (s *sanitizer) text(t xml.CharData) error {
	if s.skip > 0 {
		return nil
	}

	if blank := len(bytes.TrimSpace(t)) == 0; !s.sawRoot || s.rootDone {
		switch {
		case blank:
			return nil
		case !s.sawRoot:
			return errSVGNotSVG
		default:
			return errSVGTrailing
		}
	}

	if s.w.inStyle {
		s.w.styleBuf.Write(t)
		return nil
	}

	_ = xml.EscapeText(&s.w.buf, t)

	return nil
}

func (w *svgWriter) start(t xml.StartElement, root bool) error {
	w.buf.WriteByte('<')
	w.buf.WriteString(t.Name.Local)

	if root {
		w.buf.WriteString(` xmlns="` + svgNS + `" xmlns:xlink="` + xlinkNS + `"`)
	}

	for _, a := range t.Attr {
		name, ok := attrName(a.Name)
		if !ok {
			continue
		}

		if !attrSafe(a.Value) {
			return errSVGUnsafe
		}

		switch name {
		case "style":
			if !cssSafe(a.Value) {
				return errSVGUnsafe
			}
		case "href", "xlink:href":
			if !strings.HasPrefix(a.Value, "#") {
				continue // external references are stripped
			}
		}

		w.buf.WriteByte(' ')
		w.buf.WriteString(name)
		w.buf.WriteString(`="`)
		_ = xml.EscapeText(&w.buf, []byte(a.Value))
		w.buf.WriteByte('"')
	}

	w.buf.WriteByte('>')

	if t.Name.Local == "style" {
		for _, a := range t.Attr {
			if a.Name.Local == "type" && a.Value != "" && !strings.EqualFold(a.Value, "text/css") {
				return errSVGUnsafe
			}
		}

		w.inStyle = true
		w.styleBuf.Reset()
	}

	return nil
}

func (w *svgWriter) end(local string) error {
	if local == "style" && w.inStyle {
		css := w.styleBuf.String()
		if !cssSafe(css) {
			return errSVGUnsafe
		}

		_ = xml.EscapeText(&w.buf, []byte(css))
		w.inStyle = false
	}

	w.buf.WriteString("</" + local + ">")

	return nil
}

// attrName maps an attribute to its output name, or reports false when it is
// dropped (not on the allowlist, namespace declarations, event handlers).
func attrName(n xml.Name) (string, bool) {
	switch n.Space {
	case "":
		if n.Local == "xmlns" || !svgAttrs[n.Local] {
			return "", false
		}

		return n.Local, true
	case xlinkNS:
		if n.Local == "href" {
			return "xlink:href", true
		}
	case xmlNS:
		if n.Local == "space" {
			return "xml:space", true
		}
	}

	return "", false
}
