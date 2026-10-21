// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func handleArtifact(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa artifact <list|upload|delete>")
	}

	switch args[0] {
	case "list":
		if len(args) < 2 {
			dief("Usage: oa artifact list <repo>")
		}

		repoPackages(client, args[1], outFmt)
	case "upload":
		if len(args) < 3 {
			dief("Usage: oa artifact upload <repo> <file>")
		}

		artifactUpload(client, args[1], args[2])
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa artifact delete <repo> <id>")
		}

		artifactDelete(client, args[1], args[2])
	default:
		dief("Unknown artifact subcommand: %s", args[0])
	}
}

func artifactUpload(client *Client, repo, filePath string) {
	file, err := os.Open(filePath)
	if err != nil {
		dief("opening file: %v", err)
	}
	defer func() { _ = file.Close() }()

	body, contentType := multipartStream(file, filepath.Base(filePath))
	defer func() { _ = body.Close() }()

	// Choose endpoint based on file extension
	endpoint := "/api/v1/repositories/" + escape(repo) + "/packages"
	if strings.ToLower(filepath.Ext(filePath)) == ".deb" {
		endpoint = "/api/v1/repositories/" + escape(repo) + "/debs"
	}

	resp, err := client.do("POST", endpoint, body, contentType)
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusCreated, http.StatusOK)
	fmt.Printf("Uploaded '%s' to '%s'.\n", filepath.Base(filePath), repo)
}

// artifactDelete removes a package by id. Debian repositories delete through
// the deb endpoint, everything else through the package endpoint.
func artifactDelete(client *Client, repo, id string) {
	var info struct {
		Format string `json:"format"`
	}

	getJSON(client, "/api/v1/repository/"+escape(repo), &info)

	endpoint := "/api/v1/repositories/" + escape(repo) + "/packages/" + escape(id)
	if info.Format == "deb" {
		endpoint = "/api/v1/repositories/" + escape(repo) + "/debs/" + escape(id)
	}

	resp, err := client.do("DELETE", endpoint, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Printf("Artifact '%s' deleted from '%s'.\n", id, repo)
}
