// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"strings"
)

// helpRequested reports whether args ask for help: -h, -help or --help
// anywhere before a bare "--" terminator. It is checked before any subcommand
// reads its positional arguments, so a help flag can never become a name, id
// or path and never reaches the server.
func helpRequested(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "-help", "--help":
			return true
		}
	}

	return false
}

// usageSection returns the lines of usage that describe the command named by
// the leading words of args (for example "repo create"), or all of usage when
// no entry matches.
func usageSection(usage string, args []string) string {
	var words []string

	for _, a := range args {
		if strings.HasPrefix(a, "-") || len(words) == 3 {
			break
		}

		words = append(words, a)
	}

	lines := strings.Split(usage, "\n")

	for n := len(words); n > 0; n-- {
		var out []string

		keep := false

		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   "):
				keep = entryMatches(l, words[:n])
			case !strings.HasPrefix(l, "   "):
				keep = false
			}

			if keep {
				out = append(out, l)
			}
		}

		if len(out) > 0 {
			return strings.Join(out, "\n") + "\n"
		}
	}

	return usage
}

// entryMatches reports whether the usage entry line starts with words; a word
// of the entry such as "show|test|pushes" matches any of its alternatives.
func entryMatches(line string, words []string) bool {
	fields := strings.Fields(line)
	if len(fields) < len(words) {
		return false
	}

	for i, w := range words {
		ok := false

		for alt := range strings.SplitSeq(fields[i], "|") {
			ok = ok || alt == w
		}

		if !ok {
			return false
		}
	}

	return true
}

// printHelp writes the usage of the command in args to stdout.
func printHelp(args []string) {
	fmt.Fprint(os.Stdout, usageSection(usageText(), args))
}

// operand rejects a positional value that looks like a flag. Names, ids and
// paths never start with "-", so one that does is a mistyped or misplaced flag
// that must not be sent to the server as a resource name.
func operand(s string) string {
	if strings.HasPrefix(s, "-") {
		dief("%q looks like a flag, not a name or id; put flags after the positional arguments (see `oa <command> --help`)", s)
	}

	return s
}

// commandsWithOwnFlag lists the commands that define a flag of the same name
// as a global one; for them the flag after the command belongs to the command.
//
//nolint:gochecknoglobals // immutable lookup table
var commandsWithOwnFlag = map[string]map[string]bool{
	"repo create":   {"format": true},
	"sbom generate": {"format": true},
	"vex get":       {"format": true},
	"audit export":  {"format": true},
	"auth login":    {"server": true},
}

// globalFlag reports whether arg is --server, --token or --format (one or two
// leading dashes) and whether its value is attached with "=".
func globalFlag(arg string) (string, bool, bool) {
	if !strings.HasPrefix(arg, "-") {
		return "", false, false
	}

	n, _, attached := strings.Cut(strings.TrimLeft(arg, "-"), "=")
	switch n {
	case "server", "token", "format":
		return n, attached, true
	}

	return "", false, false
}

// hoistGlobals moves the global flags (--server, --token, --format, as
// "--flag value" or "--flag=value") from anywhere on the command line, up to a
// bare "--", to the front, so `oa repo list --format json` works like
// `oa --format json repo list`. A flag the command defines itself stays where
// it is. Order among the global flags is kept, so the last one still wins.
func hoistGlobals(args []string) []string {
	// Index of the command word and of the subcommand word.
	var words []int

	for i := 0; i < len(args) && len(words) < 2; i++ {
		switch {
		case args[i] == "--":
			i = len(args)
		case strings.HasPrefix(args[i], "-"):
			if _, attached, ok := globalFlag(args[i]); ok && !attached {
				i++
			}
		default:
			words = append(words, i)
		}
	}

	var key []string
	for _, w := range words {
		key = append(key, args[w])
	}

	own := commandsWithOwnFlag[strings.Join(key, " ")]

	var globals, rest []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)

			break
		}

		name, attached, ok := globalFlag(a)
		if !ok || (own[name] && len(words) > 0 && i > words[0]) {
			rest = append(rest, a)

			continue
		}

		globals = append(globals, a)

		if !attached && i+1 < len(args) {
			i++

			globals = append(globals, args[i])
		}
	}

	return append(globals, rest...)
}
