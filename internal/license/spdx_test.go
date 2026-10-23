// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package license_test

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/license"
)

func TestIsSPDXExpression(t *testing.T) {
	t.Parallel()

	valid := []string{
		"MIT", "mit", "Apache-2.0", "GPL-2.0+", "GPL-3.0-or-later",
		"MIT OR Apache-2.0", "MIT AND (BSD-3-Clause OR ISC)",
		"GPL-2.0-only WITH Classpath-exception-2.0", "(MIT)",
	}

	invalid := []string{
		"", "   ", "BSD", "BSD License", "Proprietary", "UNKNOWN", "SEE LICENSE IN LICENSE.md",
		"MIT OR", "OR MIT", "MIT BSD-3-Clause", "(MIT", "MIT)", "MIT WITH", "MIT WITH Not-An-Exception",
		"LicenseRef-custom", "https://example.com/license",
	}

	for _, expr := range valid {
		if !license.IsSPDXExpression(expr) {
			t.Errorf("IsSPDXExpression(%q) = false, want true", expr)
		}
	}

	for _, expr := range invalid {
		if license.IsSPDXExpression(expr) {
			t.Errorf("IsSPDXExpression(%q) = true, want false", expr)
		}
	}
}

func TestUnresolved(t *testing.T) {
	t.Parallel()

	d := license.NewDetector(nil)

	got := d.Unresolved([]string{
		"BSD", "License :: OSI Approved :: BSD License", "License :: OSI Approved :: MIT License",
		"MIT OR Apache-2.0", "BSD", "", "License :: OSI Approved :: ISC License (ISCL)",
	})

	if !slices.Equal(got, []string{"BSD", "BSD License"}) {
		t.Errorf("Unresolved = %v, want raw names flagged once and mapped classifiers resolved", got)
	}

	if got := d.Unresolved(nil); len(got) != 0 {
		t.Errorf("Unresolved(nil) = %v, want none", got)
	}
}
