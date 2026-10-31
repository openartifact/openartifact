// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
)

// GlobMatch reports whether s matches pattern, which must match the whole
// string. "*" matches any run of characters (including none and "/", so
// "refs/heads/*" matches "refs/heads/release/1.2") and "?" matches exactly one
// character; every other character stands for itself, case-sensitively. There
// is no escape: claim values of CI systems do not contain the two wildcards.
func GlobMatch(pattern, s string) bool {
	p, v := []rune(pattern), []rune(s)

	var pi, vi int

	// star is the pattern index just after the last "*" seen, mark the value
	// index that star is currently assumed to cover up to.
	star, mark := -1, 0

	for vi < len(v) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi+1, vi
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == v[vi]):
			pi++
			vi++
		case star >= 0:
			mark++
			vi = mark
			pi = star
		default:
			return false
		}
	}

	for pi < len(p) && p[pi] == '*' {
		pi++
	}

	return pi == len(p)
}

// claimStrings renders a claim value as the strings a pattern is matched
// against: a string is itself, a boolean or number its JSON text, an array each
// of its scalar elements. Anything else (an object, null, a missing claim)
// yields none and so never matches.
func claimStrings(v any) []string {
	switch c := v.(type) {
	case string:
		return []string{c}
	case bool:
		return []string{strconv.FormatBool(c)}
	case float64:
		return []string{strconv.FormatFloat(c, 'f', -1, 64)}
	case []any:
		out := make([]string, 0, len(c))

		for _, e := range c {
			switch e.(type) {
			case string, bool, float64:
				out = append(out, claimStrings(e)...)
			}
		}

		return out
	}

	return nil
}

// ConditionsMatch reports whether claims satisfy every condition: each named
// claim must be present and one of its values must match one of the
// condition's patterns.
func ConditionsMatch(conditions []models.OIDCCondition, claims map[string]any) bool {
	if len(conditions) == 0 {
		return false
	}

	for _, c := range conditions {
		if !conditionMatches(c, claims) {
			return false
		}
	}

	return true
}

func conditionMatches(c models.OIDCCondition, claims map[string]any) bool {
	for _, value := range claimStrings(claims[c.Claim]) {
		for _, pattern := range c.Patterns {
			if GlobMatch(pattern, value) {
				return true
			}
		}
	}

	return false
}
