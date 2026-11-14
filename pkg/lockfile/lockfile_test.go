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

// jsDeps is what each JavaScript fixture installs from the registry: the same
// project locked by npm, pnpm 7, 8 and 9, Yarn 1 and Yarn 4 (acme-git, a GitHub
// dependency, and acme-local, a file: one, are left out; my-acme-pad is an
// alias of acme-pad). dev marks the development-only ones; Yarn records none.
// acme-react is a peer dependency of acme-store, which Yarn 1 does not record.
func jsDeps(dev, reactPeer bool) []Dep {
	by := func(names ...string) []string { return names }
	react := by("acme-store")
	if !reactPeer {
		react = nil
	}

	return []Dep{
		{Name: "@acme/is", Version: "4.6.0"},
		{Name: "acme-buffer", Version: "1.1.6", Dev: dev, RequiredBy: by("acme-kind")},
		{Name: "acme-envify", Version: "1.4.0", RequiredBy: by("acme-react")},
		{Name: "acme-even", Version: "1.0.0", Dev: dev},
		{Name: "acme-kind", Version: "3.2.2", Dev: dev, RequiredBy: by("acme-number")},
		{Name: "acme-number", Version: "3.0.0", Dev: dev, RequiredBy: by("acme-odd")},
		{Name: "acme-number", Version: "6.0.0", RequiredBy: by("acme-odd")},
		{Name: "acme-odd", Version: "0.1.2", Dev: dev, RequiredBy: by("acme-even")},
		{Name: "acme-odd", Version: "3.0.1", RequiredBy: by("acme-even")},
		{Name: "acme-pad", Version: "1.3.0"},
		{Name: "acme-react", Version: "18.2.0", RequiredBy: react},
		{Name: "acme-store", Version: "1.2.0"},
		{Name: "acme-tokens", Version: "4.0.0", RequiredBy: by("acme-envify")},
	}
}

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
			{Name: "acme/console", Version: "v6.4.3", RequiredBy: []string{"acme/test-runner"}},
			{Name: "acme/http-client", Version: "7.8.1"},
			{Name: "acme/polyfill", Version: "v1.29.0", RequiredBy: []string{"acme/console", "acme/http-client"}},
			{Name: "acme/test-runner", Version: "10.5.10", Dev: true},
		}},
		{"package-lock.json v3", NPM, "testdata/package-lock.json", []Dep{
			{Name: "@acme/shared", Version: "3.4.21", RequiredBy: []string{"acme-bundler"}},
			{Name: "acme-bundler", Version: "5.1.4", Dev: true},
			{Name: "acme-minifier", Version: "0.19.12", Dev: true, RequiredBy: []string{"acme-bundler"}},
			{Name: "acme-pad", Version: "1.3.0"},
			{Name: "acme-width", Version: "4.2.3", RequiredBy: []string{"acme-pad"}},
		}},
		{"package-lock.json v1", NPM, "testdata/package-lock-v1.json", []Dep{
			{Name: "@acme/real", Version: "1.2.3"},
			{Name: "acme-log", Version: "2.6.9", RequiredBy: []string{"@acme/real", "acme-server"}},
			{Name: "acme-log", Version: "4.3.4", Dev: true, RequiredBy: []string{"@acme/real", "acme-server"}},
			{Name: "acme-server", Version: "4.18.2"},
		}},
		{"pnpm-lock.yaml v5.4", PNPM, "testdata/pnpm-lock-v5.yaml", jsDeps(true, true)},
		{"pnpm-lock.yaml v6.0", PNPM, "testdata/pnpm-lock-v6.yaml", jsDeps(true, true)},
		{"pnpm-lock.yaml v9.0", PNPM, "testdata/pnpm-lock.yaml", jsDeps(true, true)},
		{"yarn.lock v1", Yarn, "testdata/yarn-v1.lock", jsDeps(false, false)},
		{"yarn.lock berry", Yarn, "testdata/yarn.lock", jsDeps(false, true)},
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

	for _, tc := range []struct {
		name string
		kind Kind
		data string
	}{
		{"pnpm empty", PNPM, ""},
		{"pnpm sequence", PNPM, "- a\n- b\n"},
		{"pnpm without lockfileVersion", PNPM, "packages:\n  /a@1.0.0: {}\n"},
		{"pnpm not YAML", PNPM, "a: [\n"},
		{"pnpm wrong type", PNPM, "lockfileVersion: '9.0'\npackages: x\n"},
		{"yarn berry wrong type", Yarn, "__metadata:\n  version: 8\na@npm:1: x\n"},
		{"yarn classic without a header", Yarn, "  version \"1.0.0\"\n"},
		{"yarn classic header without a colon", Yarn, "a@^1.0.0\n"},
	} {
		if _, err := Parse(tc.kind, []byte(tc.data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}

	if _, err := Parse("cargo", []byte("{}")); !errors.Is(err, ErrUnknownLockfile) {
		t.Errorf("unknown kind: err = %v", err)
	}
}

func TestKindOf(t *testing.T) {
	for path, want := range map[string]Kind{
		"composer.lock":            Composer,
		"app/composer.lock":        Composer,
		"package-lock.json":        NPM,
		"/src/npm-shrinkwrap.json": NPM,
		"yarn.lock":                Yarn,
		"web/pnpm-lock.yaml":       PNPM,
		"Cargo.lock":               "",
		"composer.json":            "",
	} {
		got, err := KindOf(path)
		if got != want || (want == "") != errors.Is(err, ErrUnknownLockfile) {
			t.Errorf("KindOf(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
}
