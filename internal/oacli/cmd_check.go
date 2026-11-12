// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/openartifact/openartifact/pkg/lockfile"
)

const (
	// checkBatch is the most package versions one availability request carries
	// (the server's limit).
	checkBatch = 1000
	// maxLockfileBytes bounds the lockfile oa check reads.
	maxLockfileBytes = 64 << 20

	statusAvailable = "available"
	statusHeld      = "held"
	statusNotFound  = "not_found"
	statusUnknown   = "unknown"
)

var (
	errCheckLockfile = errors.New("--lockfile is required")
	errLockfileSize  = fmt.Errorf("lockfile larger than %d MiB", maxLockfileBytes>>20)
)

// checkReason is one reason of an availability result.
type checkReason struct {
	Source string `json:"source"`
	Detail string `json:"detail"`
	Until  string `json:"until,omitempty"`
	RuleID string `json:"rule_id,omitempty"`
}

// checkResult is the server's verdict on one package version.
type checkResult struct {
	Package     string        `json:"package"`
	Version     string        `json:"version"`
	Status      string        `json:"status"`
	Repository  string        `json:"repository"`
	Available   bool          `json:"available"`
	Reasons     []checkReason `json:"reasons"`
	MinAge      string        `json:"min_age,omitempty"`
	PublishedAt string        `json:"published_at,omitempty"`
	Dev         bool          `json:"dev,omitempty"`
}

// checkReport is what `oa check` prints with --format json.
type checkReport struct {
	Repository string         `json:"repository"`
	Lockfile   string         `json:"lockfile"`
	Kind       string         `json:"kind"`
	Summary    map[string]int `json:"summary"`
	Results    []checkResult  `json:"results"`
}

func newCheckCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check <repo> --lockfile FILE",
		Short: "Check a lockfile's dependencies against a repository before installing",
		Long: "Read the package versions a lockfile pins (composer.lock, package-lock.json or npm-shrinkwrap.json) and ask " +
			"the repository, usually the group your package manager uses, whether it will serve each of them: available, " +
			"held (by a curation rule, the minimum age, a quarantine or the license policy, with the reason), not_found or " +
			"unknown (a proxy that has not cached the package's metadata yet). Local, linked, bundled and git or file " +
			"dependencies are skipped. Reading needs read access on the repository.\n\n" +
			"The exit status is 1 when a dependency is held or not found (or unknown, with --fail-on-unknown) and 0 " +
			"otherwise; the result is printed either way (--format json for every dependency).",
		Example:           "  oa check php --lockfile composer.lock\n  oa check npm-all --lockfile package-lock.json --no-dev",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.repoNames()),
	}

	path := cmd.Flags().String("lockfile", "", "Lockfile to check: composer.lock, package-lock.json or npm-shrinkwrap.json")
	noDev := cmd.Flags().Bool("no-dev", false, "Skip the dependencies the lockfile marks as development only")
	failUnknown := cmd.Flags().Bool("fail-on-unknown", false, "Exit 1 also when the repository cannot tell (unknown)")

	flagValues(cmd, "lockfile", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"lock", "json"}, cobra.ShellCompDirectiveFilterFileExt
	})

	cmd.RunE = func(c *cobra.Command, args []string) error {
		if *path == "" {
			return errCheckLockfile
		}

		report, err := runCheck(c.Context(), newCIAPI(a.client), args[0], *path, *noDev)
		if err != nil {
			return err
		}

		ciPrint(a.outFmt, report, report.text())

		if report.Summary[statusHeld]+report.Summary[statusNotFound] > 0 || (*failUnknown && report.Summary[statusUnknown] > 0) {
			return exitWith(1)
		}

		return nil
	}

	return cmd
}

// runCheck parses the lockfile and asks repo about its dependencies, at most
// checkBatch per request.
func runCheck(ctx context.Context, api *ciAPI, repo, path string, noDev bool) (checkReport, error) {
	kind, err := lockfile.KindOf(path)
	if err != nil {
		return checkReport{}, err
	}

	data, err := readLockfile(path)
	if err != nil {
		return checkReport{}, err
	}

	deps, err := lockfile.Parse(kind, data)
	if err != nil {
		return checkReport{}, fmt.Errorf("%s: %w", path, err)
	}

	report := checkReport{
		Repository: repo, Lockfile: path, Kind: string(kind),
		Summary: map[string]int{statusAvailable: 0, statusHeld: 0, statusNotFound: 0, statusUnknown: 0},
		Results: []checkResult{},
	}

	var queries []lockfile.Dep

	for _, d := range deps {
		if !noDev || !d.Dev {
			queries = append(queries, d)
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	for start := 0; start < len(queries); start += checkBatch {
		chunk := queries[start:min(start+checkBatch, len(queries))]

		var out struct {
			Results []checkResult `json:"results"`
		}

		body := map[string]any{"packages": chunk}
		if err := api.call(ctx, http.MethodPost, repoPath(repo, "availability"), body, &out); err != nil {
			return checkReport{}, err
		}

		for i, r := range out.Results {
			if i < len(chunk) {
				r.Dev = chunk[i].Dev
			}

			report.Summary[r.Status]++
			report.Results = append(report.Results, r)
		}
	}

	return report, nil
}

// readLockfile reads path, refusing a file over maxLockfileBytes.
func readLockfile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read lockfile: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxLockfileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read lockfile: %w", err)
	}

	if len(data) > maxLockfileBytes {
		return nil, errLockfileSize
	}

	return data, nil
}

// text is the table of the dependencies that are not available, and a summary.
func (r checkReport) text() string {
	var b strings.Builder

	total := len(r.Results)

	if r.Summary[statusAvailable] == total {
		fmt.Fprintf(&b, "All %s of %s are available from %s.", dependencies(total), r.Lockfile, r.Repository)
		return b.String()
	}

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "STATUS\tPACKAGE\tVERSION\tFROM\tREASON")

	for _, res := range r.Results {
		if res.Status == statusAvailable {
			continue
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", res.Status, res.Package, res.Version, res.Repository, reasonLine(res.Reasons))
	}

	_ = w.Flush()

	fmt.Fprintf(&b, "\n%s: %d available, %d held, %d not found, %d unknown.", dependencies(total),
		r.Summary[statusAvailable], r.Summary[statusHeld], r.Summary[statusNotFound], r.Summary[statusUnknown])

	return b.String()
}

// dependencies is "1 dependency" or "n dependencies".
func dependencies(n int) string {
	if n == 1 {
		return "1 dependency"
	}

	return fmt.Sprintf("%d dependencies", n)
}

// reasonLine is the first reason of a result, with when a minimum-age hold ends.
func reasonLine(reasons []checkReason) string {
	if len(reasons) == 0 {
		return ""
	}

	r := reasons[0]

	line := r.Detail
	if r.Until != "" {
		line += " (until " + r.Until + ")"
	}

	if len(reasons) > 1 {
		line += fmt.Sprintf(" (+%d more)", len(reasons)-1)
	}

	return strings.Join(strings.Fields(line), " ")
}
