// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ansibletest builds Ansible collection artifacts for tests.
package ansibletest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
)

// TB is the part of testing.TB the builders use, which fuzz targets satisfy too.
type TB interface {
	Helper()
	Fatal(args ...any)
}

// Spec describes a collection.
type Spec struct {
	Namespace, Name, Version string
	// Dependencies maps "namespace.name" to a requirement.
	Dependencies map[string]string
	// License is the single license identifier of collection_info.
	License string
	// RequiresAnsible is written to meta/runtime.yml when not empty.
	RequiresAnsible string
	// Files are extra files (path to content) of the collection.
	Files map[string]string
}

// Entry is one tar entry.
type Entry struct {
	Name string
	Body []byte
	// Type is the tar type flag; 0 means a regular file.
	Type byte
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func mustJSON(tb TB, v any) []byte {
	tb.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		tb.Fatal(err)
	}

	return b
}

// Entries returns the entries of a valid artifact of s: MANIFEST.json,
// FILES.json and the files.
func Entries(tb TB, s Spec) []Entry {
	tb.Helper()

	files := map[string]string{"README.md": "# " + s.Name + "\n"}
	maps.Copy(files, s.Files)

	if s.RequiresAnsible != "" {
		files["meta/runtime.yml"] = "---\nrequires_ansible: '" + s.RequiresAnsible + "'\n"
	}

	listed := []map[string]any{{"name": ".", "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1}}
	entries := make([]Entry, 0, len(files)+2)

	for _, name := range slices.Sorted(maps.Keys(files)) {
		listed = append(listed, map[string]any{
			"name": name, "ftype": "file", "chksum_type": "sha256", "chksum_sha256": sum([]byte(files[name])), "format": 1,
		})
		entries = append(entries, Entry{Name: name, Body: []byte(files[name])})
	}

	filesJSON := mustJSON(tb, map[string]any{"files": listed, "format": 1})

	deps := s.Dependencies
	if deps == nil {
		deps = map[string]string{}
	}

	manifest := mustJSON(tb, map[string]any{
		"collection_info": map[string]any{
			"namespace": s.Namespace, "name": s.Name, "version": s.Version, "authors": []string{"Ann <ann@example.com>"},
			"readme": "README.md", "tags": []string{"demo"}, "description": "A demo collection.", "license": []string{s.License},
			"license_file": nil, "dependencies": deps, "repository": "https://example.com/repo", "documentation": nil,
			"homepage": nil, "issues": nil,
		},
		"file_manifest_file": map[string]any{
			"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": sum(filesJSON), "format": 1,
		},
		"format": 1,
	})

	return append([]Entry{{Name: "MANIFEST.json", Body: manifest}, {Name: "FILES.json", Body: filesJSON}}, entries...)
}

// Tar returns the gzip-compressed tar of entries.
func Tar(tb TB, entries []Entry) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
		}

		hdr := &tar.Header{Name: e.Name, Typeflag: typ, Mode: 0o644, Size: int64(len(e.Body))}
		if typ != tar.TypeReg {
			hdr.Size = 0
			hdr.Linkname = "target"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			tb.Fatal(err)
		}

		if typ == tar.TypeReg {
			if _, err := tw.Write(e.Body); err != nil {
				tb.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// Build returns a valid artifact of s.
func Build(tb TB, s Spec) []byte {
	tb.Helper()

	return Tar(tb, Entries(tb, s))
}
