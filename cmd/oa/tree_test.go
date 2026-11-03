// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagValueUse matches a flag and its value placeholder in a Use line
// ("--format <fmt>"), which is not a positional argument.
var flagValueUse = regexp.MustCompile(`--[a-z-]+ <[^>]*>`)

// walkCommands returns every command of the tree below (and excluding) root.
func walkCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command

	var visit func(c *cobra.Command)

	visit = func(c *cobra.Command) {
		for _, k := range c.Commands() {
			out = append(out, k)
			visit(k)
		}
	}

	visit(root)

	return out
}

// words are the command words after the program name ("repo", "create").
func words(c *cobra.Command) []string {
	return strings.Fields(strings.TrimPrefix(c.CommandPath(), c.Root().Name()))
}

func isLeaf(c *cobra.Command) bool { return !c.HasSubCommands() }

// shorthands are the only one-letter flags the CLI has: -h (help), -f/-R
// (apply and diff), -o (output), -r (--repo), -y (--yes).
var allowedShorthands = []string{"h", "f", "R", "o", "r", "y"} //nolint:gochecknoglobals // immutable test table

// Every command is documented and validated: a one-line summary, an argument
// validator, completion for positionals, and flags that follow the rules.
func TestCommandTreeIsComplete(t *testing.T) {
	root := newRootCmd()
	cmds := walkCommands(root)

	if len(cmds) < 100 {
		t.Fatalf("only %d commands in the tree", len(cmds))
	}

	for _, c := range cmds {
		name := c.CommandPath()

		if strings.TrimSpace(c.Short) == "" {
			t.Errorf("%s: no Short help", name)
		}

		if c.Short != "" && (!unicode.IsUpper(rune(c.Short[0])) || strings.HasSuffix(c.Short, ".")) {
			t.Errorf("%s: Short %q must start with a capital letter and not end with a period", name, c.Short)
		}

		if first, _, _ := strings.Cut(c.Use, " "); first != c.Name() {
			t.Errorf("%s: Use %q does not start with the command name", name, c.Use)
		}

		if c.RunE == nil && c.Run == nil {
			t.Errorf("%s: not runnable (a group must be built with parentCmd)", name)
		}

		if c.Args == nil {
			t.Errorf("%s: no Args validation", name)
		}

		if isLeaf(c) && strings.Contains(flagValueUse.ReplaceAllString(c.Use, ""), "<") && c.ValidArgsFunction == nil &&
			len(c.ValidArgs) == 0 {
			t.Errorf("%s: takes positional arguments but has no completion", name)
		}

		c.Flags().VisitAll(func(f *pflag.Flag) { checkFlag(t, name, f) })
	}
}

func checkFlag(t *testing.T, cmd string, f *pflag.Flag) {
	t.Helper()

	if f.Name == "help" {
		return
	}

	if len(f.Name) < 2 {
		t.Errorf("%s: flag --%s: long names have at least two letters (use a shorthand for one-letter forms)", cmd, f.Name)
	}

	if f.Usage == "" && !f.Hidden {
		t.Errorf("%s: flag --%s has no usage text", cmd, f.Name)
	}

	if f.Shorthand != "" && !slices.Contains(allowedShorthands, f.Shorthand) {
		t.Errorf("%s: flag --%s has shorthand -%s, not in %v", cmd, f.Name, f.Shorthand, allowedShorthands)
	}
}

// Group commands only hold subcommands: run alone or with an unknown word they
// fail with exit 1 and a usage text, never silently succeed.
func TestGroupsRequireASubcommand(t *testing.T) {
	srv, requests := countingServer(t)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("OA_SERVER", srv.URL)
	t.Setenv("OA_TOKEN", "t")

	for _, c := range walkCommands(newRootCmd()) {
		if isLeaf(c) {
			continue
		}

		var out, errb bytes.Buffer

		if code := run(
			append(words(c), "nosuchsubcommand"),
			&out,
			&errb,
		); code != 1 ||
			!strings.Contains(errb.String(), "unknown command") {
			t.Errorf("oa %s nosuchsubcommand: exit %d, stderr %q", strings.Join(words(c), " "), code, errb.String())
		}

		out.Reset()
		errb.Reset()

		// `oa system` is a group whose own action shows the system info.
		if c.Name() == "system" {
			continue
		}

		if code := run(words(c), &out, &errb); code != 1 || !strings.Contains(errb.String(), "Usage:") {
			t.Errorf("oa %s: exit %d, stderr %q, want a usage failure", strings.Join(words(c), " "), code, errb.String())
		}
	}

	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests were made", n)
	}
}

// A mistyped command word, at the root or in a group, names the closest
// commands.
func TestUnknownCommandSuggestsTheClosest(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"reop", "list"}, "\trepo"},
		{[]string{"rep"}, "\trepo"},
		{[]string{"repo", "shw", "x"}, "\tshow"},
		{[]string{"trash", "purg"}, "\tpurge"},
	} {
		var out, errb bytes.Buffer

		if code := run(tc.args, &out, &errb); code != 1 ||
			!strings.Contains(errb.String(), "Did you mean this?") || !strings.Contains(errb.String(), tc.want) {
			t.Errorf("oa %s: exit %d, stderr %q, want a suggestion %q", strings.Join(tc.args, " "), code, errb.String(), tc.want)
		}
	}
}

// Long flags need two dashes: -name, where name is a long flag of the command,
// is refused with a hint (pflag would read it as bundled shorthands, one of which
// may swallow the rest as its value), and makes no request.
func TestSingleDashLongFlagIsRefused(t *testing.T) {
	srv, requests := countingServer(t)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("OA_SERVER", srv.URL)
	t.Setenv("OA_TOKEN", "t")

	for _, c := range append(walkCommands(newRootCmd()), newRootCmd()) {
		seen := map[string]bool{}

		for _, fs := range []*pflag.FlagSet{c.LocalFlags(), c.InheritedFlags()} {
			fs.VisitAll(func(f *pflag.Flag) {
				if seen[f.Name] || len(f.Name) < 2 {
					return
				}

				seen[f.Name] = true

				args := append(words(c), "-"+f.Name)

				var out, errb bytes.Buffer

				code := run(args, &out, &errb)
				if code != 1 || !strings.Contains(errb.String(), "did you mean --"+f.Name+"?") {
					t.Errorf("oa %s: exit %d, stderr %q, want the --%s hint", strings.Join(args, " "), code, errb.String(), f.Name)
				}
			})
		}
	}

	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests were made", n)
	}
}

// `-name=value` and a value in the next argument do not hide the refusal, and
// values of flags are never taken for flags.
func TestSingleDashLongFlagForms(t *testing.T) {
	srv, _ := countingServer(t)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("OA_SERVER", srv.URL)
	t.Setenv("OA_TOKEN", "t")

	for _, args := range [][]string{
		{"repo", "delete", "x", "-confirm=x"},
		{"-format", "json", "repo", "list"},
		{"repo", "list", "-server", "http://x"},
		{"apply", "-format", "x"},
	} {
		var out, errb bytes.Buffer

		if code := run(args, &out, &errb); code != 1 || !strings.Contains(errb.String(), "did you mean --") {
			t.Errorf("oa %s: exit %d, stderr %q", strings.Join(args, " "), code, errb.String())
		}
	}

	// A flag value that looks like a flag is a value (this one reaches the
	// server, so it runs as a real process).
	res := runOA(t, srv, "", "repo", "update", "x", "--description", "-format")
	if strings.Contains(res.stderr, "did you mean") || strings.Contains(res.stderr, "unknown") {
		t.Errorf("a flag value was taken for a flag: exit %d, stderr %q", res.code, res.stderr)
	}
}
