// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════════════
// SEARCH
// ═══════════════════════════════════════════════════════════════════════════════

func handleSearch(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa search <query> [--repo R] [--fmt F]")
	}

	fs := flag.NewFlagSet("search", flag.ExitOnError)
	repoFilter := fs.String("repo", "", "Filter by repository")
	fmtFilter := fs.String("fmt", "", "Filter by format")
	limit := fs.Int("limit", 50, "Max results")
	_ = fs.Parse(args)

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		dief("query is required")
	}

	q := url.Values{}
	q.Set("q", query)

	if *repoFilter != "" {
		q.Set("repo", *repoFilter)
	}

	if *fmtFilter != "" {
		q.Set("format", *fmtFilter)
	}

	q.Set("limit", strconv.Itoa(*limit))

	resp, err := client.do("GET", "/api/v1/search?"+q.Encode(), nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	var result struct {
		Repositories []map[string]any `json:"repositories"`
		Results      []map[string]any `json:"packages"`
	}
	decodeJSON(resp, &result)

	repos := result.Repositories[:0:0]

	for _, r := range result.Repositories {
		if *fmtFilter != "" && r["format"] != *fmtFilter {
			continue
		}

		if len(repos) >= *limit {
			break
		}

		repos = append(repos, r)
	}

	if outFmt == "json" {
		printJSON(map[string]any{"repositories": repos, "results": result.Results})
		return
	}

	w := newTabWriter()

	if len(repos) > 0 {
		_, _ = fmt.Fprintln(w, "REPOSITORY\tFORMAT\tDESCRIPTION")
		for _, r := range repos {
			_, _ = fmt.Fprintf(w, "%v\t%v\t%v\n", r["name"], r["format"], r["description"])
		}

		_, _ = fmt.Fprintln(w)
	}

	_, _ = fmt.Fprintln(w, "NAME\tREPO\tVERSION\tFORMAT")
	for _, r := range result.Results {
		_, _ = fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", r["name"], r["repository"], r["version"], r["format"])
	}

	_ = w.Flush()
}
