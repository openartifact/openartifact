// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
)

// The commands in cmd_ci_*.go (upload, bundle, promote, version) are what CI
// plugins shell out to: they stay quiet on stdout except for their result, which
// is one JSON document under --format json, and they exit non-zero on any
// failure with the reason on stderr.

// ciListPageSize is the page size of the listings the CI commands walk.
const ciListPageSize = 100

var errCIUsage = errors.New("usage")

// ciUsagef is a command-line error; ciFail prints it like every other CLI error.
func ciUsagef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCIUsage, fmt.Sprintf(format, args...))
}

// ciFail reports err and exits non-zero. A usage error shows its text as is.
func ciFail(err error) {
	msg := err.Error()
	if errors.Is(err, errCIUsage) {
		msg = msg[len(errCIUsage.Error())+2:]
	}

	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	os.Exit(1)
}

// ciPrint writes the result of a command: v as JSON under --format json, text
// otherwise.
func ciPrint(outFmt string, v any, text string) {
	if outFmt == "json" {
		printJSON(v)
		return
	}

	fmt.Println(text)
}

// versionInfo is the document of `oa version --format json`.
type versionInfo struct {
	Version string `json:"version"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func currentVersionInfo() versionInfo {
	return versionInfo{Version: version, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
}

func handleVersion(outFmt string) {
	ciPrint(outFmt, currentVersionInfo(), "oa version "+version)
}

// ciAPI is the JSON client of the CI commands. It returns errors instead of
// exiting so the commands can report partial results.
type ciAPI struct {
	t *importTarget
}

func newCIAPI(c *Client) *ciAPI { return &ciAPI{t: &importTarget{c: c}} }

// call sends in (nil: no body) as JSON and decodes a 2xx response into out
// (nil: ignore it). Any other status is an error carrying the server's message.
func (a *ciAPI) call(ctx context.Context, method, apiPath string, in, out any) error {
	var (
		body   io.Reader
		length int64 = -1
		hdr    http.Header
	)

	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}

		body, length = bytes.NewReader(data), int64(len(data))
		hdr = http.Header{"Content-Type": {"application/json"}}
	}

	resp, err := a.t.request(ctx, method, apiPath, body, length, hdr)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, apiPath, redactURLError(err))
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return serverError(resp, method+" "+apiPath)
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decoding response: %w", method, apiPath, err)
	}

	return nil
}

// ciRepo is the part of a repository the CI commands need.
type ciRepo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Format   string `json:"format"`
	RepoType string `json:"repo_type"`
}

// hosted reports whether artifacts can be stored in (and promoted from) the
// repository; an empty type is a hosted repository of an older server.
func (r ciRepo) hosted() bool { return r.RepoType == "" || r.RepoType == "hosted" }

func (a *ciAPI) repo(ctx context.Context, name string) (ciRepo, error) {
	var r ciRepo
	if err := a.call(ctx, http.MethodGet, "/api/v1/repository/"+url.PathEscape(name), nil, &r); err != nil {
		return ciRepo{}, err
	}

	return r, nil
}

// repos lists the repositories the caller may read.
func (a *ciAPI) repos(ctx context.Context) ([]ciRepo, error) {
	var all []ciRepo

	for page := 1; ; page++ {
		var out struct {
			Total        int      `json:"total"`
			Repositories []ciRepo `json:"repositories"`
		}

		q := "?limit=" + strconv.Itoa(ciListPageSize) + "&page=" + strconv.Itoa(page)
		if err := a.call(ctx, http.MethodGet, "/api/v1/repositories"+q, nil, &out); err != nil {
			return nil, err
		}

		all = append(all, out.Repositories...)

		if len(out.Repositories) < ciListPageSize || (out.Total > 0 && len(all) >= out.Total) {
			return all, nil
		}
	}
}

// ciPackage is one row of a repository's package listing.
type ciPackage struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Format   string `json:"format"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// packages pages through a repository's package listing, narrowed by the text q
// when set, until fn returns false or the listing ends.
func (a *ciAPI) packages(ctx context.Context, repo, q string, fn func(ciPackage) bool) error {
	for page, seen := 1, 0; ; page++ {
		var out struct {
			Total    int         `json:"total"`
			Packages []ciPackage `json:"packages"`
		}

		query := url.Values{}
		query.Set("page", strconv.Itoa(page))
		query.Set("limit", strconv.Itoa(ciListPageSize))

		if q != "" {
			query.Set("q", q)
		}

		if err := a.call(ctx, http.MethodGet, "/api/v1/repositories/"+url.PathEscape(repo)+"/packages?"+query.Encode(), nil, &out); err != nil {
			return err
		}

		for _, p := range out.Packages {
			if !fn(p) {
				return nil
			}
		}

		seen += len(out.Packages)
		if len(out.Packages) == 0 || seen >= out.Total {
			return nil
		}
	}
}

// searchProperty returns the ids of the artifacts of repo whose property key is value.
func (a *ciAPI) searchProperty(ctx context.Context, repo, key, value string) ([]string, error) {
	q := url.Values{}
	q.Set("key", key)
	q.Set("value", value)

	var out struct {
		ArtifactIDs []string `json:"artifact_ids"`
	}

	if err := a.call(ctx, http.MethodGet, "/api/v1/repositories/"+url.PathEscape(repo)+"/artifacts/search?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}

	return out.ArtifactIDs, nil
}
