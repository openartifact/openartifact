// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package composertest builds Composer dist zips for tests.
package composertest

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"
)

// Spec describes a dist zip.
type Spec struct {
	// Name is the composer.json name; Version its optional version.
	Name, Version string
	// License is the composer.json license (a string or a list).
	License any
	// Require is the composer.json require map.
	Require map[string]string
	// Description is the composer.json description.
	Description string
	// Dir wraps every entry in this top-level directory (a GitHub zipball).
	Dir string
	// Raw replaces the generated composer.json bytes when set.
	Raw []byte
	// Files are extra entries (name to content).
	Files map[string]string
}

// Build returns the zip of spec.
func Build(tb testing.TB, s Spec) []byte {
	tb.Helper()

	manifest := s.Raw
	if manifest == nil {
		doc := map[string]any{"name": s.Name}
		if s.Version != "" {
			doc["version"] = s.Version
		}

		if s.License != nil {
			doc["license"] = s.License
		}

		if s.Require != nil {
			doc["require"] = s.Require
		}

		if s.Description != "" {
			doc["description"] = s.Description
		}

		var err error
		if manifest, err = json.Marshal(doc); err != nil {
			tb.Fatalf("marshal composer.json: %v", err)
		}
	}

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	prefix := ""

	if s.Dir != "" {
		prefix = s.Dir + "/"
		if _, err := zw.Create(prefix); err != nil {
			tb.Fatalf("zip dir: %v", err)
		}
	}

	add := func(name, content string) {
		w, err := zw.Create(prefix + name)
		if err != nil {
			tb.Fatalf("zip entry %s: %v", name, err)
		}

		if _, err := w.Write([]byte(content)); err != nil {
			tb.Fatalf("zip write %s: %v", name, err)
		}
	}

	add("composer.json", string(manifest))

	for name, content := range s.Files {
		add(name, content)
	}

	if err := zw.Close(); err != nil {
		tb.Fatalf("zip close: %v", err)
	}

	return buf.Bytes()
}
