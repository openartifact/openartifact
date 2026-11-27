// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2

import "testing"

func TestRelocate(t *testing.T) {
	t.Parallel()

	const upstream = "https://updates.example.org/releases/2024-12"

	tests := []struct {
		name, key, location, want string
		changed                   bool
	}{
		{"relative child stays", "compositeContent.xml", "202412041000", "202412041000", false},
		{"relative child with slash stays", "compositeContent.xml", "child/", "child/", false},
		{"escapes the repository", "compositeContent.xml", "../../technology/epp/2024-12/", "_host/technology/epp/2024-12/", true},
		{
			"absolute inside the base",
			"compositeContent.xml",
			"https://updates.example.org/releases/2024-12/202412041000",
			"202412041000",
			true,
		},
		{"absolute inside, trailing slash", "compositeContent.xml", "https://updates.example.org/releases/2024-12/a/b/", "a/b/", true},
		{"absolute outside the base", "compositeContent.xml", "https://updates.example.org/other/site", "_host/other/site", true},
		{"plain http on the same host", "compositeContent.xml", "http://updates.example.org/releases/2024-12/x", "x", true},
		{"another host", "compositeContent.xml", "https://other.example.net/repo/", "https://other.example.net/repo/", false},
		{"another port", "compositeContent.xml", "https://updates.example.org:8443/repo/", "https://updates.example.org:8443/repo/", false},
		{
			"empty segment stays",
			"compositeContent.xml",
			"https://updates.example.org/releases/2024-12/a//b/",
			"https://updates.example.org/releases/2024-12/a//b/",
			false,
		},
		{"query", "compositeContent.xml", "https://updates.example.org/dl.php?file=x", "https://updates.example.org/dl.php?file=x", false},
		{"from a subdirectory to a sibling", "a/b/compositeContent.xml", "../c/", "../c/", false},
		{
			"from a subdirectory, absolute sibling",
			"a/b/compositeContent.xml",
			"https://updates.example.org/releases/2024-12/a/c",
			"../c",
			true,
		},
		{"from a subdirectory to the root", "a/b/compositeContent.xml", "https://updates.example.org/releases/2024-12/x", "../../x", true},
		{"the repository itself", "a/compositeContent.xml", "https://updates.example.org/releases/2024-12/", "../", true},
		{"escape from the host namespace", "_host/technology/epp/compositeContent.xml", "../../releases/2024-12/x", "../../../x", true},
		{"relative inside the host namespace", "_host/technology/epp/compositeContent.xml", "2024-12", "2024-12", false},
	}

	for _, tt := range tests {
		doc := upstream + "/" + tt.key
		if len(tt.key) > 6 && tt.key[:6] == HostPrefix+"/" {
			doc = "https://updates.example.org/" + tt.key[len(HostPrefix)+1:]
		}

		r, err := NewRelocator(upstream, doc, tt.key)
		if err != nil {
			t.Fatal(err)
		}

		got, changed := r.Relocate(tt.location)
		if got != tt.want || changed != tt.changed {
			t.Errorf("%s: Relocate(%q) = %q, %v; want %q, %v", tt.name, tt.location, got, changed, tt.want, tt.changed)
		}
	}
}

func TestUpstreamURL(t *testing.T) {
	t.Parallel()

	tests := []struct{ upstream, key, want string }{
		{"https://h.example/releases/2024-12", "plugins/a_1.jar", "https://h.example/releases/2024-12/plugins/a_1.jar"},
		{"https://h.example/releases/2024-12/", "p2.index", "https://h.example/releases/2024-12/p2.index"},
		{"https://h.example/releases/2024-12", "_host/technology/epp/content.jar", "https://h.example/technology/epp/content.jar"},
		{"http://h.example:8080/r", "_host/x%3Fy/a", "http://h.example:8080/x%253Fy/a"},
	}

	for _, tt := range tests {
		got, ok := UpstreamURL(tt.upstream, tt.key)
		if !ok || got != tt.want {
			t.Errorf("UpstreamURL(%q, %q) = %q, %v; want %q", tt.upstream, tt.key, got, ok, tt.want)
		}
	}

	if _, ok := UpstreamURL("ftp://h/", "x"); ok {
		t.Error("an ftp upstream is not a p2 upstream")
	}

	if _, ok := UpstreamURL("https://h/", HostPrefix+"/"); ok {
		t.Error("the host namespace without a path is not a file")
	}
}
