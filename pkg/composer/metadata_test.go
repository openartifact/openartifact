// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/composer"
)

func TestMinifyExpandRoundTrip(t *testing.T) {
	t.Parallel()

	versions := []composer.Version{
		{"name": "a/b", "version": "2.0.0", "description": "new", "require": map[string]any{"php": ">=8"}, "bin": []any{"x"}},
		{"name": "a/b", "version": "1.1.0", "description": "new", "require": map[string]any{"php": ">=7"}},
		{"name": "a/b", "version": "1.0.0", "description": "old", "require": map[string]any{"php": ">=7"}, "bin": []any{"x"}},
	}

	minified := composer.Minify(versions)

	wantSecond := composer.Version{"version": "1.1.0", "require": map[string]any{"php": ">=7"}, "bin": "__unset"}
	if !reflect.DeepEqual(minified[1], wantSecond) {
		t.Fatalf("second minified entry = %v, want %v", minified[1], wantSecond)
	}

	if got := composer.Expand(minified); !reflect.DeepEqual(got, versions) {
		t.Fatalf("Expand(Minify(v)) = %v, want %v", got, versions)
	}
}

func TestDocumentRoundTrip(t *testing.T) {
	t.Parallel()

	pkgs := map[string][]composer.Version{"a/b": {
		{"name": "a/b", "version": "1.0.0", "n": json.Number("12345678901234567890")},
		{"name": "a/b", "version": "0.9.0", "n": json.Number("12345678901234567890")},
	}}

	for _, minify := range []bool{false, true} {
		data, err := composer.EncodeDocument(pkgs, minify)
		if err != nil {
			t.Fatal(err)
		}

		got, err := composer.Decode(data)
		if err != nil || !reflect.DeepEqual(got, pkgs) {
			t.Fatalf("minify=%v: DecodeDocument = %v, %v; want %v", minify, got, err, pkgs)
		}
	}
}

func TestNewVersion(t *testing.T) {
	t.Parallel()

	src := `{"name":"a/b","description":"d","scripts":{"x":"y"},"require":{"php":">=8"},"license":"MIT","version":"9.9.9"}`

	v, err := composer.NewVersion("a/b", []byte(src), composer.Release{
		Version: "1.0.0", Normalized: "1.0.0.0", Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Dist: composer.Dist{URL: "https://x/y.zip", SHA1: "aa", Reference: "bb"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := v["scripts"]; ok {
		t.Error("scripts must not be served")
	}

	if v["version"] != "1.0.0" || v["version_normalized"] != "1.0.0.0" || v["time"] != "2026-01-02T03:04:05+00:00" {
		t.Errorf("identity fields wrong: %v", v)
	}

	dist, _ := v["dist"].(map[string]any)
	if dist["type"] != "zip" || dist["url"] != "https://x/y.zip" || dist["shasum"] != "aa" || dist["reference"] != "bb" {
		t.Errorf("dist wrong: %v", dist)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"monolog/monolog": true, "a/b": true, "my-vendor/my_pkg.x": true, "vendor/pkg--x": true,
		"Vendor/pkg": false, "vendor": false, "a/b/c": false, "-a/b": false, "a/-b": false, "a/b-": false, "a//b": false, "": false,
	} {
		if got := composer.ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}

	for file, want := range map[string][3]any{
		"pkg.json": {"pkg", false, true}, "pkg~dev.json": {"pkg", true, true}, "pkg.txt": {"", false, false}, "~dev.json": {"", false, false},
	} {
		pkg, dev, ok := composer.MetadataName(file)
		if pkg != want[0] || dev != want[1] || ok != want[2] {
			t.Errorf("MetadataName(%q) = %q %v %v, want %v", file, pkg, dev, ok, want)
		}
	}
}
