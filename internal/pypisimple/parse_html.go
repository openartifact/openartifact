// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// eachAnchor calls fn for every <a> element of an HTML document with its
// attributes and (unescaped) text. Nothing is retained between calls, so the
// full PyPI index (hundreds of thousands of anchors) is walked in constant
// memory beyond what fn keeps.
func eachAnchor(r io.Reader, fn func(attrs []html.Attribute, text string)) error {
	z := html.NewTokenizer(r)

	var (
		open  bool
		attrs []html.Attribute
		text  strings.Builder
	)

	flush := func() {
		if open {
			fn(attrs, text.String())
		}

		open, attrs = false, nil

		text.Reset()
	}

	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); !errors.Is(err, io.EOF) {
				return fmt.Errorf("parse simple page: %w", err)
			}

			flush()

			return nil
		case html.StartTagToken:
			tok := z.Token()
			if tok.Data != "a" {
				continue
			}

			flush()

			open, attrs = true, tok.Attr
		case html.TextToken:
			if open {
				text.Write(z.Text())
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); open && string(name) == "a" {
				flush()
			}
		case html.SelfClosingTagToken, html.CommentToken, html.DoctypeToken:
		}
	}
}

func attr(attrs []html.Attribute, key string) (string, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Val, true
		}
	}

	return "", false
}

// ParseIndexHTML returns the project names of a PEP 503 index page.
func ParseIndexHTML(r io.Reader) ([]string, error) {
	var names []string

	err := eachAnchor(r, func(_ []html.Attribute, text string) {
		if name := strings.TrimSpace(text); name != "" {
			names = append(names, name)
		}
	})
	if err != nil {
		return nil, err
	}

	return names, nil
}

// ParseProjectHTML returns the files of a PEP 503 project page. Anchors
// without an href, or whose href is only a fragment, are ignored; the filename
// is the anchor text.
func ParseProjectHTML(r io.Reader) ([]File, error) {
	var files []File

	err := eachAnchor(r, func(attrs []html.Attribute, text string) {
		if f, ok := fileFromAnchor(attrs, text); ok {
			files = append(files, f)
		}
	})
	if err != nil {
		return nil, err
	}

	return files, nil
}

func fileFromAnchor(attrs []html.Attribute, text string) (File, bool) {
	href, ok := attr(attrs, "href")
	if !ok || strings.TrimSpace(href) == "" {
		return File{}, false
	}

	var f File

	f.RequiresPython, _ = attr(attrs, "data-requires-python")
	f.URL, f.Hashes = splitFragment(strings.TrimSpace(href))

	// A link that is only a fragment names no file: resolved against the page
	// it would be the page itself.
	if f.URL == "" {
		return File{}, false
	}

	f.Filename = strings.TrimSpace(text)
	if f.Filename == "" {
		f.Filename = lastSegment(f.URL)
	}

	if f.Filename == "" {
		return File{}, false
	}

	if v, ok := attr(attrs, "data-core-metadata"); ok {
		f.CoreMetadata, f.CoreMetadataHashes = parseMetadataHint(v)
	} else if v, ok := attr(attrs, "data-dist-info-metadata"); ok {
		f.CoreMetadata, f.CoreMetadataHashes = parseMetadataHint(v)
	}

	if v, ok := attr(attrs, "data-yanked"); ok {
		f.Yanked, f.YankedReason = true, v
	}

	return f, true
}

// splitFragment separates "#algo=hex" from a link.
func splitFragment(href string) (string, map[string]string) {
	u, frag, found := strings.Cut(href, "#")
	if !found {
		return u, nil
	}

	algo, hex, ok := strings.Cut(frag, "=")
	if !ok || algo == "" || hex == "" {
		return u, nil
	}

	return u, map[string]string{strings.ToLower(algo): strings.ToLower(hex)}
}

// parseMetadataHint reads a data-core-metadata attribute: "true" or
// "algo=hex".
func parseMetadataHint(v string) (bool, map[string]string) {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "false") {
		return false, nil
	}

	if algo, hex, ok := strings.Cut(v, "="); ok && algo != "" && hex != "" {
		return true, map[string]string{strings.ToLower(algo): strings.ToLower(hex)}
	}

	return true, nil
}

func lastSegment(u string) string {
	u, _, _ = strings.Cut(u, "?")

	return u[strings.LastIndexByte(u, '/')+1:]
}
