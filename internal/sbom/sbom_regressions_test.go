// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Names that sanitize to the same LicenseRef id must not share one extracted
// license: the later one would be reported with the earlier one's text.
func TestSPDXLicenseExprIDCollisions(t *testing.T) {
	t.Parallel()

	var extracted []spdxExtracted

	a := spdxLicenseExpr([]licenseEntry{{Name: "Acme EULA"}}, &extracted)
	b := spdxLicenseExpr([]licenseEntry{{Name: "Acme/EULA"}}, &extracted)
	again := spdxLicenseExpr([]licenseEntry{{Name: "Acme EULA"}}, &extracted)
	cyr1 := spdxLicenseExpr([]licenseEntry{{Name: "Лицензия А"}}, &extracted)
	cyr2 := spdxLicenseExpr([]licenseEntry{{Name: "Лицензия Б"}}, &extracted)

	if a != "LicenseRef-Acme-EULA" || again != a {
		t.Errorf("first name = %q, repeated = %q", a, again)
	}

	if b == a || !strings.HasPrefix(b, "LicenseRef-Acme-EULA-") {
		t.Errorf("colliding name got %q, want a distinct hashed id", b)
	}

	if cyr1 == cyr2 {
		t.Errorf("distinct non-ASCII names share %q", cyr1)
	}

	ids := map[string]string{}

	for _, x := range extracted {
		if prev, dup := ids[x.LicenseID]; dup {
			t.Errorf("duplicate extracted id %q (%q, %q)", x.LicenseID, prev, x.Name)
		}

		ids[x.LicenseID] = x.Name
	}

	if len(extracted) != 4 {
		t.Errorf("extracted = %+v, want 4 entries", extracted)
	}
}

func TestSplitArtifactIDTrailingAt(t *testing.T) {
	t.Parallel()

	for id, want := range map[string][2]string{
		"foo@":             {"foo", "unknown"},
		"foo":              {"foo", "unknown"},
		"@scope/pkg":       {"@scope/pkg", "unknown"},
		"@scope/pkg@1.0.0": {"@scope/pkg", "1.0.0"},
		"@scope/pkg@":      {"@scope/pkg", "unknown"},
	} {
		if n, v := splitArtifactID(id); n != want[0] || v != want[1] {
			t.Errorf("splitArtifactID(%q) = %q, %q, want %q, %q", id, n, v, want[0], want[1])
		}
	}
}

// A not_affected assessment whose justification has no SPDX 3 equivalent must
// omit security_justificationType rather than emit an empty enum value.
func TestSPDX3NotAffectedWithoutJustification(t *testing.T) {
	t.Parallel()

	assess := map[string]any{}
	spdx3VexDetails(assess, Finding{Status: models.VEXNotAffected, Justification: "bogus"}, time.Time{})

	if v, ok := assess["security_justificationType"]; ok {
		t.Errorf("security_justificationType = %q, want it omitted", v)
	}
}

// A dependency that names the subject itself would repeat the subject's
// CycloneDX bom-ref.
func TestNewInputDropsSelfDependency(t *testing.T) {
	t.Parallel()

	in := newInput("npm", "lib@1.0.0", map[string]any{"dependencies": []any{
		map[string]any{"name": "lib", "version": "1.0.0"},
		map[string]any{"name": "other", "version": "2.0.0"},
	}})

	if len(in.Deps) != 1 || in.Deps[0].Name != "other" {
		t.Errorf("deps = %+v, want only other", in.Deps)
	}
}
