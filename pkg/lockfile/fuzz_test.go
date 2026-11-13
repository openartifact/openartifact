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

		if kind == NPM && (strings.Contains(d.Version, ":") || strings.Contains(d.Version, "/")) {
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
