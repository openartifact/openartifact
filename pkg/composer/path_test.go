// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer_test

import (
	"testing"

	"github.com/openartifact/openartifact/pkg/composer"
)

func TestParsePath(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		path string
		want composer.Path
		ok   bool
	}{
		{"/packages.json", composer.Path{Kind: composer.KindRoot}, true},
		{"/list.json", composer.Path{Kind: composer.KindList}, true},
		{"/search.json", composer.Path{Kind: composer.KindSearch}, true},
		{"/p2/acme/tools.json", composer.Path{Kind: composer.KindMetadata, Name: "acme/tools"}, true},
		{"/p2/acme/tools~dev.json", composer.Path{Kind: composer.KindMetadata, Name: "acme/tools", Dev: true}, true},
		{
			"/packages/upload/acme/tools/dev-feature%2Fx",
			composer.Path{Kind: composer.KindUpload, Name: "acme/tools", Version: "dev-feature/x"}, true,
		},
		{"/dists/acme/tools/1.0.0/abc.zip", composer.Path{Kind: composer.KindDist, Name: "acme/tools", Version: "1.0.0", Ref: "abc"}, true},
		{
			"/dists/acme/tools/dev-feature/x/abc.zip",
			composer.Path{Kind: composer.KindDist, Name: "acme/tools", Version: "dev-feature/x", Ref: "abc"}, true,
		},
		{"/dists/acme/tools/1.0.0/abc.tar", composer.Path{}, false},
		{"/p2/acme/Tools.json", composer.Path{}, false},
		{"/p2/acme/tools.json/x", composer.Path{}, false},
		{"/packages/upload/acme/tools", composer.Path{}, false},
		{"/dists/acme/tools/%zz/abc.zip", composer.Path{}, false},
	} {
		got, ok := composer.ParsePath(c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestDistPathAndRef(t *testing.T) {
	t.Parallel()

	p := composer.DistPath("acme/tools", "dev-feature/x", "abc123")
	if want := "dists/acme/tools/dev-feature%2Fx/abc123.zip"; p != want {
		t.Fatalf("DistPath = %q, want %q", p, want)
	}

	got, ok := composer.ParsePath(p)
	if !ok || got.Version != "dev-feature/x" || got.Ref != "abc123" {
		t.Errorf("ParsePath(DistPath) = %+v, %v", got, ok)
	}

	for url, want := range map[string]string{
		"https://oa.example/repos/php/" + p:                    "abc123",
		"https://oa.example/repos/dists/dists/a/b/1.0/f.zip":   "f",
		"https://oa.example/repos/php/dists/a/b/1.0/f.zip?x=1": "f",
	} {
		if got, ok := composer.DistRef(url); !ok || got != want {
			t.Errorf("DistRef(%q) = %q, %v; want %q", url, got, ok, want)
		}
	}

	for _, url := range []string{"", "https://x/y.zip", "https://x/dists/a/b/1.0/.zip", "https://x/a/b/1.0/f.zip", "https://x/dists/a/b/1.0/f.tar", "%zz"} {
		if got, ok := composer.DistRef(url); ok {
			t.Errorf("DistRef(%q) = %q, want none", url, got)
		}
	}
}
