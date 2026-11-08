// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/spf13/cobra"
)

var errRetention = errors.New("--trash-retention-days must be a non-negative integer or \"default\"")

// hostedFormatNames are the formats a hosted repository can have, sorted.
func hostedFormatNames() []string {
	return slices.DeleteFunc(repotype.SortedNames(), func(f string) bool { return !repotype.HostedSupported(f) })
}

// listPageSize is the largest page the repository and package endpoints serve.
const listPageSize = 100

func newRepoCmd(a *app) *cobra.Command {
	return parentCmd("repo", "Manage repositories",
		&cobra.Command{
			Use:   "list",
			Short: "List repositories",
			Args:  usageArgs(0, 0),
			Run:   func(*cobra.Command, []string) { repoList(a.client, a.outFmt) },
		},
		repoCreateCmd(a),
		&cobra.Command{
			Use:               "show <name>",
			Short:             "Show repository details",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.repoNames()),
			Run:               func(_ *cobra.Command, args []string) { repoShow(a.client, args[0], a.outFmt) },
		},
		repoUpdateCmd(a),
		repoDeleteCmd(a),
		&cobra.Command{
			Use:               "packages <name>",
			Short:             "List packages in a repository",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.repoNames()),
			Run:               func(_ *cobra.Command, args []string) { repoPackages(a.client, args[0], a.outFmt) },
		},
		&cobra.Command{
			Use:               "members <name>",
			Short:             "List the member repositories of a group repository",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.repoNames()),
			Run:               func(_ *cobra.Command, args []string) { repoMembers(a.client, args[0], a.outFmt) },
		},
	)
}

func repoList(client *Client, outFmt string) {
	repos := fetchAllPages(client, "/api/v1/repositories", "repositories", "limit", listPageSize)

	printTable(repos, []string{"NAME", "FORMAT", "TYPE", "DESCRIPTION", "PUBLIC"},
		[]string{"name", "format", "repo_type", "description", "public"}, outFmt)
}

// repoCreateCmd defines its own --format (the repository format), which
// shadows the global output format after the command name; put the global one
// before the command (oa --format json repo create ...).
func repoCreateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name> --format <fmt>",
		Short: "Create a repository",
		Long: "Create a hosted repository. --format is required and names the package format.\n\n" +
			"After the command name, --format is the repository format; to choose the output format " +
			"put the global flag before the command: oa --format json repo create NAME --format npm.",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(nothing),
	}

	repoFmt := cmd.Flags().String("format", "", "Repository format (required)")
	desc := cmd.Flags().String("description", "", "Description")
	public := cmd.Flags().Bool("public", false, "Public repository")
	retention := cmd.Flags().String(
		"trash-retention-days",
		"",
		"Days deleted items stay in the trash (0 = delete immediately, default = server setting)",
	)
	immutable := cmd.Flags().Bool("immutable-releases", false, "Block deleting or overwriting released versions")
	signPkgs := cmd.Flags().Bool("sign-packages", false, "Hosted rpm repositories: sign uploaded RPMs with the active GPG key")

	honourQuay := cmd.Flags().Bool("honour-quay-expiry", false,
		"Hosted docker repositories: delete images once their quay.expires-after time has passed (not with --immutable-releases)")

	flagEnum(cmd, "format", hostedFormatNames()...)

	cmd.Run = func(cmd *cobra.Command, args []string) {
		repoCreate(a.client, args[0], a.outFmt, cmd.Flags().Changed, repoCreateFlags{
			format: *repoFmt, description: *desc, public: *public, retention: *retention, immutable: *immutable, signPackages: *signPkgs,
			honourQuay: *honourQuay,
		})
	}

	return cmd
}

type repoCreateFlags struct {
	format, description, retention              string
	public, immutable, signPackages, honourQuay bool
}

func repoCreate(client *Client, name, outFmt string, changed func(string) bool, f repoCreateFlags) {
	if f.format == "" {
		dief("--format is required; one of: %s", repotype.HostedFormats())
	}

	body := map[string]any{
		"name":        name,
		"format":      f.format,
		"description": f.description,
		"public":      f.public,
	}

	if changed("trash-retention-days") {
		days, err := parseRetention(f.retention)
		if err != nil {
			dief("%v", err)
		}

		if days != nil {
			body["trash_retention_days"] = days
		}
	}

	if changed("immutable-releases") {
		body["immutable_releases"] = f.immutable
	}

	if changed("sign-packages") {
		body["sign_packages"] = f.signPackages
	}

	if changed("honour-quay-expiry") {
		body["honour_quay_expiry"] = f.honourQuay
	}

	result := sendJSON(client, "POST", "/api/v1/repositories", body, http.StatusCreated)

	if outFmt == "json" {
		printJSON(result)
		return
	}

	fmt.Printf("Repository '%s' created.\n", name)
}

func repoShow(client *Client, name, outFmt string) {
	var result map[string]any
	getJSON(client, "/api/v1/repository/"+escape(name), &result)

	if outFmt == "json" {
		printJSON(result)
		return
	}

	w := newTabWriter()

	for _, k := range []string{
		"name", "format", "repo_type", "description", "public", "upstream_url", "trash_retention_days",
		"immutable_releases", "merge_proxy_versions", "sign_packages", "honour_quay_expiry", "created_at", "updated_at",
	} {
		if v, ok := result[k]; ok {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", k, cell(v))
		}
	}

	_ = w.Flush()
}

// repoUpdateCmd sends only the flags that were given: an omitted --public must
// not flip a public repository private.
func repoUpdateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <name>",
		Short:             "Update a repository (only the flags you pass are sent)",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.repoNames()),
	}

	fl := cmd.Flags()
	desc := fl.String("description", "", "Description")
	public := fl.Bool("public", false, "Public (--public=false to make private)")
	retention := fl.String(
		"trash-retention-days",
		"",
		"Days deleted items stay in the trash (0 = delete immediately, default = server setting)",
	)
	immutable := fl.Bool(
		"immutable-releases",
		false,
		"Block deleting or overwriting released versions (--immutable-releases=false to allow)",
	)
	mergeProxy := fl.Bool(
		"merge-proxy-versions",
		false,
		"Group repositories, administrators only: merge proxy members' versions of names a hosted member holds "+
			"(--merge-proxy-versions=false restores hosted-wins)",
	)
	signPkgs := fl.Bool(
		"sign-packages",
		false,
		"Hosted rpm repositories: sign uploaded RPMs with the active GPG key (--sign-packages=false to stop)",
	)

	honourQuay := fl.Bool(
		"honour-quay-expiry",
		false,
		"Hosted docker repositories: delete images once their quay.expires-after time has passed (--honour-quay-expiry=false to stop)",
	)

	flagEnum(cmd, "trash-retention-days", "default", "0", "7", "30")

	cmd.Run = func(cmd *cobra.Command, args []string) {
		set := cmd.Flags().Changed
		body := map[string]any{}

		if set("description") {
			body["description"] = *desc
		}

		if set("public") {
			body["public"] = *public
		}

		if set("trash-retention-days") {
			days, err := parseRetention(*retention)
			if err != nil {
				dief("%v", err)
			}

			body["trash_retention_days"] = days
		}

		if set("immutable-releases") {
			body["immutable_releases"] = *immutable
		}

		if set("merge-proxy-versions") {
			body["merge_proxy_versions"] = *mergeProxy
		}

		if set("sign-packages") {
			body["sign_packages"] = *signPkgs
		}

		if set("honour-quay-expiry") {
			body["honour_quay_expiry"] = *honourQuay
		}

		if len(body) == 0 {
			dief(
				"nothing to update: pass --description, --public[=false], --trash-retention-days, " +
					"--immutable-releases[=false], --sign-packages[=false], --honour-quay-expiry[=false] or --merge-proxy-versions[=false]",
			)
		}

		sendJSON(a.client, "PATCH", "/api/v1/repository/"+escape(args[0]), body, http.StatusOK)
		fmt.Printf("Repository '%s' updated.\n", args[0])
	}

	return cmd
}

// parseRetention maps --trash-retention-days to the API value: a non-negative
// integer, or nil (JSON null) for "default" (use the server-wide retention).
func parseRetention(v string) (any, error) {
	if v == "default" {
		return nil, nil //nolint:nilnil // nil is the JSON null that selects the server default
	}

	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return nil, errRetention
	}

	return n, nil
}

// repoDeleteCmd deletes a repository. The server requires the repository name as
// confirmation, so --confirm (or an interactive prompt) is mandatory; the
// repository then waits in the trash for its retention period.
func repoDeleteCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "delete <name>",
		Short:             "Delete a repository (moved to the trash; prompts for the name on a terminal)",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.repoNames()),
	}

	confirm := cmd.Flags().String("confirm", "", "Repository name, as confirmation")

	flagValues(cmd, "confirm", a.repoNames())

	cmd.Run = func(cmd *cobra.Command, args []string) {
		name := args[0]

		confirmed, err := resolveRepoConfirm(name, *confirm, cmd.Flags().Changed("confirm"), stdinIsTerminal(), stdinReader, stderrPrompt)
		if err != nil {
			dief("%v", err)
		}

		resp, err := a.client.do("DELETE", "/api/v1/repository/"+escape(name)+"?confirm="+url.QueryEscape(confirmed), nil, "")
		if err != nil {
			dief("%v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Repository '%s' deleted (kept in the trash until its retention expires, unless retention is 0).\n", name)
	}

	return cmd
}

func repoPackages(client *Client, name, outFmt string) {
	pkgs := fetchAllPages(client, "/api/v1/repositories/"+escape(name)+"/packages", "packages", "limit", listPageSize)

	printTable(pkgs, []string{"ID", "NAME", "VERSION", "ARCH", "SIZE"},
		[]string{"id", "name", "version", "arch", "size"}, outFmt)
}

// repoMembers lists the member repositories of a group repository.
func repoMembers(client *Client, name, outFmt string) {
	listGeneric(client, "/api/v1/repositories/"+escape(name)+"/members", "members",
		[]string{"NAME", "FORMAT", "TYPE", "PRIORITY"},
		[]string{"name", "format", "repo_type", "priority"}, outFmt)
}
