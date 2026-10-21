// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"net/http"
)

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
			dief("Usage: oa repo create <name> [--format fmt] [--description D] [--public]")
		}

		repoCreate(client, args[1], args[2:], outFmt)
	case "show":
		if len(args) < 2 {
			dief("Usage: oa repo show <name>")
		}

		repoShow(client, args[1], outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa repo update <name> [--description D] [--public[=false]]")
		}

		repoUpdate(client, args[1], args[2:])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa repo delete <name>")
		}

		repoDelete(client, args[1])
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
	_ = fs.Parse(args)

	result := sendJSON(client, "POST", "/api/v1/repositories", map[string]any{
		"name":        name,
		"format":      *repoFmt,
		"description": *desc,
		"public":      *public,
	}, http.StatusCreated)

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

	for _, k := range []string{"name", "format", "repo_type", "description", "public", "upstream_url", "created_at", "updated_at"} {
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
	_ = fs.Parse(args)

	set := setFlags(fs)
	body := map[string]any{}

	if set["description"] {
		body["description"] = *desc
	}

	if set["public"] {
		body["public"] = *public
	}

	if len(body) == 0 {
		dief("nothing to update: pass --description and/or --public[=false]")
	}

	sendJSON(client, "PATCH", "/api/v1/repository/"+escape(name), body, http.StatusOK)
	fmt.Printf("Repository '%s' updated.\n", name)
}

func repoDelete(client *Client, name string) {
	resp, err := client.do("DELETE", "/api/v1/repository/"+escape(name), nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Printf("Repository '%s' deleted.\n", name)
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
