// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"encoding/json"
	"testing"
)

func TestDetailResolve(t *testing.T) {
	doc := Document{
		Package:   json.RawMessage(`{"scope":"acme","n":3}`),
		Version:   json.RawMessage(`{"manifest_json":{"description":"d","license":{"type":"MIT"}},"keywords":["a","b"],"classifiers":["x","y"],"yanked":false,"nar_size":12345678901}`),
		File:      json.RawMessage(`{"installed_size":0}`),
		Checksums: json.RawMessage(`{"shasum":"abc"}`),
		Columns:   json.RawMessage(`{"download_count":7}`),
		Sibling:   json.RawMessage(`{"go_mod_content":"module m\n"}`),
	}

	d := Detail{
		Description: Field{Source: FromVersion, Path: "manifest_json.description"},
		License:     Field{Source: FromVersion, Path: "manifest_json.license"},
		SHA1:        Field{Source: FromChecksums, Path: "shasum"},
		MD5:         Field{Source: FromChecksums, Path: "md5"},
		Info: []InfoField{
			{Key: "scope", Field: Field{Source: FromPackage, Path: "scope"}},
			{Key: "keywords", Field: Field{Source: FromVersion, Path: "keywords"}},
			{Key: "classifiers", Field: Field{Source: FromVersion, Path: "classifiers", Join: "\n"}},
			{Key: "yanked", Field: Field{Source: FromVersion, Path: "yanked"}},
			{Key: "nar_size", Field: Field{Source: FromVersion, Path: "nar_size"}},
			{Key: "downloads", Field: Field{Source: FromColumns, Path: "download_count"}},
			{Key: "installed_size", Field: Field{Source: FromFile, Path: "installed_size"}},
			{Key: "missing", Field: Field{Source: FromSide, Path: "nothing"}},
		},
		Deps:    Field{Source: FromSibling, Path: "go_mod_content"},
		Decoder: "go-mod",
	}

	got := d.Resolve(doc)

	if got.Description != "d" || got.License != "" || got.SHA1 != "abc" || got.MD5 != "" {
		t.Fatalf("text fields %+v", got)
	}

	want := map[string]string{
		"scope": "acme", "keywords": "a, b", "classifiers": "x\ny", "yanked": "false",
		"nar_size": "12345678901", "downloads": "7", "installed_size": "0",
	}

	if len(got.Info) != len(want) {
		t.Fatalf("info %v, want %v", got.Info, want)
	}

	for k, v := range want {
		if got.Info[k] != v {
			t.Errorf("info[%s] = %q, want %q", k, got.Info[k], v)
		}
	}

	if string(got.Deps) != `"module m\n"` {
		t.Fatalf("deps %s", got.Deps)
	}

	// Without a decoder the format shows no dependencies.
	d.Decoder = ""
	if got := d.Resolve(doc); got.Deps != nil {
		t.Fatalf("deps without decoder: %s", got.Deps)
	}

	if zero := (Detail{}).Resolve(Document{}); len(zero.Info) != 0 || zero.Deps != nil || zero.Summary != "" {
		t.Fatalf("zero detail %+v", zero)
	}
}
