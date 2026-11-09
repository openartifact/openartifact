// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"reflect"
	"testing"
)

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  SearchFilter
	}{
		{
			name:  "empty string",
			input: "",
			want:  SearchFilter{},
		},
		{
			name:  "single free-text word",
			input: "lodash",
			want:  SearchFilter{FreeText: "lodash"},
		},
		{
			name:  "format token",
			input: "format:npm",
			want:  SearchFilter{Formats: []string{"npm"}},
		},
		{
			name:  "repo token",
			input: "repo:my-repo",
			want:  SearchFilter{Repos: []string{"my-repo"}},
		},
		{
			name:  "project and project_role tokens",
			input: "lodash project:acme project:globex project_role:viewer",
			want: SearchFilter{
				FreeText: "lodash", Projects: []string{"acme", "globex"}, ProjectRoles: []string{"viewer"},
			},
		},
		{
			name:  "bare role is not a qualifier",
			input: "role:viewer",
			want:  SearchFilter{FreeText: "role:viewer"},
		},
		{
			name:  "sha256 token",
			input: "sha256:abc123",
			want:  SearchFilter{SHA256Prefix: "abc123"},
		},
		{
			name:  "name token",
			input: "name:lodash",
			want:  SearchFilter{Name: "lodash"},
		},
		{
			name:  "mixed query free-text and key-value",
			input: "lodash format:npm repo:my-repo",
			want:  SearchFilter{FreeText: "lodash", Formats: []string{"npm"}, Repos: []string{"my-repo"}},
		},
		{
			name:  "unrecognized key treated as free-text",
			input: "unknown:value foo",
			want:  SearchFilter{FreeText: "unknown:value foo"},
		},
		{
			name:  "multiple format tokens accumulate",
			input: "format:npm format:maven",
			want:  SearchFilter{Formats: []string{"npm", "maven"}},
		},
		{
			name:  "multiple repo tokens accumulate",
			input: "repo:alpha repo:beta",
			want:  SearchFilter{Repos: []string{"alpha", "beta"}},
		},
		{
			name:  "all keys combined",
			input: "my-lib name:mylib format:rpm repo:releases sha256:deadbeef",
			want: SearchFilter{
				FreeText:     "my-lib",
				Name:         "mylib",
				Formats:      []string{"rpm"},
				Repos:        []string{"releases"},
				SHA256Prefix: "deadbeef",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseQuery(tt.input)
			if tt.want.FreeText != got.FreeText {
				t.Errorf("FreeText: want %q, got %q", tt.want.FreeText, got.FreeText)
			}

			if tt.want.Name != got.Name {
				t.Errorf("Name: want %q, got %q", tt.want.Name, got.Name)
			}

			if tt.want.SHA256Prefix != got.SHA256Prefix {
				t.Errorf("SHA256Prefix: want %q, got %q", tt.want.SHA256Prefix, got.SHA256Prefix)
			}

			if !reflect.DeepEqual(tt.want.Formats, got.Formats) {
				t.Errorf("Formats: want %v, got %v", tt.want.Formats, got.Formats)
			}

			if !reflect.DeepEqual(tt.want.Repos, got.Repos) {
				t.Errorf("Repos: want %v, got %v", tt.want.Repos, got.Repos)
			}
		})
	}
}
