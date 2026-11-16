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

// pyDeps is what each Python fixture installs from an index: the same project
// locked by uv, Poetry 2.2 and Poetry 1.8 (acme-six, a git dependency, and
// acme-local, a directory one, are left out; acme-json is an extra). dev marks
// the packages only the dev group needs; Poetry 1.8 records none.
func pyDeps(dev bool) []Dep {
	by := func(names ...string) []string { return names }

	return []Dep{
		{Name: "acme-certs", Version: "2026.10.23", RequiredBy: by("acme-http")},
		{Name: "acme-charset", Version: "3.5.2", RequiredBy: by("acme-http")},
		{Name: "acme-color", Version: "0.4.6", Dev: dev, RequiredBy: by("acme-test")},
		{Name: "acme-exc", Version: "1.3.1", Dev: dev, RequiredBy: by("acme-test")},
		{Name: "acme-http", Version: "2.32.3"},
		{Name: "acme-idna", Version: "3.20", RequiredBy: by("acme-http", "acme-local")},
		{Name: "acme-ini", Version: "2.3.1", Dev: dev, RequiredBy: by("acme-test")},
		{Name: "acme-json", Version: "5.10.0"},
		{Name: "acme-packaging", Version: "26.3", Dev: dev, RequiredBy: by("acme-test")},
		{Name: "acme-plug", Version: "1.7.0", Dev: dev, RequiredBy: by("acme-test")},
		{Name: "acme-test", Version: "8.3.3", Dev: dev},
		{Name: "acme-toml", Version: "2.5.0", Dev: dev, RequiredBy: by("acme-test")},
		{Name: "acme-typing", Version: "4.16.0", Dev: dev, RequiredBy: by("acme-exc")},
		{Name: "acme-url", Version: "2.8.0", RequiredBy: by("acme-http")},
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
		{"uv.lock", UV, "testdata/uv.lock", pyDeps(true)},
		{"poetry.lock 2.1", Poetry, "testdata/poetry.lock", pyDeps(true)},
		{"poetry.lock 2.0", Poetry, "testdata/poetry-1.8/poetry.lock", pyDeps(false)},
		// A workspace (acme-app, acme-helper) with a git crate (acme-cfg) and
		// a dev-dependency (acme-hex) Cargo.lock does not mark.
		{"Cargo.lock", Cargo, "testdata/Cargo.lock", []Dep{
			{Name: "acme-hex", Version: "0.4.3"},
			{Name: "acme-ident", Version: "1.0.27", RequiredBy: []string{"acme-macro", "acme-syn"}},
			{Name: "acme-itoa", Version: "1.0.11"},
			{Name: "acme-macro", Version: "1.0.107", RequiredBy: []string{"acme-quote", "acme-syn", "acme_serde_derive"}},
			{Name: "acme-memchr", Version: "2.7.4"},
			{Name: "acme-quote", Version: "1.0.47", RequiredBy: []string{"acme-syn", "acme_serde_derive"}},
			{Name: "acme-serde", Version: "1.0.210"},
			{Name: "acme-syn", Version: "2.0.119", RequiredBy: []string{"acme_serde_derive"}},
			{Name: "acme_serde_derive", Version: "1.0.210", RequiredBy: []string{"acme-serde"}},
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
		{"poetry empty", Poetry, ""},
		{"poetry not TOML", Poetry, "[[package]\n"},
		{"poetry wrong type", Poetry, "package = 1\n"},
		{"uv without version", UV, "[[package]]\nname = \"a\"\n"},
		{"uv wrong type", UV, "version = 1\npackage = \"a\"\n"},
		{"cargo empty", Cargo, ""},
		{"cargo wrong type", Cargo, "version = 4\npackage = [1]\n"},
	} {
		if _, err := Parse(tc.kind, []byte(tc.data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}

	if _, err := Parse("gradle", []byte("{}")); !errors.Is(err, ErrUnknownLockfile) {
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
		"Cargo.lock":               Cargo,
		"py/poetry.lock":           Poetry,
		"uv.lock":                  UV,
		"cargo.lock":               "",
		"Pipfile.lock":             "",
		"composer.json":            "",
	} {
		got, err := KindOf(path)
		if got != want || (want == "") != errors.Is(err, ErrUnknownLockfile) {
			t.Errorf("KindOf(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
}

// TestParseSources: only index and registry sources count, whichever spelling
// of a dependency a lockfile uses.
func TestParseSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		data string
		want []Dep
	}{
		{"cargo sparse registry, old dependency spelling", Cargo, `
[[package]]
name = "a"
version = "1.0.0"
source = "sparse+https://oa.example.test/repos/crates/index/"
dependencies = ["b 2.0.0 (registry+https://github.com/rust-lang/crates.io-index)"]

[[package]]
name = "b"
version = "2.0.0"
source = "registry+https://github.com/rust-lang/crates.io-index"

[[package]]
name = "c"
version = "0.1.0"
source = "git+https://example.test/c#0123"
dependencies = ["b"]
`, []Dep{
			{Name: "a", Version: "1.0.0"},
			{Name: "b", Version: "2.0.0", RequiredBy: []string{"a", "c"}},
		}},
		{"uv two versions of a name, url and path sources", UV, `
version = 1

[[package]]
name = "app"
version = "0.1.0"
source = { editable = "." }
dependencies = [{ name = "b", version = "2.0.0", source = { registry = "https://pypi.org/simple" } }]

[package.dev-dependencies]
test = [{ name = "b", version = "1.0.0", source = { registry = "https://pypi.org/simple" } }]

[[package]]
name = "B"
version = "1.0.0"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "b"
version = "2.0.0"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "c"
version = "1.0.0"
source = { url = "https://example.test/c-1.0.0.tar.gz" }

[[package]]
name = "d"
version = "1.0.0"
source = { path = "dist/d-1.0.0.whl" }
`, []Dep{
			{Name: "b", Version: "1.0.0", Dev: true},
			{Name: "b", Version: "2.0.0"},
		}},
		{"poetry before 1.5", Poetry, `
[[package]]
name = "Acme.Old"
version = "1.0"
category = "dev"

[[package]]
name = "acme-url"
version = "1.0"
category = "main"

[package.source]
type = "url"
url = "https://example.test/acme_url-1.0.tar.gz"

[metadata]
lock-version = "1.1"
`, []Dep{{Name: "acme-old", Version: "1.0", Dev: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, err := Parse(tc.kind, []byte(tc.data))
			if err != nil || !reflect.DeepEqual(deps, tc.want) {
				t.Errorf("deps = %+v, %v\nwant %+v", deps, err, tc.want)
			}
		})
	}
}

func TestDevRecorded(t *testing.T) {
	read := func(path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		return data
	}

	for _, tc := range []struct {
		kind Kind
		file string
		want bool
	}{
		{NPM, "testdata/package-lock.json", true},
		{PNPM, "testdata/pnpm-lock.yaml", true},
		{UV, "testdata/uv.lock", true},
		{Poetry, "testdata/poetry.lock", true},
		{Poetry, "testdata/poetry-1.8/poetry.lock", false},
		{Yarn, "testdata/yarn.lock", false},
		{Cargo, "testdata/Cargo.lock", false},
	} {
		if got := DevRecorded(tc.kind, read(tc.file)); got != tc.want {
			t.Errorf("DevRecorded(%s, %s) = %v, want %v", tc.kind, tc.file, got, tc.want)
		}
	}
}
