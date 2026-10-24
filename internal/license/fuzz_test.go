// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package license

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// FuzzIsSPDXExpression feeds arbitrary license strings (the license field of
// uploaded package metadata) to the SPDX expression validator: spacing does
// not matter, an expression stays one when parenthesized or combined with
// another with AND / OR, and the identifiers an accepted expression names are
// all on the SPDX list.
func FuzzIsSPDXExpression(f *testing.F) {
	for _, s := range []string{
		"MIT", "mit", "Apache-2.0 OR MIT", "(MIT OR Apache-2.0) AND BSD-3-Clause",
		"GPL-2.0-or-later WITH Classpath-exception-2.0", "GPL-2.0+", "MIT AND", "(MIT", "MIT)",
		"MIT\tOR\nApache-2.0", "WITH", "MIT WITH", "", "((((MIT))))", "BSD License", "(MIT)AND(ISC)",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, expr string) {
		valid := IsSPDXExpression(expr)

		tokens := tokenizeSPDX(expr)
		for _, tok := range tokens {
			if tok == "" || (len(tok) > 1 && strings.ContainsAny(tok, " \t\r\n()")) {
				t.Fatalf("token %q of %q is empty or holds whitespace or a parenthesis", tok, expr)
			}
		}

		if spaced := strings.Join(tokens, " "); IsSPDXExpression(spaced) != valid {
			t.Fatalf("%q is valid = %v but %q is not", expr, valid, spaced)
		}

		if !valid {
			return
		}

		for _, variant := range []string{"(" + expr + ")", "MIT OR " + expr, expr + " AND Apache-2.0"} {
			if !IsSPDXExpression(variant) {
				t.Fatalf("%q is an expression but %q is not", expr, variant)
			}
		}
	})
}

// operator reports whether tok is an SPDX expression operator.
func operator(tok string) bool {
	switch strings.ToUpper(tok) {
	case "AND", "OR", "WITH":
		return true
	}

	return false
}

// idTokens splits a license string into the words that name a license: it is
// the reference tokenization the policy must be at least as fine as.
func idTokens(expr string) ([]string, bool) {
	var (
		ids    []string
		hasAnd bool
	)

	for _, tok := range strings.FieldsFunc(expr, func(r rune) bool {
		return strings.ContainsRune(" \t\r\n\v\f(),", r)
	}) {
		switch {
		case strings.EqualFold(tok, "AND"):
			hasAnd = true
		case operator(tok):
		default:
			ids = append(ids, normalizeID(tok))
		}
	}

	return ids, hasAnd
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}

	return strings.Split(s, "|")
}

// FuzzEvaluatePolicy checks a license policy against arbitrary license
// strings and lists: a policy that is off or absent never complains, the
// verdict agrees with the violations, a denied identifier is found wherever
// it stands in an expression, and an expression joined by AND is allowed only
// when every license in it is.
func FuzzEvaluatePolicy(f *testing.F) {
	f.Add("MIT|GPL-3.0-only AND MIT", "MIT", "GPL-3.0", true)
	f.Add("MIT OR GPL-3.0", "MIT", "", true)
	f.Add("GPL-3.0+", "gpl-3.0-or-later", "", false)
	f.Add("", "MIT", "MIT", true)

	f.Fuzz(func(t *testing.T, licenses, allowed, denied string, enabled bool) {
		d := NewDetector(nil)
		list := splitList(licenses)

		policy := &models.LicensePolicy{Enabled: enabled, AllowedLicenses: splitList(allowed), DeniedLicenses: splitList(denied)}

		ok, violations := d.EvaluatePolicy(list, policy)
		if ok != (len(violations) == 0) {
			t.Fatalf("EvaluatePolicy verdict %v with violations %v", ok, violations)
		}

		if !enabled {
			if !ok {
				t.Fatalf("a disabled policy reported %v", violations)
			}

			return
		}

		if ok, v := d.EvaluatePolicy(list, nil); !ok || v != nil {
			t.Fatalf("no policy reported %v", v)
		}

		checkDenied(t, d, list, policy)
		checkAllowed(t, list, policy)
	})
}

func checkDenied(t *testing.T, d *Detector, list []string, policy *models.LicensePolicy) {
	t.Helper()

	denied := normalizeSet(policy.DeniedLicenses)

	only := &models.LicensePolicy{Enabled: true, DeniedLicenses: policy.DeniedLicenses}

	ok, violations := d.EvaluatePolicy(list, only)

	for _, l := range list {
		ids, _ := idTokens(l)

		for _, id := range ids {
			if _, bad := denied[id]; bad && ok {
				t.Fatalf("license %q holds the denied identifier %q but the policy %v passes it (%v)", l, id, policy.DeniedLicenses, violations)
			}
		}
	}
}

func checkAllowed(t *testing.T, list []string, policy *models.LicensePolicy) {
	t.Helper()

	allowed := normalizeSet(policy.AllowedLicenses)

	for _, l := range list {
		ids, hasAnd := idTokens(l)
		if _, whole := allowed[normalizeID(l)]; whole || !hasAnd {
			continue
		}

		for _, id := range ids {
			if _, ok := allowed[id]; !ok && expressionAllowed(l, allowed) {
				t.Fatalf("expression %q joined by AND is allowed although %q is not in the allowed list %v", l, id, policy.AllowedLicenses)
			}
		}
	}
}

// FuzzDetectFromMetadata feeds arbitrary decoded package metadata to the
// license extraction: whatever shapes the license keys have, the result is
// deterministic, free of empty and duplicate entries, trimmed, and stable
// under another normalization.
func FuzzDetectFromMetadata(f *testing.F) {
	f.Add(`{"license":"MIT"}`)
	f.Add(`{"licenses":[{"type":"Apache 2.0"},{"name":"BSD"},"License :: OSI Approved :: MIT License"]}`)
	f.Add(`{"License":["a",["b",["c",["d",["e"]]]]],"spdx_id":" GPL-3.0 ","spdxId":{"id":"x"}}`)
	f.Add(`{"license":{"type":""},"licenses":[null,5,true,{}]}`)
	f.Add(`{}`)

	f.Fuzz(func(t *testing.T, doc string) {
		var meta map[string]any
		if json.Unmarshal([]byte(doc), &meta) != nil {
			return
		}

		d := NewDetector(nil)
		got := d.DetectFromMetadata(meta)

		if again := d.DetectFromMetadata(meta); !slices.Equal(got, again) {
			t.Fatalf("DetectFromMetadata is not deterministic: %v then %v", got, again)
		}

		seen := map[string]struct{}{}

		for _, l := range got {
			if _, dup := seen[l]; dup || l == "" || l != strings.TrimSpace(l) {
				t.Fatalf("DetectFromMetadata(%s) returned %q among %q", doc, l, got)
			}

			seen[l] = struct{}{}

			if again := d.NormalizeLicense(l); again != l {
				t.Fatalf("normalizing %q again gives %q", l, again)
			}
		}

		for _, u := range d.Unresolved(got) {
			if IsSPDXExpression(u) || !slices.Contains(got, u) {
				t.Fatalf("Unresolved(%q) returned %q", got, u)
			}
		}
	})
}
