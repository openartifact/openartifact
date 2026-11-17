// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// checkDeps asserts what Parse promises for any input it accepts: every
// dependency has a name and a registry version, the list is sorted by name then
// version with each pair once, its requirers are sorted, unique and never itself,
// and parsing again gives the same list.
func checkDeps(t *testing.T, kind Kind, data []byte) {
	t.Helper()

	deps, err := Parse(kind, data)
	if err != nil {
		return
	}

	for i, d := range deps {
		if d.Name == "" || d.Version == "" {
			t.Fatalf("dependency %d has an empty name or version: %+v", i, d)
		}

		if kind != Composer && (strings.Contains(d.Version, ":") || strings.Contains(d.Version, "/")) {
			t.Fatalf("npm dependency %d has a non-registry version: %+v", i, d)
		}

		for j, r := range d.RequiredBy {
			if r == "" || r == d.Name || (j > 0 && d.RequiredBy[j-1] >= r) {
				t.Fatalf("dependency %d has an empty, self or unsorted requirer: %+v", i, d)
			}
		}

		if i > 0 {
			p := deps[i-1]
			if p.Name > d.Name || (p.Name == d.Name && p.Version >= d.Version) {
				t.Fatalf("dependencies %d and %d are not sorted and unique: %+v, %+v", i-1, i, p, d)
			}
		}
	}

	again, err := Parse(kind, data)
	if err != nil || !reflect.DeepEqual(deps, again) {
		t.Fatalf("parsing again gave %+v, %v", again, err)
	}
}

func addSeed(f *testing.F, path string) {
	f.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		f.Fatal(err)
	}

	f.Add(data)
}

func FuzzComposerLock(f *testing.F) {
	addSeed(f, "testdata/composer.lock")
	f.Add([]byte(`{"packages":[{"name":"a/b","version":"1.0.0","dist":{"type":"path"}}],"packages-dev":null}`))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, Composer, data) })
}

func FuzzPackageLock(f *testing.F) {
	addSeed(f, "testdata/package-lock.json")
	addSeed(f, "testdata/package-lock-v1.json")
	f.Add([]byte(`{"dependencies":{"a":{"version":"npm:@s/b@1.0.0","dependencies":{"c":{"version":"2.0.0"}}}}}`))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, NPM, data) })
}

func FuzzPnpmLock(f *testing.F) {
	addSeed(f, "testdata/pnpm-lock.yaml")
	addSeed(f, "testdata/pnpm-lock-v6.yaml")
	addSeed(f, "testdata/pnpm-lock-v5.yaml")
	f.Add([]byte("lockfileVersion: '9.0'\npackages:\n  '@s/a@1.0.0': {resolution: {integrity: x}}\nsnapshots:\n  '@s/a@1.0.0': {}\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, PNPM, data) })
}

func FuzzYarnLock(f *testing.F) {
	addSeed(f, "testdata/yarn.lock")
	addSeed(f, "testdata/yarn-v1.lock")
	f.Add([]byte("\"@s/x@npm:@o/y@^1\":\n  version \"1.0.0\"\n  dependencies:\n    z \"^2\"\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, Yarn, data) })
}

func FuzzPoetryLock(f *testing.F) {
	addSeed(f, "testdata/poetry.lock")
	addSeed(f, "testdata/poetry-1.8/poetry.lock")
	f.Add([]byte("[[package]]\nname = \"A_b\"\nversion = \"1\"\ncategory = \"dev\"\n[package.dependencies]\nc = \"*\"\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, Poetry, data) })
}

func FuzzUVLock(f *testing.F) {
	addSeed(f, "testdata/uv.lock")
	f.Add([]byte("version = 1\n[[package]]\nname = \"a\"\nversion = \"1\"\nsource = { virtual = \".\" }\n" +
		"dependencies = [{ name = \"a\" }]\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, UV, data) })
}

func FuzzCargoLock(f *testing.F) {
	addSeed(f, "testdata/Cargo.lock")
	f.Add([]byte("[[package]]\nname = \"a\"\nversion = \"1.0.0\"\nsource = \"sparse+x\"\ndependencies = [\"a 1.0.0\", \" \"]\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, Cargo, data) })
}

func FuzzGemfileLock(f *testing.F) {
	addSeed(f, "testdata/Gemfile.lock")
	f.Add([]byte("GEM\n  specs:\n    a (1.0-x86_64-linux)\n      a\n      b (>= 1)\nPATH\n  specs:\n    c (0.1)\n      a\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, Bundler, data) })
}

func FuzzNuGetLock(f *testing.F) {
	addSeed(f, "testdata/packages.lock.json")
	f.Add([]byte(`{"version":1,"dependencies":{"x":{"A":{"type":"Project","dependencies":{"a":"1"}},"a":{"resolved":"1.0"}}}}`))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, NuGet, data) })
}

func FuzzPylock(f *testing.F) {
	addSeed(f, "testdata/pylock.toml")
	addSeed(f, "testdata/pylock-pip/pylock.toml")
	f.Add([]byte("lock-version = \"1.0\"\n[[packages]]\nname = \"A_b\"\nversion = \"1\"\ndependencies = [{ name = \"a-b\" }]\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, Pylock, data) })
}

func FuzzGoSum(f *testing.F) {
	addSeed(f, "testdata/go.sum")
	f.Add([]byte("example.com/a/v2 v2.0.0/go.mod h1:x=\nexample.com/A v1.0.0-RC.1 h1:y=\n"))

	f.Fuzz(func(t *testing.T, data []byte) { checkDeps(t, GoSum, data) })
}
