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
