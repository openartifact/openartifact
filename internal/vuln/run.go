// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Scan statuses recorded for a finished scan.
const (
	// StatusCompleted: every scanner ran and its findings are in the result.
	StatusCompleted = "completed"
	// StatusPartial: several scanners ran and some failed; the findings of the
	// others are in the result and the failed ones are listed.
	StatusPartial = "partial"
	// StatusFailed: no scanner produced a result.
	StatusFailed = "failed"
)

// Outcome is the combined result of running one or more scanners on a package.
type Outcome struct {
	// Status is StatusCompleted, StatusPartial or StatusFailed.
	Status string
	// Vulnerabilities holds the deduplicated findings of the scanners that ran.
	// It is empty for StatusFailed.
	Vulnerabilities []models.Vulnerability
	// Failures lists the scanners that did not produce a result, sorted by name.
	Failures []*ScanError
}

// FailedScanners returns the names of the scanners that failed, sorted.
func (o Outcome) FailedScanners() []string {
	names := make([]string, 0, len(o.Failures))
	for _, f := range o.Failures {
		names = append(names, f.Scanner)
	}

	return names
}

// PublicError returns the client-safe description of the failures
// ("trivy: scanner is not available on the server; osv: scan timed out"), or
// "" when no scanner failed. It never contains raw scanner or upstream text.
func (o Outcome) PublicError() string {
	parts := make([]string, 0, len(o.Failures))
	for _, f := range o.Failures {
		parts = append(parts, f.Scanner+": "+f.PublicMessage())
	}

	return strings.Join(parts, "; ")
}

// Run scans the package with every backend concurrently and combines the
// results. A scanner that fails never turns into "no vulnerabilities": the
// outcome is StatusFailed when all of them failed and StatusPartial when only
// some did. Failure details are logged here; Outcome carries only what is safe
// to show clients.
func Run(ctx context.Context, log *logger.Logger, backends []ScannerBackend, packageName, version, ecosystem string) Outcome {
	type result struct {
		vulns []models.Vulnerability
		err   *ScanError
	}

	results := make([]result, len(backends))

	var wg sync.WaitGroup

	for i, backend := range backends {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					results[i] = result{err: newScanError(backend.Name(), FailureInternal, fmt.Errorf("%w: %v", errScannerPanic, r))}
				}
			}()

			vulns, err := backend.Scan(ctx, packageName, version, ecosystem)
			if err != nil {
				results[i] = result{err: classify(backend.Name(), err)}
				return
			}

			results[i] = result{vulns: vulns}
		})
	}

	wg.Wait()

	var out Outcome

	seen := make(map[string]struct{})

	for _, res := range results {
		if res.err != nil {
			log.Errorw("vuln scan failed",
				"scanner", res.err.Scanner,
				"kind", string(res.err.Kind),
				"error", res.err,
				"package", packageName,
				"version", version,
				"ecosystem", ecosystem,
			)

			out.Failures = append(out.Failures, res.err)

			continue
		}

		for _, v := range res.vulns {
			if v.ID != "" {
				if _, dup := seen[v.ID]; dup {
					continue
				}

				seen[v.ID] = struct{}{}
			}

			out.Vulnerabilities = append(out.Vulnerabilities, v)
		}
	}

	slices.SortStableFunc(out.Failures, func(a, b *ScanError) int { return strings.Compare(a.Scanner, b.Scanner) })

	switch {
	case len(backends) == 0 || len(out.Failures) == len(backends):
		out.Status = StatusFailed
		out.Vulnerabilities = nil
	case len(out.Failures) > 0:
		out.Status = StatusPartial
	default:
		out.Status = StatusCompleted
	}

	if out.Vulnerabilities == nil {
		out.Vulnerabilities = []models.Vulnerability{}
	}

	return out
}
