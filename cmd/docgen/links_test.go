// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestSlug(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Installing `Trivy` and Grype": "installing-trivy-and-grype",
		"What's [new](x.md)? (v2.0)":   "whats-new-v20",
		"snake_case & more":            "snake_case--more",
		"Reset the **admin** password": "reset-the-admin-password",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMarkdown(t *testing.T) {
	t.Parallel()

	f := parseMarkdown([]byte("# A\n\n## Dup\n\n## Dup\n\n```\n# not a heading\n[x](gone.md)\n```\n\nSee [a](b.md#c) and `[x](nope.md)`.\n"))
	for _, a := range []string{"a", "dup", "dup-1"} {
		if !f.anchors[a] {
			t.Errorf("missing anchor %q in %v", a, f.anchors)
		}
	}

	if f.anchors["not-a-heading"] {
		t.Error("heading inside a code fence was indexed")
	}

	if len(f.links) != 1 || f.links[0].target != "b.md#c" {
		t.Errorf("links = %v, want only b.md#c", f.links)
	}
}
