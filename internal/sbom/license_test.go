// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strings"
	"testing"
)

// TestCanonicalSPDXExpressionNesting: the license of an npm package.json can be
// 4 MiB of "(" (the size cap of the file); the parser must refuse it instead of
// recursing past the goroutine stack limit, which is fatal to the whole process.
func TestCanonicalSPDXExpressionNesting(t *testing.T) {
	t.Parallel()

	if got, ok := canonicalSPDXExpression(strings.Repeat("(", 4<<20)); ok || got != "" {
		t.Errorf("4 MiB of parentheses = %q, %v, want it rejected", got, ok)
	}

	ok := strings.Repeat("(", maxExprDepth) + "MIT" + strings.Repeat(")", maxExprDepth)
	if got, valid := canonicalSPDXExpression(ok); !valid || got != ok {
		t.Errorf("%d nested parentheses = %q, %v, want it accepted", maxExprDepth, got, valid)
	}

	tooDeep := "(" + ok + ")"
	if got, valid := canonicalSPDXExpression(tooDeep); valid || got != "" {
		t.Errorf("%d nested parentheses = %q, %v, want it rejected", maxExprDepth+1, got, valid)
	}
}
