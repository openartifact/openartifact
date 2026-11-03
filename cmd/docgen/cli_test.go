// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"reflect"
	"strings"
	"testing"
)

const rootHelp = `oa is the client.

Reads OA_TOKEN too.

Usage:
  oa [flags]
  oa [command]

Auth:
  auth        Manage credentials

Repositories:
  repo        Manage repositories

Utility:
  completion  Generate the shell completion script
  help        Help about any command

Flags:
  -h, --help            help for oa
      --server string   Server URL (default http://localhost:8080, env OA_SERVER)
      --version         Print the version

Use "oa [command] --help" for more information about a command.
`

const createHelp = `Create a hosted repository.

Install it for every session:

  oa repo create x

Usage:
  oa repo create <name> --format <fmt> [flags]

Aliases:
  create, new

Examples:
  oa repo create r --format npm

Flags:
      --format string        Repository format (required)
  -h, --help                 help for create
      --public               Public repository
      --trash-retention-days string   Days (default "x")

Global Flags:
      --server string   Server URL
`

func TestParseHelp(t *testing.T) {
	t.Parallel()

	d, err := parseHelp(rootHelp)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(d.Desc, "oa is the client.") || !strings.Contains(d.Desc, "OA_TOKEN") {
		t.Errorf("desc = %q", d.Desc)
	}

	if !reflect.DeepEqual(d.UseLines, []string{"oa [flags]"}) {
		t.Errorf("use lines = %q", d.UseLines)
	}

	var titles []string
	for _, g := range d.Groups {
		titles = append(titles, g.Title)
	}

	if want := []string{"Auth", "Repositories", "Utility"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("groups = %v, want %v", titles, want)
	}

	if got := d.Groups[2].Commands; len(got) != 2 || got[0].Name != "completion" || got[0].Short != "Generate the shell completion script" {
		t.Errorf("utility commands = %+v", got)
	}

	if len(d.Flags) != 2 || d.Flags[0].Name != "--server" || d.Flags[0].Value != "string" || d.Flags[1].Name != "--version" {
		t.Errorf("flags = %+v", d.Flags)
	}
}

func TestParseHelpLeaf(t *testing.T) {
	t.Parallel()

	d, err := parseHelp(createHelp)
	if err != nil {
		t.Fatal(err)
	}

	// A description line that looks like a section heading stays description.
	if !strings.Contains(d.Desc, "Install it for every session:") || len(d.Groups) != 0 {
		t.Errorf("desc = %q, groups = %+v", d.Desc, d.Groups)
	}

	if !reflect.DeepEqual(d.Aliases, []string{"create", "new"}) || d.Examples != "oa repo create r --format npm" {
		t.Errorf("aliases = %v, examples = %q", d.Aliases, d.Examples)
	}

	if len(d.Flags) != 3 || d.Flags[2].Name != "--trash-retention-days" || !strings.Contains(d.Flags[2].Desc, `(default "x")`) {
		t.Errorf("flags = %+v", d.Flags)
	}

	if len(d.Inherited) != 1 || d.Inherited[0].Name != "--server" {
		t.Errorf("inherited = %+v", d.Inherited)
	}
}

func TestParseHelpWithoutUsageFails(t *testing.T) {
	t.Parallel()

	if _, err := parseHelp("nothing here\n"); err == nil {
		t.Error("a help text without Usage must fail")
	}
}

func TestEnvMentionsAndRender(t *testing.T) {
	t.Parallel()

	root, err := parseHelp(rootHelp)
	if err != nil {
		t.Fatal(err)
	}

	create, err := parseHelp(createHelp)
	if err != nil {
		t.Fatal(err)
	}

	create.Path, create.Short = []string{"repo", "create"}, "Create a repository"

	repo := &cmdDoc{
		Path:     []string{"repo"},
		Short:    "Manage repositories",
		Desc:     "Manage repositories",
		UseLines: []string{"oa repo"},
		Kids:     []*cmdDoc{create},
	}
	auth := &cmdDoc{Path: []string{"auth"}, Short: "Manage credentials", Desc: "Manage credentials", UseLines: []string{"oa auth"}}
	root.Kids = []*cmdDoc{auth, repo}

	env := envMentions(root)
	want := map[string][]string{"OA_SERVER": {"`--server`"}}

	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}

	page, err := renderCLI(root)
	if err != nil {
		t.Fatal(err)
	}

	out := string(page)
	for _, w := range []string{
		"# CLI reference",
		"(../integrations/cli.md)",
		"## Auth\n",
		"## Repositories\n",
		"### `oa repo`\n",
		"#### `oa repo create`\n",
		"Create a repository.\n",
		"| `--format` | `string` | Repository format (required) |",
		"Aliases: `create`, `new`.",
		"| `OA_SERVER` | `--server` |",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("page lacks %q:\n%s", w, out)
		}
	}

	if strings.Contains(out, "| `--help`") || strings.Contains(out, "`-h`") {
		t.Error("the --help flag must not be documented")
	}

	if _, err := renderCLI(&cmdDoc{}); err == nil {
		t.Error("an empty tree must fail")
	}
}
