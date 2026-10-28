// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/osv"
)

// NameVersion is a package version to match against the mirror.
type NameVersion struct {
	Name    string
	Version string
}

// Mirror matches package versions against the local OSV mirror (see
// internal/osv). It is what the osv scanner uses instead of the online API when
// the mirror is enabled and populated, and what the continuous re-match of the
// stored packages evaluates.
type Mirror struct {
	store database.OSVStore
}

// NewMirror creates a Mirror over the advisory store.
func NewMirror(store database.OSVStore) *Mirror { return &Mirror{store: store} }

// Ready reports whether the mirror holds a completed import of an OSV
// ecosystem, so a match against it is meaningful. A mirror that was never
// imported, or whose first import has not finished, reports false: callers fall
// back to the online API.
func (m *Mirror) Ready(ctx context.Context, ecosystem string) (bool, error) {
	if !osv.Supported(ecosystem) {
		return false, nil
	}

	st, err := m.store.GetOSVMirrorState(ctx, ecosystem)
	if err != nil {
		return false, fmt.Errorf("read osv mirror state: %w", err)
	}

	return st != nil && st.HighWater != nil, nil
}

// Match returns, for each package version, the advisories that affect it (by
// advisory id), in the order of pkgs. Names are matched after normalization;
// withdrawn advisories never match.
func (m *Mirror) Match(ctx context.Context, ecosystem string, pkgs []NameVersion) ([][]models.Vulnerability, error) {
	norms := make([]string, 0, len(pkgs))
	seen := make(map[string]bool, len(pkgs))

	for _, p := range pkgs {
		n := osv.NormalizeName(ecosystem, p.Name)
		if !seen[n] {
			seen[n] = true
			norms = append(norms, n)
		}
	}

	byName, err := m.store.ListOSVAdvisoriesForPackages(ctx, ecosystem, norms)
	if err != nil {
		return nil, fmt.Errorf("list osv advisories: %w", err)
	}

	out := make([][]models.Vulnerability, len(pkgs))

	for i, p := range pkgs {
		out[i] = matchPackage(byName[osv.NormalizeName(ecosystem, p.Name)], p.Version)
	}

	return out, nil
}

// matchPackage returns the findings of the advisories that affect version,
// sorted by advisory id.
func matchPackage(advisories []models.OSVAdvisoryMatch, version string) []models.Vulnerability {
	var vulns []models.Vulnerability

	for i := range advisories {
		hit, fixed := false, ""

		for _, aff := range advisories[i].Affected {
			affected, fix := osv.Affects(aff, version)
			if !affected {
				continue
			}

			hit = true

			if fixed == "" {
				fixed = fix
			}
		}

		if hit {
			vulns = append(vulns, findingFromAdvisory(&advisories[i].Advisory, fixed))
		}
	}

	slices.SortFunc(vulns, func(a, b models.Vulnerability) int { return strings.Compare(a.ID, b.ID) })

	return vulns
}

// findingFromAdvisory converts a mirror advisory the way the online scanner
// converts an OSV API record.
func findingFromAdvisory(adv *models.OSVAdvisory, fixed string) models.Vulnerability {
	var severities []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	}

	if len(adv.Severity) > 0 {
		_ = json.Unmarshal(adv.Severity, &severities)
	}

	v := models.Vulnerability{
		ID:         adv.ID,
		Summary:    adv.Summary,
		FixedIn:    fixed,
		References: adv.Refs,
		CWEs:       adv.CWEIDs,
	}

	if adv.Published != nil {
		v.PublishedAt = adv.Published.UTC().Format(time.RFC3339)
	}

	v.Severity, v.CVSS = parseSeverityFromOSV(severities, adv.DBSeverity)

	return v
}

// CountSeverities tallies findings by severity class; unknown severities are
// not counted.
func CountSeverities(vulns []models.Vulnerability) (int, int, int, int) {
	var critical, high, medium, low int

	for _, v := range vulns {
		switch strings.ToUpper(v.Severity) {
		case "CRITICAL":
			critical++
		case "HIGH":
			high++
		case "MEDIUM":
			medium++
		case "LOW":
			low++
		}
	}

	return critical, high, medium, low
}
