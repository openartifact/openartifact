// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

var errRetention = errors.New("--trash-retention-days must be a non-negative integer or \"default\"")

// listPageSize is the largest page the repository and package endpoints serve.
const listPageSize = 100

func handleRepo(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa repo <list|create|show|update|delete|packages|members>")
	}

	switch args[0] {
	case "list":
		repoList(client, outFmt)
	case "create":
		if len(args) < 2 {
			dief("Usage: oa repo create <name> [--format fmt] [--description D] [--public] [--trash-retention-days N] [--immutable-releases]")
		}

		repoCreate(client, args[1], args[2:], outFmt)
	case "show":
		if len(args) < 2 {
			dief("Usage: oa repo show <name>")
		}

		repoShow(client, args[1], outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa repo update <name> [--description D] [--public[=false]] [--trash-retention-days N|default] [--immutable-releases[=false]]")
		}

		repoUpdate(client, args[1], args[2:])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa repo delete <name> [--confirm <name>]")
		}

		repoDelete(client, args[1], args[2:])
	case "packages":
		if len(args) < 2 {
			dief("Usage: oa repo packages <name>")
		}

		repoPackages(client, args[1], outFmt)
	case "members":
		if len(args) < 2 {
			dief("Usage: oa repo members <name>")
		}

		repoMembers(client, args[1], outFmt)
	default:
		dief("Unknown repo subcommand: %s", args[0])
	}
}

func repoList(client *Client, outFmt string) {
	repos := fetchAllPages(client, "/api/v1/repositories", "repositories", "limit", listPageSize)

	printTable(repos, []string{"NAME", "FORMAT", "TYPE", "DESCRIPTION", "PUBLIC"},
		[]string{"name", "format", "repo_type", "description", "public"}, outFmt)
}

func repoCreate(client *Client, name string, args []string, outFmt string) {
	fs := flag.NewFlagSet("repo create", flag.ExitOnError)
	repoFmt := fs.String("format", "npm", "Repository format")
	desc := fs.String("description", "", "Description")
	public := fs.Bool("public", false, "Public repository")
	retention := fs.String("trash-retention-days", "", "Days deleted items stay in the trash (0 = delete immediately, default = server setting)")
	immutable := fs.Bool("immutable-releases", false, "Block deleting or overwriting released versions")
	_ = fs.Parse(args)

	set := setFlags(fs)
	body := map[string]any{
		"name":        name,
		"format":      *repoFmt,
		"description": *desc,
		"public":      *public,
	}

	if set["trash-retention-days"] {
		days, err := parseRetention(*retention)
		if err != nil {
			dief("%v", err)
		}

		if days != nil {
			body["trash_retention_days"] = days
		}
	}

	if set["immutable-releases"] {
		body["immutable_releases"] = *immutable
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

	for _, k := range []string{"name", "format", "repo_type", "description", "public", "upstream_url", "trash_retention_days", "immutable_releases", "merge_proxy_versions", "created_at", "updated_at"} {
		if v, ok := result[k]; ok {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", k, cell(v))
		}
	}

	_ = w.Flush()
}

// repoUpdate sends only the flags that were given: an omitted --public must
// not flip a public repository private.
func repoUpdate(client *Client, name string, args []string) {
	fs := flag.NewFlagSet("repo update", flag.ExitOnError)
	desc := fs.String("description", "", "Description")
	public := fs.Bool("public", false, "Public (--public=false to make private)")
	retention := fs.String("trash-retention-days", "", "Days deleted items stay in the trash (0 = delete immediately, default = server setting)")
	immutable := fs.Bool("immutable-releases", false, "Block deleting or overwriting released versions (--immutable-releases=false to allow)")
	mergeProxy := fs.Bool("merge-proxy-versions", false, "Group repositories, administrators only: merge proxy members' versions of names a hosted member holds (--merge-proxy-versions=false restores hosted-wins)")
	_ = fs.Parse(args)

	set := setFlags(fs)
	body := map[string]any{}

	if set["description"] {
		body["description"] = *desc
	}

	if set["public"] {
		body["public"] = *public
	}

	if set["trash-retention-days"] {
		days, err := parseRetention(*retention)
		if err != nil {
			dief("%v", err)
		}

		body["trash_retention_days"] = days
	}

	if set["immutable-releases"] {
		body["immutable_releases"] = *immutable
	}

	if set["merge-proxy-versions"] {
		body["merge_proxy_versions"] = *mergeProxy
	}

	if len(body) == 0 {
		dief("nothing to update: pass --description, --public[=false], --trash-retention-days, --immutable-releases[=false] or --merge-proxy-versions[=false]")
	}

	sendJSON(client, "PATCH", "/api/v1/repository/"+escape(name), body, http.StatusOK)
	fmt.Printf("Repository '%s' updated.\n", name)
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

// repoDelete deletes a repository. The server requires the repository name as
// confirmation, so --confirm (or an interactive prompt) is mandatory; the
// repository then waits in the trash for its retention period.
func repoDelete(client *Client, name string, args []string) {
	fs := flag.NewFlagSet("repo delete", flag.ExitOnError)
	confirm := fs.String("confirm", "", "Repository name, as confirmation")
	_ = fs.Parse(args)

	confirmed, err := resolveRepoConfirm(name, *confirm, setFlags(fs)["confirm"], stdinIsTerminal(), stdinReader, stderrPrompt)
	if err != nil {
		dief("%v", err)
	}

	resp, err := client.do("DELETE", "/api/v1/repository/"+escape(name)+"?confirm="+url.QueryEscape(confirmed), nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Printf("Repository '%s' deleted (kept in the trash until its retention expires, unless retention is 0).\n", name)
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
