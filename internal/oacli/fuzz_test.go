// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"net/url"
	"strings"
	"testing"
	"unicode"

	"github.com/openartifact/openartifact/pkg/lockfile"
)

// FuzzMetadataPath: whatever a lockfile names a package, --fetch either skips
// it or requests a path inside the repository, with no dot segment, query,
// fragment or control character.
func FuzzMetadataPath(f *testing.F) {
	for _, name := range []string{
		"acme/a", "@scope/pkg", "plain", "..", "../x", "a/../b", "@../x", "@a/..", "a/./b", "a//b", "",
		"a?b", "a#b", "a%2f..%2fb", "a%00", "a\nb", "a\x7fb", "a b", `a\b`, "@scope/pkg/extra", "/abs", "x/",
	} {
		for _, repo := range []string{"r", "r/x", "..", "r?x", ""} {
			f.Add(repo, name, "1.0.0")
		}
	}

	f.Add("r", "acme/a", "dev-main")
	f.Add("r", "acme/a", "1.x-dev")

	kinds := []lockfile.Kind{
		lockfile.Composer, lockfile.NPM, lockfile.PNPM, lockfile.Yarn, lockfile.Poetry, lockfile.UV,
		lockfile.Pylock, lockfile.Cargo, lockfile.Bundler, lockfile.NuGet, lockfile.GoSum,
	}

	f.Fuzz(func(t *testing.T, repo, name, version string) {
		for _, kind := range kinds {
			p, ok := metadataPath(repo, kind, lockfile.Dep{Name: name, Version: version})
			if !ok {
				continue
			}

			prefix := "/repos/" + url.PathEscape(repo) + "/"
			if !strings.HasPrefix(p, prefix) {
				t.Fatalf("%s %q: %q lacks prefix %q", kind, name, p, prefix)
			}

			if strings.ContainsAny(p, "?#") {
				t.Fatalf("%s %q: %q has a query or fragment", kind, name, p)
			}

			raw, err := url.PathUnescape(p)
			if err != nil {
				t.Fatalf("%s %q: %q does not unescape: %v", kind, name, p, err)
			}

			// The repository is the caller's own argument and is escaped on the wire;
			// the part a lockfile controls is what follows it.
			rest := strings.TrimPrefix(raw, "/repos/"+repo+"/")
			if strings.ContainsFunc(p, unicode.IsControl) && !strings.ContainsFunc(repo, unicode.IsControl) ||
				strings.ContainsFunc(rest, unicode.IsControl) {
				t.Fatalf("%s %q: %q has a control character", kind, name, p)
			}

			for seg := range strings.SplitSeq(rest, "/") {
				if seg == ".." || seg == "." {
					t.Fatalf("%s %q: %q has a dot segment", kind, name, p)
				}
			}
		}
	})
}
