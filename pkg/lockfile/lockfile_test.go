// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func parseFile(t *testing.T, kind Kind, path string) []Dep {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	deps, err := Parse(kind, data)
	if err != nil {
		t.Fatalf("Parse %s: %v", path, err)
	}

	return deps
}

// TestParse: the registry packages of each lockfile, sorted and once each; local,
// linked, bundled and VCS or file dependencies are left out, an alias is named
// after the package it installs, and a version used outside development is not
// Dev.
func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		file string
		want []Dep
	}{
		{"composer.lock", Composer, "testdata/composer.lock", []Dep{
			{Name: "acme/console", Version: "v6.4.3"},
			{Name: "acme/http-client", Version: "7.8.1"},
			{Name: "acme/polyfill", Version: "v1.29.0"},
			{Name: "acme/test-runner", Version: "10.5.10", Dev: true},
		}},
		{"package-lock.json v3", NPM, "testdata/package-lock.json", []Dep{
			{Name: "@acme/shared", Version: "3.4.21"},
			{Name: "acme-bundler", Version: "5.1.4", Dev: true},
			{Name: "acme-minifier", Version: "0.19.12", Dev: true},
			{Name: "acme-pad", Version: "1.3.0"},
			{Name: "acme-width", Version: "4.2.3"},
		}},
		{"package-lock.json v1", NPM, "testdata/package-lock-v1.json", []Dep{
			{Name: "@acme/real", Version: "1.2.3"},
			{Name: "acme-log", Version: "2.6.9"},
			{Name: "acme-log", Version: "4.3.4", Dev: true},
			{Name: "acme-server", Version: "4.18.2"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFile(t, tc.kind, tc.file); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("deps =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	for name, data := range map[string]string{
		"empty":      "",
		"array":      "[]",
		"not JSON":   "{",
		"wrong type": `{"packages": "x"}`,
	} {
		for _, kind := range []Kind{Composer, NPM} {
			if _, err := Parse(kind, []byte(data)); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s %s: err = %v, want ErrInvalid", kind, name, err)
			}
		}
	}

	if _, err := Parse("yarn", []byte("{}")); !errors.Is(err, ErrUnknownLockfile) {
		t.Errorf("unknown kind: err = %v", err)
	}
}

func TestKindOf(t *testing.T) {
	for path, want := range map[string]Kind{
		"composer.lock":            Composer,
		"app/composer.lock":        Composer,
		"package-lock.json":        NPM,
		"/src/npm-shrinkwrap.json": NPM,
		"yarn.lock":                "",
		"composer.json":            "",
	} {
		got, err := KindOf(path)
		if got != want || (want == "") != errors.Is(err, ErrUnknownLockfile) {
			t.Errorf("KindOf(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
}
