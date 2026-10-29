// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// mergeFindings combines the findings of several scanners into one list, in
// the order the vulnerabilities first appear. Two findings are the same
// vulnerability when they share an id or an alias: OSV reports a GHSA id and
// lists the CVE as an alias where trivy and grype report the CVE, and counting
// both would inflate every severity count. A finding without an id or alias is
// never merged. A finding that names two others that so far had nothing in
// common joins them into one.
//
// The first finding of a vulnerability keeps its id; the merge keeps the
// highest severity and CVSS score, lists every other id as an alias and takes
// the union of references and CWEs (see mergeFinding).
func mergeFindings(lists ...[]models.Vulnerability) []models.Vulnerability {
	var all []models.Vulnerability

	for _, list := range lists {
		all = append(all, list...)
	}

	// Union-find over the positions of all; the root of a set is its lowest
	// position, so the earliest finding of a vulnerability leads the merge.
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}

	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}

		return i
	}

	first := make(map[string]int, len(all)) // id or alias → first position that names it

	for i := range all {
		for _, key := range findingKeys(&all[i]) {
			j, seen := first[key]
			if !seen {
				first[key] = i

				continue
			}

			if ri, rj := find(i), find(j); ri != rj {
				parent[max(ri, rj)] = min(ri, rj)
			}
		}
	}

	out := make([]models.Vulnerability, 0, len(all))
	at := make(map[int]int, len(all)) // root → position in out

	for i := range all {
		root := find(i)

		if p, ok := at[root]; ok {
			out[p] = mergeFinding(out[p], all[i])

			continue
		}

		at[root] = len(out)
		out = append(out, all[i])
	}

	return out
}

// findingKeys returns the ids a finding is known by: its own and its aliases.
func findingKeys(v *models.Vulnerability) []string {
	keys := make([]string, 0, 1+len(v.Aliases))

	for _, k := range append([]string{v.ID}, v.Aliases...) {
		if k = strings.TrimSpace(k); k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}

	return keys
}

// mergeFinding folds b into a, two reports of one vulnerability: a keeps its id,
// the highest severity and CVSS score win, the other ids become aliases, and a
// field a leaves empty is taken from b.
func mergeFinding(a, b models.Vulnerability) models.Vulnerability {
	if severityRank(b.Severity) > severityRank(a.Severity) {
		a.Severity = b.Severity
	}

	a.CVSS = max(a.CVSS, b.CVSS)

	if a.Summary == "" {
		a.Summary = b.Summary
	}

	if a.FixedIn == "" {
		a.FixedIn = b.FixedIn
	}

	if a.PublishedAt == "" {
		a.PublishedAt = b.PublishedAt
	}

	a.References = unionStrings(a.References, b.References)
	a.CWEs = unionStrings(a.CWEs, b.CWEs)
	a.Aliases = cleanAliases(a.ID, slices.Concat(a.Aliases, []string{b.ID}, b.Aliases))

	return a
}

// unionStrings returns a followed by the elements of b that a lacks. It never
// writes into a, which a scanner may still hold.
func unionStrings(a, b []string) []string {
	out := a

	for _, s := range b {
		if slices.Contains(out, s) {
			continue
		}

		if len(out) == len(a) {
			out = slices.Concat(a, []string{s})

			continue
		}

		out = append(out, s)
	}

	return out
}

// severityRank orders the severity labels scanners report: CRITICAL highest,
// anything unrecognised (UNKNOWN, empty) lowest.
func severityRank(severity string) int {
	switch strings.ToUpper(severity) {
	case "CRITICAL":
		return 4
	case "HIGH":
		return 3
	case "MEDIUM":
		return 2
	case "LOW":
		return 1
	}

	return 0
}
