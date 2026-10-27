// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"
)

var (
	errConfirmMismatch   = errors.New("confirmation does not match the repository name")
	errConfirmNeeded     = errors.New("confirmation required and stdin is not a terminal")
	errPurgeNotConfirmed = errors.New("purge not confirmed")
	errPurgeNeedsYes     = errors.New("refusing to purge without confirmation and stdin is not a terminal")
)

// stdinIsTerminal reports whether prompts can be answered interactively.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// resolveRepoConfirm returns the confirmation to send for deleting repo name.
// A given value must equal the name; without one, an interactive session is
// asked to type the name and a non-interactive one fails.
func resolveRepoConfirm(name, given string, givenSet, interactive bool, in *bufio.Reader, prompt func(string)) (string, error) {
	if givenSet {
		if given != name {
			return "", fmt.Errorf("%w: --confirm %q != %q", errConfirmMismatch, given, name)
		}

		return given, nil
	}

	if !interactive {
		return "", fmt.Errorf("%w; pass --confirm %s", errConfirmNeeded, name)
	}

	prompt("Type the repository name to confirm: ")

	line, err := readLine(in)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(line) != name {
		return "", fmt.Errorf("%w: got %q", errConfirmMismatch, strings.TrimSpace(line))
	}

	return name, nil
}

// confirmPurge asks before an irreversible purge unless yes is set.
func confirmPurge(kind, id string, yes, interactive bool, in *bufio.Reader, prompt func(string)) error {
	if yes {
		return nil
	}

	if !interactive {
		return fmt.Errorf("%w; pass --yes", errPurgeNeedsYes)
	}

	prompt(fmt.Sprintf("Permanently delete %s %s? This cannot be undone. Type 'yes' to confirm: ", kind, id))

	line, err := readLine(in)
	if err != nil {
		return err
	}

	if !strings.EqualFold(strings.TrimSpace(line), "yes") {
		return errPurgeNotConfirmed
	}

	return nil
}

func stderrPrompt(s string) { fmt.Fprint(os.Stderr, s) }

func handleTrash(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief(
			"Usage: oa trash <list|restore|purge>\n  oa trash purge <kind> <id> [--yes]            purge one item (admin)\n  oa trash purge --repo NAME --all [--yes]      empty a repository's trash (repository manage permission)",
		)
	}

	switch args[0] {
	case "list":
		trashList(client, args[1:], outFmt)
	case "restore":
		needArgs(args, 3, "Usage: oa trash restore <kind> <id> [--repo NAME]")
		trashRestore(client, args[1], args[2], args[3:], outFmt)
	case "purge":
		const purgeUsage = "Usage: oa trash purge <kind> <id> [--yes]  |  oa trash purge --repo NAME --all [--yes]"
		if len(args) > 1 && strings.HasPrefix(args[1], "-") {
			trashEmpty(client, args[1:], outFmt)
			return
		}

		needArgs(args, 3, purgeUsage)
		trashPurge(client, args[1], args[2], args[3:])
	default:
		dief("Unknown trash subcommand: %s", args[0])
	}
}

// trashList lists trashed items: every item (admin) or, with --repo, those of
// one repository (repository delete permission).
func trashList(client *Client, args []string, outFmt string) {
	fs := flag.NewFlagSet("trash list", flag.ExitOnError)
	repo := fs.String("repo", "", "Only items of this repository")
	kind := fs.String("kind", "", "Only items of this kind (e.g. maven_artifact, docker_tag, repository)")
	_ = fs.Parse(args)

	path := "/api/v1/trash"
	if *repo != "" {
		path = "/api/v1/repositories/" + escape(*repo) + "/trash"
	}

	if *kind != "" {
		path += "?kind=" + url.QueryEscape(*kind)
	}

	items := fetchAllPages(client, path, "items", "limit", listPageSize)

	printTable(items,
		[]string{"ID", "KIND", "REPOSITORY", "NAME", "VERSION", "SIZE", "DELETED_AT", "DELETED_BY", "PURGE_AT"},
		[]string{"id", "kind", "repository", "name", "version", "size_bytes", "deleted_at", "deleted_by", "purge_at"}, outFmt)
}

// trashRestore restores one item. --repo uses the repository-scoped endpoint
// (repository delete permission); without it the admin endpoint is used.
func trashRestore(client *Client, kind, id string, args []string, outFmt string) {
	fs := flag.NewFlagSet("trash restore", flag.ExitOnError)
	repo := fs.String("repo", "", "Restore through this repository's trash (not for kind repository)")
	_ = fs.Parse(args)

	path := "/api/v1/trash/" + escape(kind) + "/" + escape(id) + "/restore"
	if *repo != "" {
		path = "/api/v1/repositories/" + escape(*repo) + "/trash/" + escape(kind) + "/" + escape(id) + "/restore"
	}

	result := sendJSON(client, http.MethodPost, path, nil, http.StatusOK)

	if outFmt == "json" {
		printJSON(result)
		return
	}

	fmt.Printf("Restored %s %s.\n", kind, id)
}

// trashPurge permanently deletes one trashed item now (admin).
func trashPurge(client *Client, kind, id string, args []string) {
	fs := flag.NewFlagSet("trash purge", flag.ExitOnError)
	yes := fs.Bool("yes", false, "Do not ask for confirmation")
	_ = fs.Parse(args)

	if err := confirmPurge(kind, id, *yes, stdinIsTerminal(), stdinReader, stderrPrompt); err != nil {
		dief("%v", err)
	}

	deleteResource(client, "/api/v1/trash/"+escape(kind)+"/"+escape(id), fmt.Sprintf("Purged %s %s.", kind, id))
}

// trashEmpty purges every trashed item of one repository
// (DELETE /api/v1/repositories/{name}/trash, repository manage permission).
// The server purges a capped number per request; the call is repeated until
// nothing remains.
func trashEmpty(client *Client, args []string, outFmt string) {
	fs := flag.NewFlagSet("trash purge", flag.ExitOnError)
	repo := fs.String("repo", "", "Repository whose trash to empty")
	all := fs.Bool("all", false, "Purge every trashed item of --repo")
	yes := fs.Bool("yes", false, "Do not ask for confirmation")
	_ = fs.Parse(args)

	if !*all || *repo == "" {
		dief("Usage: oa trash purge --repo NAME --all [--yes] (--all requires --repo)")
	}

	if err := confirmPurge("all trashed items of repository", *repo, *yes, stdinIsTerminal(), stdinReader, stderrPrompt); err != nil {
		dief("%v", err)
	}

	path := "/api/v1/repositories/" + escape(*repo) + "/trash"
	total := 0

	for {
		result := sendJSON(client, http.MethodDelete, path, nil, http.StatusOK)

		purged, _ := result["purged"].(float64)
		remaining, _ := result["remaining"].(float64)
		total += int(purged)

		// No progress means nothing more can be purged this way.
		if remaining <= 0 || purged <= 0 {
			if outFmt == "json" {
				printJSON(map[string]any{"purged": total, "remaining": int(remaining)})
				return
			}

			fmt.Printf("Purged %d trashed items of %s (%d remaining).\n", total, *repo, int(remaining))

			return
		}
	}
}
