// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
		artifactUploadCommand(client, args[1:])
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa artifact delete <repo> <id>")
		}

		artifactDelete(client, args[1], args[2])
	default:
		dief("Unknown artifact subcommand: %s", args[0])
	}
}

// artifactUpload stores one file in a hosted repository. The repository's
// format picks the upload endpoint: Alpine takes a multipart POST with the
// branch/repo/arch directory, every other uploadable format goes through the
// same code as `oa upload` (so both commands accept the same formats).
func artifactUpload(client *Client, opts uploadOptions) {
	api := newCIAPI(client)

	repo, err := api.repo(context.Background(), opts.repo)
	if err != nil {
		dief("%v", err)
	}

	if importFormat(repo.Format) == formatAPK {
		artifactUploadAPK(client, opts)
		return
	}

	if len(opts.branch)+len(opts.apkRepo)+len(opts.arch) > 0 {
		dief("--branch, --apk-repo and --arch only apply to Alpine (apk) repositories; %q is %s", opts.repo, repo.Format)
	}

	res, err := uploadToRepo(context.Background(), api, repo, ciUploadOptions{
		repo: opts.repo, patterns: []string{opts.filePath}, flat: true,
	})
	if err != nil {
		dief("%v", err)
	}

	fmt.Printf("Uploaded '%s' to '%s'.\n", filepath.Base(opts.filePath), res.Uploaded[0].Repo)
}

func artifactUploadAPK(client *Client, opts uploadOptions) {
	repo, filePath := opts.repo, opts.filePath

	file, err := os.Open(filePath)
	if err != nil {
		dief("opening file: %v", err)
	}
	defer func() { _ = file.Close() }()

	endpoint := "/api/v1/repositories/" + escape(repo) + "/apks"
	fields := [][2]string{{"branch", opts.branch}, {"repo", opts.apkRepo}, {"arch", opts.arch}}

	body, contentType := multipartStreamFields(fields, file, "file", filepath.Base(filePath))
	defer func() { _ = body.Close() }()

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
