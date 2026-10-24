// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
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
	// listens on port 1, so a command that gets as far as a request fails at
	// once instead of reaching a real server. It is rewritten to the CLI's
	// documented default in everything the page shows.
	cliDeadServer    = "http://127.0.0.1:1"
	cliDefaultServer = "http://localhost:8080"
	cliProbeTimeout  = 15 * time.Second
	cliBuildTimeout  = 10 * time.Minute
	cliMaxPathDepth  = 3
)

func init() {
	register(generator{
		name: "cli",
		owns: func(rel string) bool { return rel == cliPage },
		run:  runCLI,
	})
}

// helpSection is one titled block of `oa help`.
type helpSection struct {
	Title string
	// Lines are the block's lines without the common two-space indent.
	Lines []string
}

// cliHelp is `oa help` split into its parts.
type cliHelp struct {
	Usage    helpSection
	Global   helpSection
	Sections []helpSection
}

// flagDoc is one flag of a flag-package usage listing.
type flagDoc struct {
	Name, Value, Description string
}

// cmdProbe is what a command printed when run for documentation.
type cmdProbe struct {
	// Path is the command words, e.g. [apply] or [webhook repo create].
	Path []string
	// Usage is the usage text printed when the command runs without arguments,
	// kept only when it is richer than the one-line summary in `oa help`.
	Usage string
	// Flags is the parsed flag listing printed by `--help`; RawFlags holds the
	// listing verbatim when it could not be parsed.
	Flags    []flagDoc
	RawFlags string
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

	helpText, err := r.run("help")
	if err != nil {
		return nil, err
	}

	help, err := parseCLIHelp(helpText)
	if err != nil {
		return nil, err
	}

	probes, err := probeCommands(r, help)
	if err != nil {
		return nil, err
	}

	page, err := renderCLI(help, probes)
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

// run returns the combined output of `oa args...`; a non-zero exit is normal
// (usage errors), a failure to start or a timeout is an error.
func (r *cliRunner) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.bin, args...) //nolint:gosec // the binary was built by this process, arguments come from its own help text
	cmd.Env = []string{
		"HOME=" + r.home, "USERPROFILE=" + r.home, "XDG_CONFIG_HOME=" + filepath.Join(r.home, ".config"),
		"TMPDIR=" + r.home, "OA_SERVER=" + cliDeadServer,
	}

	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("oa %s: timed out after %s", strings.Join(args, " "), cliProbeTimeout)
	}

	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", fmt.Errorf("oa %s: %w", strings.Join(args, " "), err)
	}

	return strings.ReplaceAll(strings.ReplaceAll(string(out), "\r\n", "\n"), cliDeadServer, cliDefaultServer), nil
}

var cliVersionLine = regexp.MustCompile(`^OpenArtifact CLI v\S*$`)

// parseCLIHelp splits the help text into the Usage block, the Global flags
// block and the command sections. A heading is an unindented line ending in a
// colon; an indented block after a blank line without a heading of its own
// (the version/help commands at the end) becomes the "Utility" section.
func parseCLIHelp(text string) (cliHelp, error) {
	var (
		h      cliHelp
		blocks []helpSection
		cur    *helpSection
	)

	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	if len(lines) > 0 && cliVersionLine.MatchString(lines[0]) {
		lines = lines[1:]
	}

	for _, line := range lines {
		switch {
		case strings.TrimSpace(line) == "":
			cur = nil
		case !strings.HasPrefix(line, " "):
			if !strings.HasSuffix(line, ":") {
				return h, fmt.Errorf("unexpected unindented line in oa help: %q", line)
			}

			blocks = append(blocks, helpSection{Title: strings.TrimSuffix(line, ":")})
			cur = &blocks[len(blocks)-1]
		default:
			if cur == nil {
				blocks = append(blocks, helpSection{Title: "Utility"})
				cur = &blocks[len(blocks)-1]
			}

			cur.Lines = append(cur.Lines, strings.TrimPrefix(line, "  "))
		}
	}

	for _, b := range blocks {
		switch b.Title {
		case "Usage":
			h.Usage = b
		case "Global flags":
			h.Global = b
		default:
			h.Sections = append(h.Sections, b)
		}
	}

	if len(h.Usage.Lines) == 0 || len(h.Global.Lines) == 0 || len(h.Sections) == 0 {
		return h, errors.New("oa help has no Usage, Global flags or command sections: the help text format changed")
	}

	for _, s := range h.Sections {
		if len(s.Lines) == 0 {
			return h, fmt.Errorf("oa help section %q is empty", s.Title)
		}
	}

	return h, nil
}

var cliWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// entryPaths returns the command paths a usage line starts: the leading words
// before the first argument, flag or double space, with `a|b` alternatives
// expanded. Continuation lines (indented further) and the description column
// are not command words.
func entryPaths(line string) [][]string {
	if strings.HasPrefix(line, " ") || line == "" {
		return nil
	}

	head, _, _ := strings.Cut(line, "  ")
	paths := [][]string{nil}

	for tok := range strings.FieldsSeq(head) {
		alts := strings.Split(tok, "|")
		if !slices.ContainsFunc(alts, func(a string) bool { return !cliWord.MatchString(a) }) {
			var next [][]string

			for _, p := range paths {
				for _, a := range alts {
					next = append(next, append(slices.Clone(p), a))
				}
			}

			paths = next
		} else {
			break
		}

		if len(paths[0]) >= cliMaxPathDepth {
			break
		}
	}

	if len(paths[0]) == 0 {
		return nil
	}

	return paths
}

// probeCommands runs every command named in the help to collect richer usage
// text: the no-argument usage of each top-level command and the `--help` flag
// listing of each command path.
func probeCommands(r *cliRunner, help cliHelp) ([]cmdProbe, error) {
	var (
		paths [][]string
		seen  = map[string]bool{}
	)

	add := func(p []string) {
		if k := strings.Join(p, " "); !seen[k] {
			seen[k] = true

			paths = append(paths, p)
		}
	}

	for _, s := range help.Sections {
		for _, l := range s.Lines {
			for _, p := range entryPaths(l) {
				add(p)
			}
		}
	}

	// top is each top-level command's usage when run without arguments (only
	// when it is richer than the summary in oa help).
	top := map[string]string{}

	for _, p := range paths {
		if _, ok := top[p[0]]; ok {
			continue
		}

		text, err := r.run(p[0])
		if err != nil {
			return nil, err
		}

		top[p[0]] = richUsage(text)
	}

	var out []cmdProbe

	for _, p := range paths {
		probe := cmdProbe{Path: p}

		text, err := r.run(append(slices.Clone(p), "--help")...)
		if err != nil {
			return nil, err
		}

		probe.Flags, probe.RawFlags = flagListing(text)

		if probe.Flags != nil || probe.RawFlags != "" {
			out = append(out, probe)
		}
	}

	// A top-level command's own usage text gets a probe even when the command
	// has no flags of its own (oa trash).
	tops := make([]string, 0, len(top))

	for c, u := range top {
		if u != "" {
			tops = append(tops, c)
		}
	}

	slices.Sort(tops)

	for _, c := range tops {
		i := slices.IndexFunc(out, func(p cmdProbe) bool { return slices.Equal(p.Path, []string{c}) })
		if i < 0 {
			out = append(out, cmdProbe{Path: []string{c}, Usage: top[c]})

			continue
		}

		out[i].Usage = top[c]
	}

	return out, nil
}

// flagListing parses the output of `oa COMMAND --help` when it is a flag
// package listing. It returns nothing for any other output (an error message),
// and the verbatim text when the listing has a shape the parser does not know.
func flagListing(text string) ([]flagDoc, string) {
	if !strings.HasPrefix(text, "Usage of ") {
		return nil, ""
	}

	// Some commands report flag.ErrHelp as an error after the listing.
	text = strings.TrimSuffix(strings.TrimRight(text, "\n"), "\nError: "+flag.ErrHelp.Error())

	flags, err := parseFlagUsage(text)
	if err != nil {
		return nil, text
	}

	return flags, ""
}

// richUsage returns the usage text of a command run without arguments when it
// has more than the one-line summary, and nothing when the command instead got
// as far as contacting the server.
func richUsage(text string) string {
	text = strings.TrimRight(strings.TrimPrefix(text, "Error: "), "\n")

	lines := strings.Split(text, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "Usage:") || strings.Contains(text, "connection refused") {
		return ""
	}

	return text
}

// parseFlagUsage parses the listing printed by the standard flag package:
//
//	Usage of name:
//	  -flag type
//	    	description
//	  -bool	description
func parseFlagUsage(text string) ([]flagDoc, error) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "Usage of ") {
		return nil, errors.New("not a flag usage listing")
	}

	var flags []flagDoc

	for _, l := range lines[1:] {
		switch {
		case strings.HasPrefix(l, "  -"):
			head, desc, _ := strings.Cut(l[2:], "\t")

			fields := strings.Fields(head)
			if len(fields) == 0 || len(fields) > 2 {
				return nil, fmt.Errorf("unrecognised flag line %q", l)
			}

			f := flagDoc{Name: fields[0], Description: strings.TrimSpace(desc)}
			if len(fields) == 2 {
				f.Value = fields[1]
			}

			flags = append(flags, f)
		case strings.HasPrefix(l, "    \t") && len(flags) > 0:
			last := &flags[len(flags)-1]
			last.Description = strings.TrimSpace(last.Description + " " + strings.TrimSpace(l))
		default:
			return nil, fmt.Errorf("unrecognised line in flag usage: %q", l)
		}
	}

	if len(flags) == 0 {
		return nil, errors.New("flag usage lists no flags")
	}

	return flags, nil
}

var cliEnvVar = regexp.MustCompile(`\bOA_[A-Z0-9_]+\b`)

// envMentions maps each OA_* variable named anywhere in the help or the flag
// listings to the places naming it: global flags or command paths.
func envMentions(help cliHelp, probes []cmdProbe) map[string][]string {
	set := map[string]map[string]bool{}

	note := func(text, where string) {
		for _, v := range cliEnvVar.FindAllString(text, -1) {
			if set[v] == nil {
				set[v] = map[string]bool{}
			}

			set[v][where] = true
		}
	}

	for _, l := range help.Global.Lines {
		if f := strings.Fields(l); len(f) > 0 {
			note(l, "`"+f[0]+"`")
		}
	}

	for _, s := range help.Sections {
		where := ""

		for _, l := range s.Lines {
			if p := entryPaths(l); len(p) > 0 {
				where = "`oa " + strings.Join(p[0], " ") + "`"
			}

			if where != "" {
				note(l, where)
			}
		}
	}

	for _, p := range probes {
		w := "`oa " + strings.Join(p.Path, " ") + "`"

		note(p.RawFlags, w)

		for _, f := range p.Flags {
			note(f.Description, w)
		}
	}

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

// renderCLI writes the page.
func renderCLI(help cliHelp, probes []cmdProbe) ([]byte, error) {
	env := envMentions(help, probes)
	if len(env) == 0 {
		return nil, errors.New("no OA_* environment variable found in the CLI help: the help text format changed")
	}

	byPath := map[string]cmdProbe{}
	for _, p := range probes {
		byPath[strings.Join(p.Path, " ")] = p
	}

	// sameFlags remembers the first command that printed a given flag listing,
	// so commands that share one (import nexus and artifactory) point at it.
	sameFlags := map[string]string{}

	var b bytes.Buffer

	b.Write(header("CLI reference", "the output of `oa help` and `oa COMMAND --help`"))
	b.WriteString("`oa` is the command-line client of the REST API. For login, the config file and worked examples see [CLI](../integrations/cli.md). Global flags go before the command. Each command group below shows its usage as printed by `oa help`; commands that parse flags list them as printed by `oa COMMAND --help` (the flag package accepts both `-flag` and `--flag`).\n\n")

	b.WriteString("## Usage\n\n")
	fence(&b, help.Usage.Lines...)
	b.WriteString("## Global flags\n\n")
	fence(&b, help.Global.Lines...)

	renderEnv(&b, env)

	for _, s := range help.Sections {
		b.WriteString("## " + s.Title + "\n\n")
		fence(&b, s.Lines...)

		for _, l := range s.Lines {
			for _, p := range entryPaths(l) {
				// The command's own usage text comes before its subcommands.
				for _, key := range []string{p[0], strings.Join(p, " ")} {
					probe, ok := byPath[key]
					if !ok {
						continue
					}

					delete(byPath, key) // one subsection per command even if several entries name it

					if err := renderProbe(&b, probe, sameFlags); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	return b.Bytes(), nil
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

func renderProbe(b *bytes.Buffer, p cmdProbe, sameFlags map[string]string) error {
	name := "oa " + strings.Join(p.Path, " ")

	b.WriteString("### `" + name + "`\n\n")

	if p.Usage != "" {
		fence(b, strings.Split(p.Usage, "\n")...)
	}

	if sig := fmt.Sprint(p.Flags, p.RawFlags); p.RawFlags != "" || len(p.Flags) > 0 {
		if first, dup := sameFlags[sig]; dup {
			b.WriteString("Same flags as `" + first + "`.\n\n")

			return nil
		}

		sameFlags[sig] = name
	}

	switch {
	case p.RawFlags != "":
		fence(b, strings.Split(p.RawFlags, "\n")...)
	case len(p.Flags) > 0:
		b.WriteString("| Flag | Value | Description |\n|---|---|---|\n")

		for _, f := range p.Flags {
			desc, err := cellText("flag "+f.Name, f.Description)
			if err != nil {
				return fmt.Errorf("oa %s: %w", strings.Join(p.Path, " "), err)
			}

			val := "none"
			if f.Value != "" {
				val = "`" + f.Value + "`"
			}

			fmt.Fprintf(b, "| `%s` | %s | %s |\n", f.Name, val, desc)
		}

		b.WriteString("\n")
	}

	return nil
}
