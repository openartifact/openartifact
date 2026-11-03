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

	"github.com/spf13/cobra"
)

func newArtifactCmd(a *app) *cobra.Command {
	return parentCmd("artifact", "List, upload and delete packages in a repository",
		&cobra.Command{
			Use:               "list <repo>",
			Short:             "List packages in a repository",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.repoNames()),
			Run:               func(_ *cobra.Command, args []string) { repoPackages(a.client, args[0], a.outFmt) },
		},
		artifactUploadCmd(a, runArtifactUpload),
		&cobra.Command{
			Use:               "delete <repo> <id>",
			Short:             "Delete a package by id",
			Args:              usageArgs(2, 2),
			ValidArgsFunction: positional(a.repoNames(), nothing),
			Run:               func(_ *cobra.Command, args []string) { artifactDelete(a.client, args[0], args[1]) },
		},
	)
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
