// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/spf13/cobra"
)

func newSearchCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <query>...",
		Short: "Search repositories and packages",
		Long: "Search repositories and packages by name. Several words are joined into one query; " +
			"flags may come before or after the query.",
		Args:              usageArgs(1, -1),
		ValidArgsFunction: positionalRepeat(nothing),
	}

	repo := cmd.Flags().StringP("repo", "r", "", "Filter by repository")
	format := cmd.Flags().String("fmt", "", "Filter by format")
	limit := cmd.Flags().Int("limit", 50, "Max results")

	flagValues(cmd, "repo", a.repoNames())
	flagEnum(cmd, "fmt", repotype.SortedNames()...)

	cmd.Run = func(_ *cobra.Command, args []string) {
		search(a.client, strings.Join(args, " "), *repo, *format, *limit, a.outFmt)
	}

	return cmd
}

func search(client *Client, query, repoFilter, fmtFilter string, limit int, outFmt string) {
	if query == "" {
		dief("query is required")
	}

	q := url.Values{}
	q.Set("q", query)

	if repoFilter != "" {
		q.Set("repo", repoFilter)
	}

	if fmtFilter != "" {
		q.Set("format", fmtFilter)
	}

	q.Set("limit", strconv.Itoa(limit))

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
		if fmtFilter != "" && r["format"] != fmtFilter {
			continue
		}

		if len(repos) >= limit {
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
