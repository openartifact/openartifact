// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	cliPage = "cli.md"
	// cliDeadServer is the server the probed binary is pointed at: nothing
	// listens on port 1, so a command that got as far as a request would fail
	// at once instead of reaching a real server (help never makes one).
	cliDeadServer   = "http://127.0.0.1:1"
	cliProbeTimeout = 15 * time.Second
	cliBuildTimeout = 10 * time.Minute
	// cliMaxDepth stops a runaway walk if the command tree ever loops.
	cliMaxDepth = 6
)

func init() {
	register(generator{
		name: "cli",
		owns: func(rel string) bool { return rel == cliPage },
		run:  runCLI,
	})
}

// flagDoc is one flag of a `Flags:` listing.
type flagDoc struct {
	Name      string // --name
	Shorthand string // -n, or empty
	Value     string // the value type pflag prints (string, int, ...), empty for a bool
	Desc      string
}

// childDoc is one line of an `Available Commands:` listing.
type childDoc struct {
	Name, Short string
}

// helpGroup is one titled list of subcommands (cobra command group).
type helpGroup struct {
	Title    string
	Commands []childDoc
}

// cmdDoc is what `oa PATH --help` printed, parsed.
type cmdDoc struct {
	// Path is the command words after oa; empty for the root.
	Path []string
	// Desc is the description block (the Long text, or the Short when there is
	// no Long text).
	Desc     string
	UseLines []string
	Aliases  []string
	Examples string
	// Flags are the command's own flags without --help; Inherited are the
	// persistent flags of its parents.
	Flags     []flagDoc
	Inherited []flagDoc
	Groups    []helpGroup
	// Short is the command's one-line summary, from its parent's listing.
	Short string
	Kids  []*cmdDoc
}

func runCLI(root string) (map[string][]byte, error) {
	tmp, err := os.MkdirTemp("", "docgen-oa-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}

	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "oa")

	ctx, cancel := context.WithTimeout(context.Background(), cliBuildTimeout)
	defer cancel()

	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X main.version=dev", "-o", bin, "./cmd/oa")
	build.Dir = root

	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build oa: %w\n%s", err, out)
	}

	home := filepath.Join(tmp, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return nil, fmt.Errorf("temp home: %w", err)
	}

	r := &cliRunner{bin: bin, home: home}

	tree, err := walkCLI(r, nil, "", 0)
	if err != nil {
		return nil, err
	}

	page, err := renderCLI(tree)
	if err != nil {
		return nil, err
	}

	return map[string][]byte{cliPage: page}, nil
}

// cliRunner runs the oa binary in a sandbox: an empty HOME (the config file
// lives under it), no token, no stdin and a dead server address.
type cliRunner struct {
	bin, home string
}

// run returns the output of `oa args...`; a non-zero exit is an error, since
// help always succeeds.
func (r *cliRunner) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext( //nolint:gosec // the binary was built by this process, arguments come from its own help text
		ctx,
		r.bin,
		args...)
	cmd.Env = []string{
		"HOME=" + r.home, "USERPROFILE=" + r.home, "XDG_CONFIG_HOME=" + filepath.Join(r.home, ".config"),
		"TMPDIR=" + r.home, "OA_SERVER=" + cliDeadServer,
	}

	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("oa %s: timed out after %s", strings.Join(args, " "), cliProbeTimeout)
	}

	if err != nil {
		return "", fmt.Errorf("oa %s: %w", strings.Join(args, " "), err)
	}

	return strings.ReplaceAll(string(out), "\r\n", "\n"), nil
}

// walkCLI documents the command at path and, recursively, its subcommands.
func walkCLI(r *cliRunner, path []string, short string, depth int) (*cmdDoc, error) {
	if depth > cliMaxDepth {
		return nil, fmt.Errorf("oa %s: command tree deeper than %d levels", strings.Join(path, " "), cliMaxDepth)
	}

	text, err := r.run(append(slices.Clone(path), "--help")...)
	if err != nil {
		return nil, err
	}

	doc, err := parseHelp(text)
	if err != nil {
		return nil, fmt.Errorf("oa %s --help: %w", strings.Join(path, " "), err)
	}

	doc.Path, doc.Short = path, short

	for _, g := range doc.Groups {
		for _, c := range g.Commands {
			if len(path) == 0 && c.Name == "help" {
				continue
			}

			kid, err := walkCLI(r, append(slices.Clone(path), c.Name), c.Short, depth+1)
			if err != nil {
				return nil, err
			}

			doc.Kids = append(doc.Kids, kid)
		}
	}

	return doc, nil
}

var (
	cliSectionHead = regexp.MustCompile(`^[A-Z][A-Za-z ]*:$`)
	cliChildLine   = regexp.MustCompile(`^  ([a-z][a-z0-9-]*)\s{2,}(.+)$`)
	cliFlagLine    = regexp.MustCompile(`^\s+(?:(-\w), )?(--[\w-]+)(?:\[=[^\]]*\])?(?: (\S+))?\s{2,}(\S.*)$`)
)

// parseHelp parses the output of `oa PATH --help` (cobra's default template):
// the description, then Usage, Aliases, Examples, command lists, Flags and
// Global Flags sections.
func parseHelp(text string) (*cmdDoc, error) {
	doc := &cmdDoc{}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")

	var (
		section string
		desc    []string
		ex      []string
		group   *helpGroup
	)

	seenUsage := false

	for _, line := range lines {
		// Everything before "Usage:" is the description, whatever it looks like.
		if line == "Usage:" {
			seenUsage = true
		}

		if seenUsage && cliSectionHead.MatchString(line) {
			section = strings.TrimSuffix(line, ":")
			group = nil

			if section != "Usage" && section != "Aliases" && section != "Examples" && section != "Flags" && section != "Global Flags" {
				doc.Groups = append(doc.Groups, helpGroup{Title: section})
				group = &doc.Groups[len(doc.Groups)-1]
			}

			continue
		}

		if strings.HasPrefix(line, "Use \"") && strings.Contains(line, "--help") {
			section = ""

			continue
		}

		switch section {
		case "":
			desc = append(desc, line)
		case "Usage":
			if s := strings.TrimSpace(line); s != "" && !strings.HasSuffix(s, "[command]") {
				doc.UseLines = append(doc.UseLines, s)
			}
		case "Aliases":
			if s := strings.TrimSpace(line); s != "" {
				for a := range strings.SplitSeq(s, ",") {
					doc.Aliases = append(doc.Aliases, strings.TrimSpace(a))
				}
			}
		case "Examples":
			ex = append(ex, line)
		case "Flags", "Global Flags":
			if strings.TrimSpace(line) == "" {
				continue
			}

			m := cliFlagLine.FindStringSubmatch(line)
			switch {
			case m != nil:
				f := flagDoc{Name: m[2], Shorthand: m[1], Value: m[3], Desc: m[4]}

				if section == "Flags" {
					doc.Flags = append(doc.Flags, f)
				} else {
					doc.Inherited = append(doc.Inherited, f)
				}
			default:
				return nil, fmt.Errorf("unrecognised flag line %q", line)
			}
		default:
			if m := cliChildLine.FindStringSubmatch(line); m != nil && group != nil {
				group.Commands = append(group.Commands, childDoc{Name: m[1], Short: strings.TrimSpace(m[2])})
			}
		}
	}

	doc.Desc = strings.TrimSpace(strings.Join(desc, "\n"))
	doc.Examples = strings.TrimSpace(dedent(ex))

	if len(doc.UseLines) == 0 {
		return nil, errors.New("no Usage section: the help format changed")
	}

	doc.Flags = slices.DeleteFunc(doc.Flags, func(f flagDoc) bool { return f.Name == "--help" })
	doc.Groups = slices.DeleteFunc(doc.Groups, func(g helpGroup) bool { return len(g.Commands) == 0 })

	return doc, nil
}

func dedent(lines []string) string {
	indent := -1

	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}

		if n := len(l) - len(strings.TrimLeft(l, " ")); indent < 0 || n < indent {
			indent = n
		}
	}

	var b strings.Builder

	for _, l := range lines {
		if indent > 0 && len(l) >= indent {
			l = l[indent:]
		}

		b.WriteString(l + "\n")
	}

	return b.String()
}

var cliEnvVar = regexp.MustCompile(`\bOA_[A-Z0-9_]+\b`)

// envMentions maps each OA_* variable named anywhere in the help to the places
// naming it: global flags (`--server`) or command paths (`oa gpg import`).
func envMentions(tree *cmdDoc) map[string][]string {
	set := map[string]map[string]bool{}

	note := func(text, where string) {
		for _, v := range cliEnvVar.FindAllString(text, -1) {
			if set[v] == nil {
				set[v] = map[string]bool{}
			}

			set[v][where] = true
		}
	}

	for _, f := range tree.Flags {
		note(f.Desc, "`"+f.Name+"`")
	}

	var walk func(d *cmdDoc)

	walk = func(d *cmdDoc) {
		if len(d.Path) > 0 {
			where := "`oa " + strings.Join(d.Path, " ") + "`"
			note(d.Desc, where)
			note(d.Examples, where)

			for _, f := range d.Flags {
				note(f.Desc, where)
			}
		}

		for _, k := range d.Kids {
			walk(k)
		}
	}

	walk(tree)

	out := map[string][]string{}

	for v, places := range set {
		for w := range places {
			out[v] = append(out[v], w)
		}

		slices.Sort(out[v])
	}

	return out
}

func fence(b *bytes.Buffer, lines ...string) {
	b.WriteString("```text\n")

	for _, l := range lines {
		b.WriteString(l + "\n")
	}

	b.WriteString("```\n\n")
}

// renderCLI writes the page: global flags, environment, then one section per
// command group of `oa --help`, each top-level command with its subcommands.
func renderCLI(tree *cmdDoc) ([]byte, error) {
	env := envMentions(tree)
	if len(env) == 0 {
		return nil, errors.New("no OA_* environment variable found in the CLI help: the help text format changed")
	}

	if len(tree.Groups) == 0 {
		return nil, errors.New("oa --help lists no command: the help text format changed")
	}

	var b bytes.Buffer

	b.Write(header("CLI reference", "the output of `oa --help` and `oa COMMAND --help`"))
	b.WriteString(
		"`oa` is the command-line client of the REST API. For login, the config file and worked examples see " +
			"[CLI](../integrations/cli.md); for shell completion see [Shell completion](../integrations/cli.md#shell-completion). " +
			"Global flags may go anywhere on the command line. Long flags take two dashes (`--format json`); " +
			"put a name that starts with `-` after a bare `--` (`oa repo show -- -legacy`).\n\n",
	)

	b.WriteString("## Usage\n\n")
	fence(&b, tree.UseLines...)

	b.WriteString("## Global flags\n\n")

	if err := renderFlags(&b, "oa", tree.Flags); err != nil {
		return nil, err
	}

	renderEnv(&b, env)

	for _, g := range tree.Groups {
		b.WriteString("## " + strings.TrimSuffix(g.Title, ":") + "\n\n")

		for _, c := range g.Commands {
			if kid := findKid(tree, c.Name); kid != nil {
				if err := renderCommand(&b, kid, 3); err != nil {
					return nil, err
				}
			}
		}
	}

	return b.Bytes(), nil
}

func findKid(d *cmdDoc, name string) *cmdDoc {
	for _, k := range d.Kids {
		if k.Path[len(k.Path)-1] == name {
			return k
		}
	}

	return nil
}

// renderCommand writes one command and, below it, its subcommands. Top-level
// commands get a level 3 heading, everything below level 4.
func renderCommand(b *bytes.Buffer, d *cmdDoc, level int) error {
	name := "oa " + strings.Join(d.Path, " ")

	b.WriteString(strings.Repeat("#", level) + " `" + name + "`\n\n")

	short, err := cellText("short of "+name, d.Short)
	if err != nil {
		return err
	}

	if short != "" {
		b.WriteString(strings.TrimSuffix(short, ".") + ".\n\n")
	}

	if d.Desc != "" && d.Desc != d.Short {
		if err := renderProse(b, name, d.Desc); err != nil {
			return err
		}
	}

	if len(d.Kids) == 0 || len(d.Flags) > 0 {
		fence(b, d.UseLines...)
	}

	if len(d.Aliases) > 0 {
		b.WriteString("Aliases: `" + strings.Join(d.Aliases, "`, `") + "`.\n\n")
	}

	if err := renderFlags(b, name, d.Flags); err != nil {
		return err
	}

	if d.Examples != "" {
		b.WriteString("Examples:\n\n")
		fence(b, strings.Split(d.Examples, "\n")...)
	}

	for _, k := range d.Kids {
		if err := renderCommand(b, k, min(level+1, 4)); err != nil {
			return err
		}
	}

	return nil
}

// renderProse writes a description: paragraphs as text (angle brackets escaped,
// since VitePress compiles the page and would read <name> as a tag), and any
// paragraph with an indented line (a command or a table) as a code block.
func renderProse(b *bytes.Buffer, name, desc string) error {
	for para := range strings.SplitSeq(desc, "\n\n") {
		para = strings.Trim(para, "\n")
		if strings.TrimSpace(para) == "" {
			continue
		}

		lines := strings.Split(para, "\n")
		indented := slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") })

		if indented {
			fence(b, lines...)

			continue
		}

		if strings.Contains(para, "{{") || strings.Contains(para, "}}") {
			return fmt.Errorf("help of %s contains template delimiters", name)
		}

		b.WriteString(strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(para) + "\n\n")
	}

	return nil
}

func renderFlags(b *bytes.Buffer, owner string, flags []flagDoc) error {
	if len(flags) == 0 {
		return nil
	}

	b.WriteString("| Flag | Value | Description |\n|---|---|---|\n")

	for _, f := range flags {
		desc, err := cellText("flag "+f.Name+" of "+owner, f.Desc)
		if err != nil {
			return err
		}

		name := "`" + f.Name + "`"
		if f.Shorthand != "" {
			name = "`" + f.Shorthand + "`, " + name
		}

		val := "none"
		if f.Value != "" {
			val = "`" + f.Value + "`"
		}

		fmt.Fprintf(b, "| %s | %s | %s |\n", name, val, desc)
	}

	b.WriteString("\n")

	return nil
}

func renderEnv(b *bytes.Buffer, env map[string][]string) {
	names := make([]string, 0, len(env))
	for v := range env {
		names = append(names, v)
	}

	slices.Sort(names)

	b.WriteString("## Environment variables\n\n| Variable | Named by |\n|---|---|\n")

	for _, v := range names {
		fmt.Fprintf(b, "| `%s` | %s |\n", v, strings.Join(env[v], ", "))
	}

	b.WriteString("\n")
}
