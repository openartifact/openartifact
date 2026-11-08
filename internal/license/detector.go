// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"slices"
	"strings"
	"unicode"

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

	// Spellings common in RPM, Debian and PyPI metadata.
	"gplv2":               "GPL-2.0-only",
	"gplv3":               "GPL-3.0-only",
	"gpl v2":              "GPL-2.0-only",
	"gpl v3":              "GPL-3.0-only",
	"lgplv2":              "LGPL-2.0-only",
	"lgplv2.1":            "LGPL-2.1-only",
	"lgplv3":              "LGPL-3.0-only",
	"agplv3":              "AGPL-3.0-only",
	"asl 2.0":             "Apache-2.0",
	"asl 1.1":             "Apache-1.1",
	"apache license, 2.0": "Apache-2.0",
	"apache-2":            "Apache-2.0",
	"mpl 2.0":             "MPL-2.0",

	// PyPI trove classifier names (after the "License :: ... ::" prefix).
	"gnu general public license v2 (gplv2)":                   "GPL-2.0-only",
	"gnu general public license v2 or later (gplv2+)":         "GPL-2.0-or-later",
	"gnu general public license v3 (gplv3)":                   "GPL-3.0-only",
	"gnu general public license v3 or later (gplv3+)":         "GPL-3.0-or-later",
	"gnu lesser general public license v2 (lgplv2)":           "LGPL-2.0-only",
	"gnu lesser general public license v2 or later (lgplv2+)": "LGPL-2.0-or-later",
	"gnu lesser general public license v3 (lgplv3)":           "LGPL-3.0-only",
	"gnu lesser general public license v3 or later (lgplv3+)": "LGPL-3.0-or-later",
	"gnu affero general public license v3":                    "AGPL-3.0-only",
	"gnu affero general public license v3 or later (agplv3+)": "AGPL-3.0-or-later",
	"apache software license 2.0":                             "Apache-2.0",
	"mozilla public license 2.0 (mpl 2.0)":                    "MPL-2.0",

	// More trove classifiers that name exactly one license. Classifiers that
	// do not ("BSD License", "GNU General Public License (GPL)", "Artistic
	// License", "Public Domain") are deliberately absent: they stay as written
	// and are flagged as unresolved.
	"python software foundation license":                         "PSF-2.0",
	"isc license (iscl)":                                         "ISC",
	"mit no attribution license (mit-0)":                         "MIT-0",
	"the unlicense (unlicense)":                                  "Unlicense",
	"cc0 1.0 universal (cc0 1.0) public domain dedication":       "CC0-1.0",
	"mozilla public license 1.1 (mpl 1.1)":                       "MPL-1.1",
	"european union public licence 1.1 (eupl 1.1)":               "EUPL-1.1",
	"european union public licence 1.2 (eupl 1.2)":               "EUPL-1.2",
	"boost software license 1.0 (bsl-1.0)":                       "BSL-1.0",
	"zlib/libpng license":                                        "Zlib",
	"eclipse public license 1.0 (epl-1.0)":                       "EPL-1.0",
	"eclipse public license 2.0 (epl-2.0)":                       "EPL-2.0",
	"common development and distribution license 1.0 (cddl-1.0)": "CDDL-1.0",
	"do what the f*ck you want to public license (wtfpl)":        "WTFPL",
	"postgresql license":                                         "PostgreSQL",
	"w3c license":                                                "W3C",
	"university of illinois/ncsa open source license":            "NCSA",
	"sil open font license 1.1 (ofl-1.1)":                        "OFL-1.1",
	"open software license 3.0 (osl-3.0)":                        "OSL-3.0",
	"historical permission notice and disclaimer (hpnd)":         "HPND",

	// Names Maven poms commonly use.
	"the apache software license, version 2.0": "Apache-2.0",
	"the apache license, version 2.0":          "Apache-2.0",
	"apache license, version 2.0":              "Apache-2.0",
	"the mit license":                          "MIT",
}

// licenseKeys are the metadata (and artifact property) keys DetectFromMetadata
// reads licenses from.
//
//nolint:gochecknoglobals // immutable lookup table
var licenseKeys = []string{"license", "licenses", "License", "Licenses", "spdx_id", "spdxId"}

// IsLicenseKey reports whether key is a metadata / property key that carries
// license information.
func IsLicenseKey(key string) bool {
	return slices.Contains(licenseKeys, key)
}

// DetectFromMetadata extracts license info from artifact metadata map
// Looks for common keys: "license", "licenses", "License", "spdx_id".
func (d *Detector) DetectFromMetadata(metadata map[string]any) []string {
	var licenses []string

	add := func(raw string) {
		if raw == "" {
			return
		}

		if normalized := d.NormalizeLicense(raw); normalized != "" && !contains(licenses, normalized) {
			licenses = append(licenses, normalized)
		}
	}

	for _, key := range licenseKeys {
		if val, ok := metadata[key]; ok {
			collectLicenseStrings(val, add, 0)
		}
	}

	return licenses
}

// collectLicenseStrings walks the shapes registries use for license fields:
// a string, a list of strings, or the legacy maven/npm objects
// ({"type": "MIT"}, {"name": "..."}), alone or in a list.
func collectLicenseStrings(val any, add func(string), depth int) {
	if depth > 3 {
		return
	}

	switch v := val.(type) {
	case string:
		add(v)
	case []string:
		for _, str := range v {
			add(str)
		}
	case []any:
		for _, item := range v {
			collectLicenseStrings(item, add, depth+1)
		}
	case map[string]any:
		for _, k := range []string{"type", "name", "spdx_id", "spdxId", "id"} {
			if str, ok := v[k].(string); ok && str != "" {
				add(str)
				return
			}
		}
	}
}

// NormalizeLicense normalizes a license string to SPDX identifier
// e.g. "MIT License" -> "MIT", "Apache 2.0" -> "Apache-2.0".
func (d *Detector) NormalizeLicense(license string) string {
	if license == "" {
		return ""
	}

	// Trim whitespace and convert to lowercase for lookup
	normalized := strings.TrimSpace(license)

	// PyPI trove classifier: "License :: OSI Approved :: MIT License".
	if strings.HasPrefix(strings.ToLower(normalized), "license ::") {
		if i := strings.LastIndex(normalized, "::"); i >= 0 {
			normalized = strings.TrimSpace(normalized[i+2:])
		}
	}

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

	// If allowed_licenses is non-empty and no license is allowed → violation.
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

// normalizeID returns the canonical form policy lists and detected licenses are
// compared in: lowercase, a trailing "+" dropped, aliases resolved and the SPDX
// "-only" / "-or-later" qualifiers removed. "GPL-3.0", "GPL-3.0-only",
// "GPL-3.0+", "GPL-3.0-or-later" and "GPLv3" therefore all match one another, so
// a policy written with any spelling covers every spelling an artifact uses.
// The version family is the unit of policy: "GPL-3.0" in a deny list also denies
// "GPL-3.0-or-later", and in an allow list also allows it.
func normalizeID(id string) string {
	id = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(id)), "+")
	// Deprecated "GPL-2.0-with-classpath-exception" ids name the base license
	// plus an exception; like "GPL-2.0 WITH Classpath-exception-2.0" the
	// exception does not count for policy.
	if i := strings.Index(id, "-with-"); i > 0 && strings.HasSuffix(id, "-exception") {
		id = id[:i]
	}

	if spdx, ok := licenseAliases[id]; ok {
		id = strings.ToLower(spdx)
	}

	id = strings.TrimSuffix(id, "-or-later")

	return strings.TrimSuffix(id, "-only")
}

func normalizeSet(list []string) map[string]struct{} {
	m := make(map[string]struct{}, len(list))
	for _, l := range list {
		m[normalizeID(l)] = struct{}{}
	}

	return m
}

// expressionFields splits a license expression into its words: on whitespace
// of any kind (a license string is free metadata text, "MIT\nAND\nGPL-3.0" and
// "MIT\tAND\tGPL-3.0" are conjunctions too), parentheses ("(MIT)AND(GPL-3.0)"),
// commas and the separators package metadata uses in place of an operator:
// "/" ("MIT/Apache-2.0", the old Cargo form), ";" and "|". No SPDX identifier
// holds any of them.
func expressionFields(expr string) []string {
	return strings.FieldsFunc(expr, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("(),/;|", r)
	})
}

// spdxIdentifiers returns the normalized license identifiers of an SPDX
// expression, dropping parentheses, the AND/OR/WITH operators and the
// exception that follows WITH.
func spdxIdentifiers(expr string) []string {
	fields := expressionFields(expr)

	ids := make([]string, 0, len(fields)+1)
	ids = append(ids, normalizeID(expr)) // the whole string, for plain (even odd) names

	afterWith := false

	for _, f := range fields {
		switch strings.ToUpper(f) {
		case "AND", "OR":
			continue
		case "WITH":
			afterWith = true

			continue
		}

		// The word after WITH is an exception, not a license: it must not have to
		// be on the allow list ("A WITH X AND B") nor can it be denied as one.
		if afterWith {
			afterWith = false

			continue
		}

		ids = append(ids, normalizeID(f))
	}

	return ids
}

// nonSPDXJoiners are the separators package metadata uses in place of an
// operator; none says which license applies, so all are required.
const nonSPDXJoiners = "/;|,"

// expressionAllowed reports whether an expression is covered by the allow list.
// A well-formed SPDX expression is evaluated with its precedence and
// parentheses: OR needs one allowed branch, AND needs all of them ("MIT AND
// (Apache-2.0 OR GPL-3.0-only)" is allowed by [MIT, Apache-2.0]); WITH
// exceptions are ignored. Free-form text joined by "/", ";", "|" or "," needs
// every identifier allowed (conservative). Anything else that does not parse,
// such as a multi-word name, is allowed only when the whole string is listed
// or when every identifier in it is allowed.
//
// Deny matching is deliberately stricter and unchanged: see EvaluatePolicy, an
// expression is denied when any identifier in it is denied, even in an OR
// branch, because the consumer may pick that branch.
func expressionAllowed(expr string, allowed map[string]struct{}) bool {
	if _, ok := allowed[normalizeID(expr)]; ok {
		return true
	}

	if !strings.ContainsAny(expr, nonSPDXJoiners) {
		if n := parseSPDXLenient(expr); n != nil {
			return n.allowed(allowed)
		}
	}

	return flatExpressionAllowed(expr, allowed)
}

// flatExpressionAllowed is the rule for text that is not a well-formed
// expression: every identifier must be allowed, so a joiner or stray word can
// never smuggle an unlisted license past the allow list.
func flatExpressionAllowed(expr string, allowed map[string]struct{}) bool {
	ids := spdxIdentifiers(expr)[1:]
	if len(ids) == 0 {
		return false
	}

	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return false
		}
	}

	return true
}

// contains checks if a slice contains a string.
func contains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}
