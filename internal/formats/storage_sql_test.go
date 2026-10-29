// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/format/storage"
)

var innermostParens = regexp.MustCompile(`\([^()]*\)`)

// TestStorageExpressionsParenthesiseJSONExtraction: in Postgres ->> and || share
// one precedence level and associate left, so `a->>'x' || '/' || b->>'y'` reads
// as `((a->>'x') || '/' || b) ->> 'y'` and fails at run time with
// "operator does not exist: text ->> unknown". Every ->/->> next to a || in a
// descriptor expression has to sit inside parentheses.
func TestStorageExpressionsParenthesiseJSONExtraction(t *testing.T) {
	t.Parallel()

	for _, name := range storage.Names() {
		s, _ := storage.Lookup(name)

		exprs := map[string]string{
			"Trash.NameExpr":      s.Trash.NameExpr,
			"Trash.VersionExpr":   s.Trash.VersionExpr,
			"Trash.RegenKeys":     s.Trash.RegenKeys,
			"Cleanup.VersionExpr": s.Cleanup.VersionExpr,
			"Cleanup.ProtectExpr": s.Cleanup.ProtectExpr,
		}

		for field, expr := range exprs {
			top := expr
			for innermostParens.MatchString(top) {
				top = innermostParens.ReplaceAllString(top, "_")
			}

			for part := range strings.SplitSeq(top, ",") {
				if strings.Contains(part, "->") && strings.Contains(part, "||") {
					t.Errorf("%s %s = %q: wrap each ->/->> extraction in parentheses", name, field, expr)

					break
				}
			}
		}
	}
}
