// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestCLIArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		argv     []string
		wantArgs []string
		wantCLI  bool
	}{
		{name: "no arguments at all", argv: nil},
		{name: "server", argv: []string{"/app/openartifact"}},
		{name: "server flag", argv: []string{"/app/openartifact", "-healthcheck"}},
		{name: "server subcommand", argv: []string{"openartifact", "rebuild", "x"}},
		{name: "linked as oa", argv: []string{"oa", "apply", "-f", "a.yaml"}, wantArgs: []string{"apply", "-f", "a.yaml"}, wantCLI: true},
		{name: "linked as oa by path", argv: []string{"/usr/local/bin/oa"}, wantArgs: []string{}, wantCLI: true},
		{
			name:     "oa subcommand",
			argv:     []string{"/app/openartifact", "oa", "repo", "list"},
			wantArgs: []string{"repo", "list"},
			wantCLI:  true,
		},
		{name: "oa is only the first argument", argv: []string{"openartifact", "-config", "oa"}},
		{name: "name that ends in oa", argv: []string{"/bin/koa", "version"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			args, ok := cliArgs(tc.argv)
			if ok != tc.wantCLI || !slices.Equal(args, tc.wantArgs) {
				t.Errorf("cliArgs(%q) = %q, %v; want %q, %v", tc.argv, args, ok, tc.wantArgs, tc.wantCLI)
			}
		})
	}
}

// TestServerBinaryAnswersAsOA runs the real main() both ways the image uses to
// reach the embedded CLI and expects `oa version`, not the server.
func TestServerBinaryAnswersAsOA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		argv []string
	}{
		{name: "as oa", argv: []string{"oa", "version", "--format", "json"}},
		{name: "oa subcommand", argv: []string{"openartifact", "oa", "version", "--format", "json"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.CommandContext(t.Context(), os.Args[0])
			cmd.Args = tc.argv
			cmd.Env = append(os.Environ(), runAsServerEnv+"=1", "HOME="+t.TempDir(), "OA_SERVER=", "OA_TOKEN=")

			var stdout, stderr bytes.Buffer

			cmd.Stdout, cmd.Stderr = &stdout, &stderr

			if err := cmd.Run(); err != nil {
				t.Fatalf("%q: %v\n%s", tc.argv, err, &stderr)
			}

			var info struct {
				Version string `json:"version"`
				Go      string `json:"go"`
			}

			if err := json.Unmarshal(stdout.Bytes(), &info); err != nil || info.Version != version || info.Go == "" {
				t.Errorf("%q printed %q (%v), want the CLI's version document for %q", tc.argv, stdout.String(), err, version)
			}
		})
	}
}
