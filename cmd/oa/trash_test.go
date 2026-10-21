// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// registerTrashRoutes mirrors the trash endpoints of the REST API (see
// docs/operations/trash.md) so the contract tests can tell a wrong CLI path
// from a served one.
func registerTrashRoutes(r chi.Router) {
	noop := func(http.ResponseWriter, *http.Request) {}

	r.Get("/api/v1/trash", noop)
	r.Post("/api/v1/trash/{kind}/{id}/restore", noop)
	r.Delete("/api/v1/trash/{kind}/{id}", noop)
	r.Get("/api/v1/repositories/{name}/trash", noop)
	r.Post("/api/v1/repositories/{name}/trash/{kind}/{id}/restore", noop)
}

func TestContractTrash(t *testing.T) {
	const list = `{"items":[{"id":"i1","kind":"maven_artifact","repository":"r","name":"g:a","version":"1.0","size_bytes":10,"deleted_at":"2026-01-01T00:00:00Z","deleted_by":"admin","purge_at":"2026-01-08T00:00:00Z"}],"total":1,"page":1,"limit":100}`

	runCases(t, []contractCase{
		{
			name:    "list all",
			args:    []string{"trash", "list"},
			replies: map[string]reply{"GET /api/v1/trash": ok(list)},
			calls:   []call{{method: "GET", pattern: "/api/v1/trash", query: map[string]string{"limit": "100", "page": "1"}}},
			out:     "maven_artifact",
		},
		{
			name:    "list one repo filtered by kind",
			args:    []string{"trash", "list", "--repo", "r", "--kind", "maven_artifact"},
			replies: map[string]reply{"GET /api/v1/repositories/{name}/trash": ok(list)},
			calls:   []call{{method: "GET", pattern: "/api/v1/repositories/{name}/trash", query: map[string]string{"kind": "maven_artifact", "limit": "100"}}},
			out:     "g:a",
		},
		{
			name:    "list json",
			args:    []string{"--format", "json", "trash", "list"},
			replies: map[string]reply{"GET /api/v1/trash": ok(list)},
			calls:   []call{{method: "GET", pattern: "/api/v1/trash"}},
			out:     `"purge_at"`,
		},
		{
			name:    "restore admin",
			args:    []string{"trash", "restore", "repository", "id1"},
			replies: map[string]reply{"POST /api/v1/trash/{kind}/{id}/restore": ok(`{"id":"id1","kind":"repository"}`)},
			calls:   []call{{method: "POST", pattern: "/api/v1/trash/{kind}/{id}/restore"}},
			out:     "Restored repository id1",
		},
		{
			name:    "restore through repo",
			args:    []string{"trash", "restore", "raw_artifact", "id2", "--repo", "r"},
			replies: map[string]reply{"POST /api/v1/repositories/{name}/trash/{kind}/{id}/restore": ok(`{"id":"id2"}`)},
			calls:   []call{{method: "POST", pattern: "/api/v1/repositories/{name}/trash/{kind}/{id}/restore"}},
			out:     "Restored raw_artifact id2",
		},
		{
			name:    "purge with yes",
			args:    []string{"trash", "purge", "docker_tag", "id3", "--yes"},
			replies: map[string]reply{"DELETE /api/v1/trash/{kind}/{id}": {status: 204}},
			calls:   []call{{method: "DELETE", pattern: "/api/v1/trash/{kind}/{id}"}},
			out:     "Purged docker_tag id3",
		},
		{
			name: "purge without yes fails non-interactively", args: []string{"trash", "purge", "docker_tag", "id3"}, fail: "--yes",
		},
		{
			name: "restore needs kind and id", args: []string{"trash", "restore", "repository"}, fail: "Usage: oa trash restore",
		},
	})
}

func TestResolveRepoConfirm(t *testing.T) {
	var prompted string

	prompt := func(s string) { prompted = s }

	tests := []struct {
		name        string
		given       string
		givenSet    bool
		interactive bool
		input       string
		want        string
		wantErr     error
	}{
		{name: "flag matches", given: "r", givenSet: true, want: "r"},
		{name: "flag mismatch", given: "x", givenSet: true, wantErr: errConfirmMismatch},
		{name: "empty flag mismatch", givenSet: true, wantErr: errConfirmMismatch},
		{name: "non-interactive without flag", wantErr: errConfirmNeeded},
		{name: "prompt typed correctly", interactive: true, input: "r\n", want: "r"},
		{name: "prompt typed wrongly", interactive: true, input: "nope\n", wantErr: errConfirmMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompted = ""

			got, err := resolveRepoConfirm("r", tt.given, tt.givenSet, tt.interactive, bufio.NewReader(strings.NewReader(tt.input)), prompt)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}

			if got != tt.want {
				t.Errorf("confirm = %q, want %q", got, tt.want)
			}

			if tt.interactive && !strings.Contains(prompted, "Type the repository name to confirm:") {
				t.Errorf("prompt = %q", prompted)
			}

			if !tt.interactive && prompted != "" {
				t.Errorf("unexpected prompt %q", prompted)
			}
		})
	}
}

func TestConfirmPurge(t *testing.T) {
	tests := []struct {
		name        string
		yes         bool
		interactive bool
		input       string
		wantErr     error
	}{
		{name: "yes flag skips prompt", yes: true},
		{name: "non-interactive needs yes", wantErr: errPurgeNeedsYes},
		{name: "typed yes", interactive: true, input: "yes\n"},
		{name: "typed no", interactive: true, input: "n\n", wantErr: errPurgeNotConfirmed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := confirmPurge("docker_tag", "id", tt.yes, tt.interactive, bufio.NewReader(strings.NewReader(tt.input)), func(string) {})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseRetention(t *testing.T) {
	tests := []struct {
		in      string
		want    any
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "30", want: 30},
		{in: "default", want: nil},
		{in: "-1", wantErr: true},
		{in: "x", wantErr: true},
		{in: "", wantErr: true},
	}

	for _, tt := range tests {
		got, err := parseRetention(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseRetention(%q) = %v, %v", tt.in, got, err)
		}
	}
}
