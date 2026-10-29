// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Source names the JSON document of the artifact detail a Field is read from.
// The detail query loads one row of package_files (the artifact, its id is the
// listing id) with its version, package and side-table row and hands them to
// Detail.Resolve as a Document.
type Source string

const (
	// FromPackage reads packages.attrs.
	FromPackage Source = "p"
	// FromVersion reads package_versions.metadata.
	FromVersion Source = "v"
	// FromFile reads package_files.attrs.
	FromFile Source = "f"
	// FromChecksums reads package_files.checksums.
	FromChecksums Source = "c"
	// FromSide reads the format's side-table row of the file (conan_files,
	// deb_files, maven_files, pacman_files, rpm_files) as a JSON object keyed by
	// column; bulk columns that no detail shows (pacman file_list, signature) are
	// not loaded.
	FromSide Source = "s"
	// FromColumns reads the plain columns the generic tables keep outside any JSON:
	// arch, download_count (numbers), name (packages.name) and version_key.
	FromColumns Source = "k"
	// FromSibling reads package_files.attrs of the unlisted sibling file of the
	// same version (go: the go.mod file next to the listed zip).
	FromSibling Source = "m"
)

// Document is the JSON the detail query loads for one artifact, one slot per Source.
// A slot is nil when the row has no such part (no side table, no sibling file).
type Document struct {
	Package   json.RawMessage `json:"p"`
	Version   json.RawMessage `json:"v"`
	File      json.RawMessage `json:"f"`
	Checksums json.RawMessage `json:"c"`
	Side      json.RawMessage `json:"s"`
	Columns   json.RawMessage `json:"k"`
	Sibling   json.RawMessage `json:"m"`
}

// Field is the location of one value in a Document: a source and a dotted key
// path inside it ("manifest_json.description"). The zero Field names nothing.
type Field struct {
	Source Source
	Path   string
	// Join separates the elements of an array value ", " when empty.
	Join string
}

// InfoField is one display entry of the detail: Key is the label the API
// returns, the value is read from the Field.
type InfoField struct {
	Key string
	Field
}

// Detail is how the artifact detail endpoint reads a format's metadata out of
// the generic tables. Every field is data; the zero Detail (a format with no
// descriptive metadata) shows only what the generic columns hold. Text values
// that are missing, null, empty or not a scalar or an array of scalars read as
// "" and are dropped from Info.
type Detail struct {
	Summary     Field
	Description Field
	License     Field
	Homepage    Field
	// MD5 and SHA1 are the digests shown beside the blob's SHA-256.
	MD5  Field
	SHA1 Field
	// Info are the format-specific display entries.
	Info []InfoField
	// Deps is the raw JSON the dependencies are decoded from, and Decoder the name
	// of the decoder (registered by the API layer) that turns it into a list; an
	// empty Decoder means the format shows no dependencies.
	Deps    Field
	Decoder string
}

// Resolved is a Detail applied to a Document.
type Resolved struct {
	Summary, Description, License, Homepage string
	MD5, SHA1                               string
	Info                                    map[string]string
	// Deps is the raw JSON of the dependency source, nil when absent.
	Deps []byte
}

// Resolve reads the fields of d out of doc.
func (d Detail) Resolve(doc Document) Resolved {
	out := Resolved{
		Summary:     d.Summary.Text(doc),
		Description: d.Description.Text(doc),
		License:     d.License.Text(doc),
		Homepage:    d.Homepage.Text(doc),
		MD5:         d.MD5.Text(doc),
		SHA1:        d.SHA1.Text(doc),
		Info:        make(map[string]string, len(d.Info)),
	}

	for _, f := range d.Info {
		if v := f.Text(doc); v != "" {
			out.Info[f.Key] = v
		}
	}

	if d.Decoder != "" {
		out.Deps = d.Deps.Raw(doc)
	}

	return out
}

// Raw returns the JSON value at the field, nil when it is absent or null.
func (f Field) Raw(doc Document) []byte {
	var src json.RawMessage

	switch f.Source {
	case FromPackage:
		src = doc.Package
	case FromVersion:
		src = doc.Version
	case FromFile:
		src = doc.File
	case FromChecksums:
		src = doc.Checksums
	case FromSide:
		src = doc.Side
	case FromColumns:
		src = doc.Columns
	case FromSibling:
		src = doc.Sibling
	default:
		return nil
	}

	cur := []byte(src)

	if f.Path != "" {
		for key := range strings.SplitSeq(f.Path, ".") {
			var obj map[string]json.RawMessage
			if json.Unmarshal(cur, &obj) != nil {
				return nil
			}

			cur = obj[key]
		}
	}

	cur = bytes.TrimSpace(cur)
	if len(cur) == 0 || string(cur) == "null" {
		return nil
	}

	return cur
}

// Text returns the value at the field as display text: a string as is, a number
// or boolean in its JSON spelling and an array of scalars joined by Join.
func (f Field) Text(doc Document) string {
	raw := f.Raw(doc)
	if raw == nil {
		return ""
	}

	sep := f.Join
	if sep == "" {
		sep = ", "
	}

	if raw[0] == '[' {
		var elems []json.RawMessage
		if json.Unmarshal(raw, &elems) != nil {
			return ""
		}

		parts := make([]string, 0, len(elems))

		for _, e := range elems {
			if s := scalarText(e); s != "" {
				parts = append(parts, s)
			}
		}

		return strings.Join(parts, sep)
	}

	return scalarText(raw)
}

func scalarText(raw []byte) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if dec.Decode(&v) != nil {
		return ""
	}

	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}
