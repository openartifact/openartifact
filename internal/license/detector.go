// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Detector detects and validates licenses in artifacts.
type Detector struct {
	log *logger.Logger
}

// NewDetector creates a new license detector.
func NewDetector(log *logger.Logger) *Detector {
	return &Detector{log: log}
}

// licenseAliases maps common license names to SPDX identifiers.
//
//nolint:gochecknoglobals // immutable lookup table
var licenseAliases = map[string]string{
	"mit":                     "MIT",
	"mit license":             "MIT",
	"apache 2.0":              "Apache-2.0",
	"apache-2.0":              "Apache-2.0",
	"apache license 2.0":      "Apache-2.0",
	"apache software license": "Apache-2.0",
	"gpl-2.0":                 "GPL-2.0-only",
	"gpl-3.0":                 "GPL-3.0-only",
	"lgpl-2.1":                "LGPL-2.1-only",
	"lgpl-3.0":                "LGPL-3.0-only",
	"bsd-2-clause":            "BSD-2-Clause",
	"bsd-3-clause":            "BSD-3-Clause",
	"isc":                     "ISC",
	"mpl-2.0":                 "MPL-2.0",
	"cc0-1.0":                 "CC0-1.0",
	"unlicense":               "Unlicense",
	"agpl-3.0":                "AGPL-3.0-only",
	"eupl-1.2":                "EUPL-1.2",
}

// DetectFromMetadata extracts license info from artifact metadata map
// Looks for common keys: "license", "licenses", "License", "spdx_id".
func (d *Detector) DetectFromMetadata(metadata map[string]any) []string {
	var licenses []string

	// Check for common license keys
	licenseKeys := []string{"license", "licenses", "License", "Licenses", "spdx_id", "spdxId"}

	for _, key := range licenseKeys {
		if val, ok := metadata[key]; ok {
			switch v := val.(type) {
			case string:
				if v != "" {
					normalized := d.NormalizeLicense(v)
					if normalized != "" && !contains(licenses, normalized) {
						licenses = append(licenses, normalized)
					}
				}
			case []any:
				for _, item := range v {
					if str, ok := item.(string); ok && str != "" {
						normalized := d.NormalizeLicense(str)
						if normalized != "" && !contains(licenses, normalized) {
							licenses = append(licenses, normalized)
						}
					}
				}
			case []string:
				for _, str := range v {
					if str != "" {
						normalized := d.NormalizeLicense(str)
						if normalized != "" && !contains(licenses, normalized) {
							licenses = append(licenses, normalized)
						}
					}
				}
			}
		}
	}

	return licenses
}

// NormalizeLicense normalizes a license string to SPDX identifier
// e.g. "MIT License" -> "MIT", "Apache 2.0" -> "Apache-2.0".
func (d *Detector) NormalizeLicense(license string) string {
	if license == "" {
		return ""
	}

	// Trim whitespace and convert to lowercase for lookup
	normalized := strings.TrimSpace(license)
	lowerNorm := strings.ToLower(normalized)

	// Check if it's in the aliases map
	if spdx, ok := licenseAliases[lowerNorm]; ok {
		return spdx
	}

	// If not in aliases, return the original (trimmed) value
	// This allows for SPDX identifiers that are already correct
	return normalized
}

// EvaluatePolicy checks if licenses comply with a policy
// Returns (compliant bool, violations []string).
// License entries may be SPDX expressions ("GPL-3.0-only AND MIT"); each
// identifier in an expression is matched case-insensitively against the lists.
func (d *Detector) EvaluatePolicy(licenses []string, policy *models.LicensePolicy) (bool, []string) {
	if policy == nil || !policy.Enabled {
		return true, nil
	}

	var violations []string

	// If denied_licenses is non-empty and any identifier in any license matches → violation
	if len(policy.DeniedLicenses) > 0 {
		denied := normalizeSet(policy.DeniedLicenses)

		for _, license := range licenses {
			for _, id := range spdxIdentifiers(license) {
				if _, bad := denied[id]; bad {
					violations = append(violations, "license "+license+" is denied")
					break
				}
			}
		}
	}

	// If allowed_licenses is non-empty and no license is allowed → violation
	if len(policy.AllowedLicenses) > 0 {
		allowed := normalizeSet(policy.AllowedLicenses)
		found := false

		for _, license := range licenses {
			if expressionAllowed(license, allowed) {
				found = true
				break
			}
		}

		if !found && len(licenses) > 0 {
			violations = append(violations, "no license in allowed list")
		}
	}

	return len(violations) == 0, violations
}

func normalizeID(id string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(id)), "+")
}

func normalizeSet(list []string) map[string]struct{} {
	m := make(map[string]struct{}, len(list))
	for _, l := range list {
		m[normalizeID(l)] = struct{}{}
	}

	return m
}

// spdxIdentifiers returns the normalized license identifiers of an SPDX
// expression, dropping parentheses and the AND/OR/WITH operators.
func spdxIdentifiers(expr string) []string {
	fields := strings.FieldsFunc(expr, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '(' || r == ')' || r == ','
	})

	ids := make([]string, 0, len(fields)+1)
	ids = append(ids, normalizeID(expr)) // the whole string, for plain (even odd) names

	for _, f := range fields {
		switch strings.ToUpper(f) {
		case "AND", "OR", "WITH":
			continue
		}

		ids = append(ids, normalizeID(f))
	}

	return ids
}

// expressionAllowed: an expression joined by AND needs every identifier
// allowed; otherwise (single license or OR) any allowed identifier suffices.
func expressionAllowed(expr string, allowed map[string]struct{}) bool {
	if _, ok := allowed[normalizeID(expr)]; ok {
		return true
	}

	ids := spdxIdentifiers(expr)[1:]
	if len(ids) == 0 {
		return false
	}

	requireAll := strings.Contains(" "+strings.ToUpper(expr)+" ", " AND ")

	for _, id := range ids {
		_, ok := allowed[id]
		if requireAll && !ok {
			return false
		}

		if !requireAll && ok {
			return true
		}
	}

	return requireAll
}

// contains checks if a slice contains a string.
func contains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}
