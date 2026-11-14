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
	errCheckResponse = errors.New("unexpected availability response")
	errNoDevYarn     = errors.New("--no-dev: yarn.lock does not record which dependencies are development-only")
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
	// RequiredBy names the locked packages that depend on this one; empty for a
	// direct dependency. It comes from the lockfile, not from the server.
	RequiredBy []string `json:"required_by,omitempty"`
}

// checkQuery is one package version of an availability request: only what the
// server reads.
type checkQuery struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

// fetchSummary reports --fetch: how many packages' metadata was requested and
// how many requests did not answer 200.
type fetchSummary struct {
	Packages int `json:"packages"`
	Failed   int `json:"failed"`
}

// checkReport is what `oa check` prints with --format json.
type checkReport struct {
	Repository string         `json:"repository"`
	Lockfile   string         `json:"lockfile"`
	Kind       string         `json:"kind"`
	Summary    map[string]int `json:"summary"`
	Fetched    *fetchSummary  `json:"fetched,omitempty"`
	Results    []checkResult  `json:"results"`
}

func newCheckCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check <repo> --lockfile FILE",
		Short: "Check a lockfile's dependencies against a repository before installing",
		Long: "Read the package versions a lockfile pins (composer.lock, package-lock.json, npm-shrinkwrap.json, " +
			"pnpm-lock.yaml or yarn.lock) and ask " +
			"the repository, usually the group your package manager uses, whether it will serve each of them: available, " +
			"held (by a curation rule, the minimum age, a quarantine or the license policy, with the reason), not_found or " +
			"unknown (a proxy that has not cached the package's metadata yet). Local, linked, bundled and git or file " +
			"dependencies are skipped. REQUIRED BY lists the locked packages that require a dependency (\"-\" when none does, " +
			"so the project requires it itself); a package the project also requires directly is not marked as such. " +
			"Reading needs read access on the repository.\n\n" +
			"A proxy only knows the packages it has fetched, so on a fresh proxy most answers are unknown. --fetch first " +
			"requests each package's metadata through the repository, as the package manager would, so the proxies " +
			"behind it fetch and cache it from their upstreams; then it checks.\n\n" +
			"The exit status is 1 when a dependency is held or not found (or unknown, with --fail-on-unknown) and 0 " +
			"otherwise; the result is printed either way (--format json for every dependency).",
		Example: "  oa check php --lockfile composer.lock\n  oa check npm-all --lockfile package-lock.json --no-dev\n" +
			"  oa check php --lockfile composer.lock --fetch",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.repoNames()),
	}

	path := cmd.Flags().
		String("lockfile", "", "Lockfile to check: composer.lock, package-lock.json, npm-shrinkwrap.json, pnpm-lock.yaml or yarn.lock")
	noDev := cmd.Flags().Bool("no-dev", false, "Skip the dependencies the lockfile marks as development only (not with yarn.lock)")
	failUnknown := cmd.Flags().Bool("fail-on-unknown", false, "Exit 1 also when the repository cannot tell (unknown)")
	fetch := cmd.Flags().Bool("fetch", false, "First request each package's metadata through the repository, so its proxies cache it")

	flagValues(cmd, "lockfile", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"lock", "json", "yaml"}, cobra.ShellCompDirectiveFilterFileExt
	})

	cmd.RunE = func(c *cobra.Command, args []string) error {
		if *path == "" {
			return errCheckLockfile
		}

		report, err := runCheck(c.Context(), newCIAPI(a.client), args[0], *path, checkOptions{noDev: *noDev, fetch: *fetch})
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

// checkOptions are the flags of oa check that shape the run.
type checkOptions struct {
	noDev bool
	fetch bool
}

// runCheck parses the lockfile and asks repo about its dependencies, at most
// checkBatch per request; with fetch it first warms the repository's proxies.
func runCheck(ctx context.Context, api *ciAPI, repo, path string, opts checkOptions) (checkReport, error) {
	kind, err := lockfile.KindOf(path)
	if err != nil {
		return checkReport{}, err
	}

	if opts.noDev && kind == lockfile.Yarn {
		return checkReport{}, errNoDevYarn
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
		if !opts.noDev || !d.Dev {
			queries = append(queries, d)
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if opts.fetch {
		report.Fetched = warmMetadata(ctx, api, repo, kind, queries)
	}

	for start := 0; start < len(queries); start += checkBatch {
		chunk := queries[start:min(start+checkBatch, len(queries))]

		var out struct {
			Results []checkResult `json:"results"`
		}

		body := map[string]any{"packages": checkQueries(chunk)}
		if err := api.call(ctx, http.MethodPost, repoPath(repo, "availability"), body, &out); err != nil {
			return checkReport{}, err
		}

		if len(out.Results) != len(chunk) {
			return checkReport{}, fmt.Errorf("%w: %d results for %d dependencies", errCheckResponse, len(out.Results), len(chunk))
		}

		for i, r := range out.Results {
			// The server answers one result per entry, in request order.
			if r.Package != chunk[i].Name || r.Version != chunk[i].Version {
				return checkReport{}, fmt.Errorf("%w: result %d is %s %s, want %s %s",
					errCheckResponse, start+i, r.Package, r.Version, chunk[i].Name, chunk[i].Version)
			}

			r.Dev, r.RequiredBy = chunk[i].Dev, chunk[i].RequiredBy
			report.Summary[r.Status]++
			report.Results = append(report.Results, r)
		}
	}

	return report, nil
}

// checkQueries is the request body of deps: the package and version only.
func checkQueries(deps []lockfile.Dep) []checkQuery {
	out := make([]checkQuery, len(deps))
	for i, d := range deps {
		out[i] = checkQuery{Package: d.Name, Version: d.Version}
	}

	return out
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

	if r.Fetched != nil {
		fmt.Fprintf(&b, "Fetched the metadata of %d packages through %s", r.Fetched.Packages, r.Repository)
		if r.Fetched.Failed > 0 {
			fmt.Fprintf(&b, " (%d did not answer 200)", r.Fetched.Failed)
		}

		b.WriteString(".\n\n")
	}

	if total == 0 {
		fmt.Fprintf(&b, "No registry dependencies to check in %s.", r.Lockfile)
		return b.String()
	}

	if r.Summary[statusAvailable] == total {
		fmt.Fprintf(&b, "All %s of %s are available from %s.", dependencies(total), r.Lockfile, r.Repository)
		return b.String()
	}

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "STATUS\tPACKAGE\tVERSION\tFROM\tREQUIRED BY\tREASON")

	for _, res := range r.Results {
		if res.Status == statusAvailable {
			continue
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			res.Status, res.Package, res.Version, res.Repository, requiredByLine(res.RequiredBy), reasonLine(res.Reasons))
	}

	_ = w.Flush()

	fmt.Fprintf(&b, "\n%s: %d available, %d held, %d not found, %d unknown.", dependencies(total),
		r.Summary[statusAvailable], r.Summary[statusHeld], r.Summary[statusNotFound], r.Summary[statusUnknown])

	return b.String()
}

// requiredByLine is the locked packages that pull a dependency in, up to three
// and how many more; "-" when no locked package requires it. The lockfile does
// not say whether the project also requires a package itself, so a list does
// not mean the dependency is only indirect.
func requiredByLine(by []string) string {
	const shown = 3

	switch {
	case len(by) == 0:
		return "-"
	case len(by) <= shown:
		return strings.Join(by, ", ")
	default:
		return strings.Join(by[:shown], ", ") + fmt.Sprintf(" +%d more", len(by)-shown)
	}
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
