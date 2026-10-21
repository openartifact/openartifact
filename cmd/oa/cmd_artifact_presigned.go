// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const uploadUsage = "Usage: oa artifact upload [--presigned [--path P] [--content-type T]] <repo> <file>"

// artifactUploadCommand parses `oa artifact upload`. Flags may appear before or
// after the positional arguments.
func artifactUploadCommand(client *Client, args []string) {
	fs := flag.NewFlagSet("artifact upload", flag.ExitOnError)
	presigned := fs.Bool("presigned", false, "Upload straight to the object store through a presigned URL (raw repositories)")
	remotePath := fs.String("path", "", "Artifact path in the repository (--presigned; default: the file name)")
	contentType := fs.String("content-type", "", "Content type (--presigned; default: detected by the server)")

	var positional []string

	for rest := args; ; {
		_ = fs.Parse(rest)

		if fs.NArg() == 0 {
			break
		}

		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	if len(positional) != 2 {
		dief("%s", uploadUsage)
	}

	if *presigned {
		artifactUploadPresigned(client, positional[0], positional[1], *remotePath, *contentType)
		return
	}

	if *remotePath != "" || *contentType != "" {
		dief("--path and --content-type require --presigned")
	}

	artifactUpload(client, positional[0], positional[1])
}

// artifactUploadPresigned uploads a file in three steps: ask the server for a
// presigned PUT target (declaring size and sha256), PUT the bytes straight to
// the object store, and ask the server to verify and record the artifact. The
// upload itself bypasses the OpenArtifact server; verification does not: the
// server reads the object back from the store to check size and sha256, which
// can take a while for a large file.
func artifactUploadPresigned(client *Client, repo, filePath, remotePath, contentType string) {
	file, err := os.Open(filePath)
	if err != nil {
		dief("opening file: %v", err)
	}
	defer func() { _ = file.Close() }()

	hasher := sha256.New()

	size, err := io.Copy(hasher, file)
	if err != nil {
		dief("hashing file: %v", err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		dief("rewinding file: %v", err)
	}

	if remotePath == "" {
		remotePath = filepath.Base(filePath)
	}

	base := "/api/v1/repositories/" + escape(repo) + "/uploads"

	target := sendJSON(client, "POST", base, map[string]any{
		"path":         remotePath,
		"size":         size,
		"sha256":       hex.EncodeToString(hasher.Sum(nil)),
		"content_type": contentType,
	}, http.StatusCreated)

	id, _ := target["id"].(string)
	uploadURL, _ := target["upload_url"].(string)

	if id == "" || uploadURL == "" {
		dief("server returned an incomplete upload target")
	}

	headers, _ := target["headers"].(map[string]any)

	putPresigned(client, uploadURL, headers, file, size)

	done := completePresigned(client, base+"/"+escape(id)+"/complete")

	fmt.Printf("Uploaded '%s' to '%s' as '%s' (%d bytes, sha256 %v).\n", filepath.Base(filePath), repo, remotePath, size, done["sha256"])
}

// maxCompleteWait bounds how long the CLI keeps retrying a complete call that
// answers "in progress" (the server gives a verification 30 minutes).
const maxCompleteWait = 35 * time.Minute

// completePresigned asks the server to verify and record the upload and returns
// the artifact. The server hashes the whole object before answering, so there
// is no response-header timeout here, and a 409 upload_in_progress (an earlier
// attempt is still verifying) is retried until it yields the result.
func completePresigned(client *Client, path string) map[string]any {
	slow := *client
	slow.httpClient = newHTTPClientWithHeaderTimeout(0)

	deadline := time.Now().Add(maxCompleteWait)

	for {
		resp, err := slow.doJSON("POST", path, nil)
		if err != nil {
			dief("completing upload: %v", err)
		}

		if resp.StatusCode == http.StatusConflict && time.Now().Before(deadline) {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			var e struct {
				Code string `json:"code"`
			}

			if json.Unmarshal(body, &e) == nil && e.Code == "upload_in_progress" {
				time.Sleep(retryAfter(resp))
				continue
			}

			resp.Body = io.NopCloser(bytes.NewReader(body))
		}

		checkOK(resp, http.StatusCreated, http.StatusOK)

		var out map[string]any

		decodeJSON(resp, &out)
		_ = resp.Body.Close()

		return out
	}
}

// retryAfter reads a Retry-After header in seconds (default 2s).
func retryAfter(resp *http.Response) time.Duration {
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}

	return 2 * time.Second
}

// putPresigned sends the object to the presigned URL. The OpenArtifact token is
// deliberately not attached: the URL itself is the credential, and errors never
// print it.
func putPresigned(client *Client, uploadURL string, headers map[string]any, body io.Reader, size int64) {
	if size == 0 {
		body = http.NoBody
	}

	//nolint:noctx // CLI tool, no context available
	req, err := http.NewRequest(http.MethodPut, uploadURL, body)
	if err != nil {
		dief("building upload request: %v", redactURLError(err))
	}

	req.ContentLength = size

	for k, v := range headers {
		if s, ok := v.(string); ok {
			req.Header.Set(k, s)
		}
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		dief("uploading to object store: %v", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		dief("object store rejected the upload: HTTP %d %s", resp.StatusCode, msg)
	}
}

// redactURLError drops the URL (whose query string is a credential) from a
// net/http error.
func redactURLError(err error) error {
	if ue, ok := err.(*url.Error); ok { //nolint:errorlint // url.Error is returned unwrapped by net/http
		return ue.Err
	}

	return err
}
