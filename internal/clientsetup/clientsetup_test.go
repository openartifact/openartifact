// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package clientsetup

import (
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/middleware"
)

const origin = "https://artifacts.example.com"

func TestRepoBaseURL(t *testing.T) {
	tests := []struct{ format, repo, want string }{
		{"docker", "images", origin},
		{"npm", "web", origin + "/repos/web"},
		{"maven", "libs", origin + "/repos/maven/libs"},
		{"sbt", "libs", origin + "/repos/sbt/libs"},
		{"ivy", "libs", origin + "/repos/ivy/libs"},
		{"raw", "files", origin + "/repos/raw/files"},
		{"pypi", "my repo", origin + "/repos/my%20repo"},
	}

	for _, tt := range tests {
		if got := RepoBaseURL(tt.format, origin, tt.repo); got != tt.want {
			t.Errorf("RepoBaseURL(%s, %s) = %s, want %s", tt.format, tt.repo, got, tt.want)
		}
	}
}

func allFormats() []string {
	return []string{
		"npm", "docker", "pypi", "maven", "helm", "deb", "rpm", "cargo",
		"go", "nuget", "rubygems", "raw", "conda", "conan", "sbt", "ivy", "pacman",
	}
}

func TestSnippetsCoverEveryFormat(t *testing.T) {
	for _, format := range allFormats() {
		t.Run(format, func(t *testing.T) {
			snippets := Snippets(format, "hosted", origin+"/", "repo1")
			if len(snippets) == 0 {
				t.Fatal("no snippets")
			}

			for _, s := range snippets {
				if s.Client == "" || s.Configure == "" {
					t.Errorf("incomplete snippet: %+v", s)
				}

				if strings.Contains(s.Configure+s.Publish, "//artifacts.example.com//") {
					t.Errorf("origin trailing slash leaked into URL: %+v", s)
				}
			}
		})
	}
}

func TestSnippetsUnknownFormat(t *testing.T) {
	got := Snippets("mystery", "hosted", origin, "r")
	if len(got) != 1 || got[0].Client != "URL" || got[0].Configure != origin+"/repos/r/" {
		t.Fatalf("unexpected fallback: %+v", got)
	}
}

func TestSnippetsUsePlaceholdersOnly(t *testing.T) {
	for _, format := range allFormats() {
		for _, s := range Snippets(format, "hosted", origin, "r") {
			if strings.Contains(s.Configure+s.Publish, "<TOKEN>") || !strings.Contains(s.Configure+s.Publish, "TOKEN") {
				continue
			}

			t.Errorf("%s: token is not the <TOKEN> placeholder: %+v", format, s)
		}
	}
}

func TestDockerSnippetUsesRepoNameAsImage(t *testing.T) {
	s := Snippets("docker", "hosted", "http://localhost:8080", "team/app")[0]

	if !strings.Contains(s.Publish, "localhost:8080/team/app:latest") {
		t.Errorf("publish = %q", s.Publish)
	}
}

// sbt only sends credentials when the realm in Credentials(...) equals the
// realm of the server's WWW-Authenticate challenge.
func TestSBTSnippetUsesServerAuthRealm(t *testing.T) {
	want := `Credentials("` + middleware.AuthRealm + `", "`

	snips := Snippets("sbt", "hosted", "https://artifacts.example.com", "repo1")
	if len(snips) != 1 || !strings.Contains(snips[0].Configure, want) {
		t.Errorf("sbt snippet = %+v, want it to contain %s", snips, want)
	}
}

// sbt looks credentials up by host name alone: a host:port entry is never found.
func TestSBTSnippetCredentialsHostHasNoPort(t *testing.T) {
	tests := []struct{ origin, host string }{
		{"https://artifacts.example.com", "artifacts.example.com"},
		{"http://127.0.0.1:18098", "127.0.0.1"},
		{"https://artifacts.example.com:8443/", "artifacts.example.com"},
		{"http://[::1]:8080", "[::1]"},
	}

	for _, tt := range tests {
		want := `, "` + tt.host + `", "<USERNAME>"`

		if got := Snippets("sbt", "hosted", tt.origin, "repo1")[0].Configure; !strings.Contains(got, want) {
			t.Errorf("origin %s: configure = %q, want it to contain %s", tt.origin, got, want)
		}
	}
}

func TestPacmanProxySnippet(t *testing.T) {
	got := Snippets("pacman", "proxy", "https://oa.example", "mirror")
	if len(got) != 1 || got[0].Publish != "" {
		t.Fatalf("want one read-only snippet, got %+v", got)
	}

	for _, want := range []string{
		"# top of /etc/pacman.d/mirrorlist\nServer = https://oa.example/repos/mirror/$repo/os/$arch",
		"/repos/mirror/$repo/os/$arch",
		"sudo pacman -Syu",
	} {
		if !strings.Contains(got[0].Configure, want) {
			t.Errorf("configure missing %q:\n%s", want, got[0].Configure)
		}
	}

	if strings.Contains(got[0].Configure, "pacman-key") {
		t.Error("proxy snippet must not import a key from this server")
	}
}

func TestPacmanGroupSnippet(t *testing.T) {
	got := Snippets("pacman", "group", "https://oa.example", "all")
	if len(got) != 1 || got[0].Publish != "" {
		t.Fatalf("want one read-only snippet, got %+v", got)
	}

	for _, want := range []string{
		"[all]\nSigLevel = Required\nServer = https://oa.example/repos/all/os/$arch",
		"curl -fsSL https://oa.example/repos/all/public.key | sudo pacman-key --add -",
		"https://oa.example/repos/<member>/public.key",
	} {
		if !strings.Contains(got[0].Configure, want) {
			t.Errorf("configure missing %q:\n%s", want, got[0].Configure)
		}
	}
}
