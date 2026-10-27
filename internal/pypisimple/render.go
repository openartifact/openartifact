// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"slices"
)

// Format is a simple API serialization.
type Format int

const (
	// HTML is the PEP 503 serialization.
	HTML Format = iota
	// JSON is the PEP 691 serialization.
	JSON
)

const apiVersion = "1.0"

// preferredHash picks the digest advertised in an HTML link fragment: sha256
// when present, else the first algorithm by name.
func preferredHash(hashes map[string]string) (string, string, bool) {
	if h, ok := hashes["sha256"]; ok && h != "" {
		return "sha256", h, true
	}

	for _, algo := range slices.Sorted(maps.Keys(hashes)) {
		if hashes[algo] != "" {
			return algo, hashes[algo], true
		}
	}

	return "", "", false
}

// RenderIndex renders the project index. href maps a listed project name to
// the URL of its page.
func RenderIndex(f Format, names []string, href func(name string) string) ([]byte, error) {
	if f == JSON {
		type project struct {
			Name string `json:"name"`
		}

		doc := struct {
			Meta     map[string]string `json:"meta"`
			Projects []project         `json:"projects"`
		}{Meta: map[string]string{"api-version": apiVersion}, Projects: make([]project, len(names))}

		for i, n := range names {
			doc.Projects[i] = project{Name: n}
		}

		return marshalJSON(doc)
	}

	var b bytes.Buffer

	b.WriteString(
		"<!DOCTYPE html>\n<html><head><meta name=\"pypi:repository-version\" content=\"" + apiVersion + "\"><title>Simple index</title></head><body>\n",
	)

	for _, n := range names {
		fmt.Fprintf(&b, "<a href=\"%s\">%s</a><br/>\n", html.EscapeString(href(n)), html.EscapeString(n))
	}

	b.WriteString("</body></html>\n")

	return b.Bytes(), nil
}

// RenderProject renders one project's page. href maps a file to the URL the
// client should download it from (without a hash fragment).
func RenderProject(f Format, name string, files []File, href func(*File) string) ([]byte, error) {
	if f == JSON {
		return renderProjectJSON(name, files, href)
	}

	var b bytes.Buffer

	title := html.EscapeString(name)

	b.WriteString(
		"<!DOCTYPE html>\n<html><head><meta name=\"pypi:repository-version\" content=\"" + apiVersion + "\"><title>Links for " + title + "</title></head><body>\n",
	)
	b.WriteString("<h1>Links for " + title + "</h1>\n")

	for i := range files {
		writeHTMLLink(&b, &files[i], href(&files[i]))
	}

	b.WriteString("</body></html>\n")

	return b.Bytes(), nil
}

func writeHTMLLink(b *bytes.Buffer, f *File, href string) {
	if algo, hex, ok := preferredHash(f.Hashes); ok {
		href += "#" + algo + "=" + hex
	}

	fmt.Fprintf(b, `<a href="%s"`, html.EscapeString(href))

	if f.RequiresPython != "" {
		fmt.Fprintf(b, ` data-requires-python="%s"`, html.EscapeString(f.RequiresPython))
	}

	if f.CoreMetadata {
		v := "true"
		if algo, hex, ok := preferredHash(f.CoreMetadataHashes); ok {
			v = algo + "=" + hex
		}

		// Both spellings: PEP 714 renamed the attribute and clients accept
		// either, so older pips still find the metadata.
		fmt.Fprintf(b, ` data-core-metadata="%[1]s" data-dist-info-metadata="%[1]s"`, html.EscapeString(v))
	}

	if f.Yanked {
		fmt.Fprintf(b, ` data-yanked="%s"`, html.EscapeString(f.YankedReason))
	}

	fmt.Fprintf(b, ">%s</a><br/>\n", html.EscapeString(f.Filename))
}

type jsonOutFile struct {
	Filename         string            `json:"filename"`
	URL              string            `json:"url"`
	Hashes           map[string]string `json:"hashes"`
	RequiresPython   string            `json:"requires-python,omitempty"`    //nolint:tagliatelle // PEP 691 wire format
	CoreMetadata     any               `json:"core-metadata,omitempty"`      //nolint:tagliatelle // PEP 691 wire format
	DistInfoMetadata any               `json:"dist-info-metadata,omitempty"` //nolint:tagliatelle // PEP 691 wire format
	Yanked           any               `json:"yanked,omitempty"`
	Size             int64             `json:"size,omitempty"`
	UploadTime       string            `json:"upload-time,omitempty"` //nolint:tagliatelle // PEP 691 wire format
}

func renderProjectJSON(name string, files []File, href func(*File) string) ([]byte, error) {
	doc := struct {
		Meta  map[string]string `json:"meta"`
		Name  string            `json:"name"`
		Files []jsonOutFile     `json:"files"`
	}{Meta: map[string]string{"api-version": apiVersion}, Name: name, Files: make([]jsonOutFile, len(files))}

	for i := range files {
		f := &files[i]

		out := jsonOutFile{
			Filename:       f.Filename,
			URL:            href(f),
			Hashes:         cloneOrEmpty(f.Hashes),
			RequiresPython: f.RequiresPython,
			Size:           f.Size,
			UploadTime:     f.UploadTime,
		}

		if f.CoreMetadata {
			var hint any = true
			if len(f.CoreMetadataHashes) > 0 {
				hint = f.CoreMetadataHashes
			}

			out.CoreMetadata, out.DistInfoMetadata = hint, hint
		}

		switch {
		case f.Yanked && f.YankedReason != "":
			out.Yanked = f.YankedReason
		case f.Yanked:
			out.Yanked = true
		}

		doc.Files[i] = out
	}

	return marshalJSON(doc)
}

func cloneOrEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}

	return m
}

func marshalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("render simple json: %w", err)
	}

	return b, nil
}
