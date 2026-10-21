// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package clientsetup

import (
	"strings"
	"testing"
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
		"gomod", "nuget", "rubygems", "raw", "conda", "conan", "sbt", "ivy",
	}
}

func TestSnippetsCoverEveryFormat(t *testing.T) {
	for _, format := range allFormats() {
		t.Run(format, func(t *testing.T) {
			snippets := Snippets(format, origin+"/", "repo1")
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
	got := Snippets("mystery", origin, "r")
	if len(got) != 1 || got[0].Client != "URL" || got[0].Configure != origin+"/repos/r/" {
		t.Fatalf("unexpected fallback: %+v", got)
	}
}

func TestSnippetsUsePlaceholdersOnly(t *testing.T) {
	for _, format := range allFormats() {
		for _, s := range Snippets(format, origin, "r") {
			if strings.Contains(s.Configure+s.Publish, "<TOKEN>") || !strings.Contains(s.Configure+s.Publish, "TOKEN") {
				continue
			}

			t.Errorf("%s: token is not the <TOKEN> placeholder: %+v", format, s)
		}
	}
}

func TestDockerSnippetUsesRepoNameAsImage(t *testing.T) {
	s := Snippets("docker", "http://localhost:8080", "team/app")[0]

	if !strings.Contains(s.Publish, "localhost:8080/team/app:latest") {
		t.Errorf("publish = %q", s.Publish)
	}
}
