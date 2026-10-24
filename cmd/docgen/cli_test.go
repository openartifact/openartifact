// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"reflect"
	"strings"
	"testing"
)

const sampleHelp = `OpenArtifact CLI vdev

Usage:
  oa [--server URL] <command> [args]

Global flags:
  --server    Server URL  (default: http://localhost:8080, env: OA_SERVER)

Auth:
  auth login [--username U]
                                     Login (env OA_PASS)
  auth token list                    List tokens

Migration:
  import nexus|artifactory --source-url URL  Import

System:
  system info                        Show system information

  version                            Show CLI version
`

func TestParseCLIHelp(t *testing.T) {
	t.Parallel()

	h, err := parseCLIHelp(sampleHelp)
	if err != nil {
		t.Fatal(err)
	}

	if got := h.Usage.Lines; len(got) != 1 || got[0] != "oa [--server URL] <command> [args]" {
		t.Errorf("usage = %q", got)
	}

	if got := h.Global.Lines; len(got) != 1 || !strings.HasPrefix(got[0], "--server") {
		t.Errorf("global = %q", got)
	}

	var titles []string
	for _, s := range h.Sections {
		titles = append(titles, s.Title)
	}

	if want := []string{"Auth", "Migration", "System", "Utility"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("titles = %v, want %v", titles, want)
	}

	// continuation lines keep their alignment relative to the dedented entry
	if got := h.Sections[0].Lines[1]; !strings.HasPrefix(got, strings.Repeat(" ", 35)+"Login") {
		t.Errorf("continuation = %q", got)
	}
}

func TestParseCLIHelpErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty":              "",
		"no sections":        "Usage:\n  oa x\n\nGlobal flags:\n  --a  b\n",
		"stray text":         "Usage:\n  oa x\n\nhello\n",
		"no usage or global": "Auth:\n  auth login\n",
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := parseCLIHelp(in); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestEntryPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want [][]string
	}{
		{"repo list                          List repositories", [][]string{{"repo", "list"}}},
		{"repo create <name> --format <fmt>", [][]string{{"repo", "create"}}},
		{"auth token create --name N", [][]string{{"auth", "token", "create"}}},
		{"webhook repo list <repo>", [][]string{{"webhook", "repo", "list"}}},
		{"search <query> [--repo R]", [][]string{{"search"}}},
		{"diff  -f FILE", [][]string{{"diff"}}},
		{"import nexus|artifactory --source-url URL", [][]string{{"import", "nexus"}, {"import", "artifactory"}}},
		{"dtrack show|test|pushes <repo>", [][]string{{"dtrack", "show"}, {"dtrack", "test"}, {"dtrack", "pushes"}}},
		{"                                   continuation line", nil},
		{"--server    Server URL", nil},
		{"", nil},
	}

	for _, tc := range tests {
		if got := entryPaths(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("entryPaths(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseFlagUsage(t *testing.T) {
	t.Parallel()

	in := "Usage of apply:\n" +
		"  -R\tDescend into subdirectories\n" +
		"  -f value\n" +
		"    \tManifest file, directory or - for stdin (repeatable)\n" +
		"  -o string\n" +
		"    \tOutput format\n" +
		"    \t(default: human readable)\n"

	got, err := parseFlagUsage(in)
	if err != nil {
		t.Fatal(err)
	}

	want := []flagDoc{
		{Name: "-R", Description: "Descend into subdirectories"},
		{Name: "-f", Value: "value", Description: "Manifest file, directory or - for stdin (repeatable)"},
		{Name: "-o", Value: "string", Description: "Output format (default: human readable)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	for name, bad := range map[string]string{
		"header only": "Usage of x:\n",
		"not usage":   "Error: nope\n",
		"stray line":  "Usage of x:\nsomething else\n",
		"odd flag":    "Usage of x:\n  -a b c\n",
	} {
		if _, err := parseFlagUsage(bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestFlagListing(t *testing.T) {
	t.Parallel()

	flags, raw := flagListing("Usage of import:\n  -a\tdesc\nError: flag: help requested\n")
	if raw != "" || len(flags) != 1 {
		t.Fatalf("flags = %v raw = %q", flags, raw)
	}

	if flags, raw := flagListing("Error: Unknown repo subcommand: --help\n"); flags != nil || raw != "" {
		t.Errorf("an error message must yield nothing, got %v %q", flags, raw)
	}

	if flags, raw := flagListing("Usage of x:\nfree text\n"); flags != nil || !strings.Contains(raw, "free text") {
		t.Errorf("unparseable listing must be kept verbatim, got %v %q", flags, raw)
	}
}

func TestRichUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{"one line", "Error: Usage: oa auth <login>\n", ""},
		{"multi line", "Error: Usage: oa trash <list>\n  oa trash purge <id>\n", "Usage: oa trash <list>\n  oa trash purge <id>"},
		{"network", "Error: Get \"http://x\": dial tcp: connection refused\n", ""},
		{"unknown command", "Unknown command: show\nOpenArtifact CLI vdev\n", ""},
	}

	for _, tc := range tests {
		if got := richUsage(tc.in); got != tc.want {
			t.Errorf("%s: richUsage = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEnvMentionsAndRender(t *testing.T) {
	t.Parallel()

	h, err := parseCLIHelp(sampleHelp)
	if err != nil {
		t.Fatal(err)
	}

	probes := []cmdProbe{
		{Path: []string{"import", "nexus"}, Flags: []flagDoc{{Name: "-source-user", Value: "string", Description: "user (or OA_IMPORT_USER)"}}},
		{Path: []string{"import", "artifactory"}, Flags: []flagDoc{{Name: "-source-user", Value: "string", Description: "user (or OA_IMPORT_USER)"}}},
		{Path: []string{"auth"}, Usage: "Usage: oa auth <login>\n  second line"},
	}

	env := envMentions(h, probes)

	want := map[string][]string{
		"OA_SERVER":      {"`--server`"},
		"OA_PASS":        {"`oa auth login`"},
		"OA_IMPORT_USER": {"`oa import artifactory`", "`oa import nexus`"},
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}

	page, err := renderCLI(h, probes)
	if err != nil {
		t.Fatal(err)
	}

	out := string(page)
	for _, w := range []string{
		"# CLI reference",
		"(../integrations/cli.md)",
		"## Auth\n",
		"### `oa auth`\n",
		"### `oa import nexus`\n",
		"| `-source-user` | `string` | user (or OA_IMPORT_USER) |",
		"Same flags as `oa import nexus`.",
		"## Utility\n",
		"| `OA_SERVER` | `--server` |",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("page lacks %q:\n%s", w, out)
		}
	}

	if strings.Contains(out, "OpenArtifact CLI v") {
		t.Error("version line must not appear")
	}

	if _, err := renderCLI(cliHelp{Global: helpSection{Lines: []string{"--x y"}}}, nil); err == nil {
		t.Error("help without any OA_* variable must fail")
	}
}
