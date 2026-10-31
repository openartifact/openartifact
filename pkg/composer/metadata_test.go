// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer_test

import (
	"encoding/json"
	"errors"
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

func TestVersionPublishedAndRenderSourceFilter(t *testing.T) {
	t.Parallel()

	for published, want := range map[string]string{
		"2026-03-10T12:00:00+00:00": "2026-03-10T12:00:00Z",
		"2026-03-10T14:00:00+02:00": "2026-03-10T12:00:00Z",
		"":                          "",
		"not a time":                "",
	} {
		got := composer.Version{"time": published}.Published()
		if (want == "") != got.IsZero() || (want != "" && got.UTC().Format(time.RFC3339) != want) {
			t.Errorf("Published(%q) = %v, want %q", published, got, want)
		}
	}

	if !(composer.Version{}).Published().IsZero() || !(composer.Version{"time": 5}).Published().IsZero() {
		t.Error("an entry without a string time must be undated")
	}

	// The filter sees expanded entries: 1.1.0 only inherits "time" from 1.2.0.
	doc, err := composer.EncodeDocument(map[string][]composer.Version{"a/b": {
		{"name": "a/b", "version": "1.2.0", "time": "2026-03-10T11:00:00+00:00"},
		{"name": "a/b", "version": "1.1.0", "time": "2026-03-10T11:00:00+00:00"},
		{"name": "a/b", "version": "1.0.0", "time": "2026-01-01T00:00:00+00:00"},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}

	old := func(v composer.Version) bool {
		return v.Published().Before(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	}

	out, err := composer.RenderSource(doc, "a/b", "https://x/repos/r", old)
	if err != nil {
		t.Fatal(err)
	}

	pkgs, err := composer.Decode(out)
	if err != nil || len(pkgs["a/b"]) != 1 || pkgs["a/b"][0].VersionString() != "1.0.0" {
		t.Errorf("filtered = %v, %v", pkgs, err)
	}

	none := func(composer.Version) bool { return false }

	if _, err := composer.RenderSource(doc, "a/b", "https://x/repos/r", none); !errors.Is(err, composer.ErrNoneLeft) {
		t.Errorf("err = %v, want ErrNoneLeft", err)
	}
}
