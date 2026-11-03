// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
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

const (
	purgeUsage      = "Usage: oa trash purge <kind> <id> [--yes]  |  oa trash purge --repo NAME --all [--yes]"
	purgeEmptyUsage = "Usage: oa trash purge --repo NAME --all [--yes] (--all requires --repo)"
)

// trashKinds are the kinds of trashed item the server knows (the TrashKind
// constants of internal/database/models; internal/oacli must not import the server
// packages).
func trashKinds() []string {
	return []string{
		"repository", "maven_artifact", "ivy_artifact", "sbt_artifact", "npm_version", "pypi_package", "nuget_package",
		"go_module", "cargo_crate", "composer", "rubygems_gem", "conda_package", "pacman", "ansible", "conan_package",
		"terraform_module", "terraform_provider", "nix_path", "apk_package", "helm_chart", "raw_artifact",
		"docker_manifest", "docker_tag", "rpm_package", "deb_package",
	}
}

func newTrashCmd(a *app) *cobra.Command {
	return parentCmd("trash", "List, restore and purge deleted items",
		trashListCmd(a),
		trashRestoreCmd(a),
		trashPurgeCmd(a),
	)
}

// trashList lists trashed items: every item (admin) or, with --repo, those of
// one repository (repository delete permission).
func trashListCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List trashed items",
		Long: "List every trashed item (administrators) or, with --repo, those of one repository " +
			"(repository delete permission).",
		Args: usageArgs(0, 0),
	}

	repo := cmd.Flags().StringP("repo", "r", "", "Only items of this repository")
	kind := cmd.Flags().String("kind", "", "Only items of this kind (e.g. maven_artifact, docker_tag, repository)")

	flagValues(cmd, "repo", a.repoNames())
	flagEnum(cmd, "kind", trashKinds()...)

	cmd.Run = func(*cobra.Command, []string) { trashList(a.client, *repo, *kind, a.outFmt) }

	return cmd
}

func trashList(client *Client, repo, kind, outFmt string) {
	path := "/api/v1/trash"
	if repo != "" {
		path = "/api/v1/repositories/" + escape(repo) + "/trash"
	}

	if kind != "" {
		path += "?kind=" + url.QueryEscape(kind)
	}

	items := fetchAllPages(client, path, "items", "limit", listPageSize)

	printTable(items,
		[]string{"ID", "KIND", "REPOSITORY", "NAME", "VERSION", "SIZE", "DELETED_AT", "DELETED_BY", "PURGE_AT"},
		[]string{"id", "kind", "repository", "name", "version", "size_bytes", "deleted_at", "deleted_by", "purge_at"}, outFmt)
}

// trashRestoreCmd restores one item. --repo uses the repository-scoped endpoint
// (repository delete permission); without it the admin endpoint is used.
func trashRestoreCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "restore <kind> <id> [--repo NAME]",
		Short:             "Restore a trashed item",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: positional(enum(trashKinds()...), nothing),
	}

	repo := cmd.Flags().StringP("repo", "r", "", "Restore through this repository's trash (not for kind repository)")

	flagValues(cmd, "repo", a.repoNames())

	cmd.Run = func(_ *cobra.Command, args []string) { trashRestore(a.client, args[0], args[1], *repo, a.outFmt) }

	return cmd
}

func trashRestore(client *Client, kind, id, repo, outFmt string) {
	path := "/api/v1/trash/" + escape(kind) + "/" + escape(id) + "/restore"
	if repo != "" {
		path = "/api/v1/repositories/" + escape(repo) + "/trash/" + escape(kind) + "/" + escape(id) + "/restore"
	}

	result := sendJSON(client, http.MethodPost, path, nil, http.StatusOK)

	if outFmt == "json" {
		printJSON(result)
		return
	}

	fmt.Printf("Restored %s %s.\n", kind, id)
}

// trashPurgeCmd is one command with two modes: purge one item (administrators)
// or, with --repo and --all, empty a repository's trash (repository manage
// permission).
func trashPurgeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purge <kind> <id> | --repo NAME --all",
		Short: "Permanently delete trashed items",
		Long: "Permanently delete one trashed item (administrators):\n\n" +
			"  oa trash purge <kind> <id> [--yes]\n\n" +
			"or empty the trash of one repository (repository manage permission):\n\n" +
			"  oa trash purge --repo NAME --all [--yes]\n\n" +
			"Both ask for confirmation unless --yes is given; without a terminal --yes is required.",
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case cmd.Flags().Changed("all"):
				if len(args) == 0 {
					return nil
				}
			case len(args) == 2:
				return nil
			case len(args) == 0 && cmd.Flags().NFlag() > 0:
				return cliError(purgeEmptyUsage)
			}

			return cliError(purgeUsage)
		},
		ValidArgsFunction: positional(enum(trashKinds()...), nothing),
	}

	repo := cmd.Flags().StringP("repo", "r", "", "Repository whose trash to empty")
	all := cmd.Flags().Bool("all", false, "Purge every trashed item of --repo")
	yes := cmd.Flags().BoolP("yes", "y", false, "Do not ask for confirmation")

	flagValues(cmd, "repo", a.repoNames())

	cmd.Run = func(_ *cobra.Command, args []string) {
		if len(args) == 2 {
			trashPurge(a.client, args[0], args[1], *yes)
			return
		}

		trashEmpty(a.client, *repo, *all, *yes, a.outFmt)
	}

	return cmd
}

// trashPurge permanently deletes one trashed item now (admin).
func trashPurge(client *Client, kind, id string, yes bool) {
	if err := confirmPurge(kind, id, yes, stdinIsTerminal(), stdinReader, stderrPrompt); err != nil {
		dief("%v", err)
	}

	deleteResource(client, "/api/v1/trash/"+escape(kind)+"/"+escape(id), fmt.Sprintf("Purged %s %s.", kind, id))
}

// trashEmpty purges every trashed item of one repository
// (DELETE /api/v1/repositories/{name}/trash, repository manage permission).
// The server purges a capped number per request; the call is repeated until
// nothing remains.
func trashEmpty(client *Client, repo string, all, yes bool, outFmt string) {
	if !all || repo == "" {
		dief("%s", purgeEmptyUsage)
	}

	if err := confirmPurge("all trashed items of repository", repo, yes, stdinIsTerminal(), stdinReader, stderrPrompt); err != nil {
		dief("%v", err)
	}

	path := "/api/v1/repositories/" + escape(repo) + "/trash"
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

			fmt.Printf("Purged %d trashed items of %s (%d remaining).\n", total, repo, int(remaining))

			return
		}
	}
}
