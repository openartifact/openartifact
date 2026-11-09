// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestNormalizePURL(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"pkg:npm/%40babel/core@7.0.0", "pkg:npm/%40babel/core@7.0.0", true},
		{"pkg:npm/@babel/core@7.0.0", "pkg:npm/%40babel/core@7.0.0", true},
		{"PKG:NPM/%40babel/core@7.0.0?repository_url=https://r.example#lib/x", "pkg:npm/%40babel/core@7.0.0", true},
		{"pkg:maven/org.x/lib@1.0%2Bb?type=jar", "pkg:maven/org.x/lib@1.0%2Bb", true},
		{"pkg:maven/org.x/lib@1.0+b", "pkg:maven/org.x/lib@1.0%2Bb", true},
		{"pkg:pypi/My_Pkg@1.0", "pkg:pypi/my-pkg@1.0", true},
		{"pkg:deb/debian/curl@7.88.1-10+deb12u5?arch=amd64&distro=bookworm", "pkg:deb/debian/curl@7.88.1-10%2Bdeb12u5", true},
		{"pkg:golang/github.com/a/b@v1.2.3", "pkg:golang/github.com/a/b@v1.2.3", true},
		{"pkg://npm/lib@1.0.0/", "pkg:npm/lib@1.0.0", true},
		{"pkg:npm/lib", "", false},
		{"pkg:npm/@babel/core", "", false},
		{"pkg:npm/lib@", "", false},
		{"pkg:npm/lib@%zz", "", false},
		{"pkg:npm", "", false},
		{"pkg:1npm/lib@1.0", "", false},
		{"cpe:2.3:a:acme:lib:1.0", "", false},
		{"", "", false},
	}

	for _, c := range cases {
		got, ok := NormalizePURL(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizePURL(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestArtifactPURLNormalized pins that an artifact's purl is already in normal
// form: imported statements are matched on NormalizePURL(ArtifactPURL(...)).
func TestArtifactPURLNormalized(t *testing.T) {
	for _, c := range [][3]string{
		{"npm", "@babel/core@7.0.0", "pkg:npm/%40babel/core@7.0.0"},
		{"maven", "org.x:lib@1.0+b", "pkg:maven/org.x/lib@1.0%2Bb"},
		{"pypi", "My_Pkg@1.0", "pkg:pypi/my-pkg@1.0"},
		{"go", "github.com/a/b@v1.2.3", "pkg:golang/github.com/a/b@v1.2.3"},
	} {
		p := ArtifactPURL(c[0], c[1])
		if p != c[2] {
			t.Errorf("ArtifactPURL(%s, %s) = %q, want %q", c[0], c[1], p, c[2])
		}

		if n, ok := NormalizePURL(p); !ok || n != p {
			t.Errorf("NormalizePURL(%q) = %q, %v", p, n, ok)
		}
	}
}

func TestPeerOpenVEXRoundTrip(t *testing.T) {
	updated := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	stmts := []*models.VEXStatement{
		{
			ArtifactID: "@acme/lib@1.2.3", VulnID: "CVE-2024-1", Status: models.VEXNotAffected,
			Justification: models.VEXVulnerableCodeNotInExec, ImpactStatement: "never called", UpdatedAt: updated,
		},
		{
			ArtifactID:      "@acme/lib@1.2.3",
			VulnID:          "GHSA-aaaa-bbbb-cccc",
			Status:          models.VEXAffected,
			ActionStatement: "upgrade",
			UpdatedAt:       updated,
		},
		{ArtifactID: "other@2.0.0", VulnID: "OSV-1", Status: models.VEXFixed, UpdatedAt: updated},
		{ArtifactID: "other@2.0.0", VulnID: "OSV-2", Status: "bogus"},
	}

	res, err := NewGenerator(nil, nil).PeerOpenVEX("npm", stmts)
	if err != nil {
		t.Fatal(err)
	}

	if res.Components != 3 {
		t.Errorf("statements = %d, want 3 (the invalid status left out)", res.Components)
	}

	got, err := ParseOpenVEX(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	want := []OpenVEXStatement{
		{
			VulnID: "CVE-2024-1", PURLs: []string{"pkg:npm/%40acme/lib@1.2.3"}, Status: models.VEXNotAffected,
			Justification: models.VEXVulnerableCodeNotInExec, ImpactStatement: "never called",
		},
		{
			VulnID:          "GHSA-aaaa-bbbb-cccc",
			PURLs:           []string{"pkg:npm/%40acme/lib@1.2.3"},
			Status:          models.VEXAffected,
			ActionStatement: "upgrade",
		},
		{VulnID: "OSV-1", PURLs: []string{"pkg:npm/other@2.0.0"}, Status: models.VEXFixed},
	}

	if len(got) != len(want) {
		t.Fatalf("parsed %+v", got)
	}

	for i := range want {
		g := got[i]
		if g.Timestamp == nil || !g.Timestamp.Equal(updated) {
			t.Errorf("statement %d timestamp = %v", i, g.Timestamp)
		}

		g.Timestamp = nil
		if !equalStatements(g, want[i]) {
			t.Errorf("statement %d = %+v\nwant %+v", i, g, want[i])
		}
	}
}

func equalStatements(a, b OpenVEXStatement) bool {
	return a.VulnID == b.VulnID && strings.Join(a.PURLs, ",") == strings.Join(b.PURLs, ",") && a.Status == b.Status &&
		a.Justification == b.Justification && a.ImpactStatement == b.ImpactStatement && a.ActionStatement == b.ActionStatement
}

func TestParseOpenVEXSkipsInvalidStatements(t *testing.T) {
	body := `{"@context":"https://openvex.dev/ns/v0.2.0","timestamp":"2026-01-01T00:00:00Z","statements":[
		{"vulnerability":{"@id":"CVE-1"},"products":[{"@id":"pkg:npm/a@1"}],"status":"fixed"},
		{"vulnerability":{"name":"CVE-2"},"products":[{"@id":"x","identifiers":{"purl":"pkg:npm/b@1"}}],"status":"not_affected"},
		{"vulnerability":{"name":"CVE-3"},"products":[{"@id":"pkg:npm/c@1"}],"status":"not_affected","justification":"because"},
		{"vulnerability":{"name":"CVE-4"},"products":[{"@id":"pkg:npm/d@1"}],"status":"wontfix"},
		{"vulnerability":{"name":"CVE-5"},"products":[{"@id":"not-a-purl"}],"status":"fixed"},
		{"vulnerability":{},"products":[{"@id":"pkg:npm/e@1"}],"status":"fixed"}
	]}`

	got, err := ParseOpenVEX([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	// Only CVE-1 survives: its id from @id, its timestamp from the document.
	if len(got) != 1 || got[0].VulnID != "CVE-1" || got[0].PURLs[0] != "pkg:npm/a@1" || got[0].Timestamp == nil {
		t.Errorf("parsed %+v", got)
	}

	for _, bad := range []string{`{"@context":"https://example.com","statements":[]}`, `[`} {
		if _, err := ParseOpenVEX([]byte(bad)); err == nil {
			t.Errorf("ParseOpenVEX(%s) accepted", bad)
		}
	}
}

// TestVEXNamesStatementSource pins that the OpenVEX status notes and the
// CycloneDX analysis detail tell where an imported statement came from.
func TestVEXNamesStatementSource(t *testing.T) {
	cases := []struct {
		kind, name, ref, note string
		inDetail              bool
	}{
		{"", "", "", "Recorded by an OpenArtifact administrator.", false},
		{models.VEXSourceLocal, "", "", "Recorded by an OpenArtifact administrator.", false},
		{
			models.VEXSourceCSAF,
			"acme",
			"https://acme.example/vex.json",
			"From the CSAF VEX feed acme (https://acme.example/vex.json).",
			true,
		},
		{models.VEXSourcePeer, "eu-mirror", "https://eu.example", "Decision of the federated peer eu-mirror.", true},
	}

	for _, c := range cases {
		in := &Input{
			Format: "npm", ArtifactID: "lib@1.0.0", Name: "lib", Version: "1.0.0", PURL: "pkg:npm/lib@1.0.0",
			Scan: scanned(testVuln),
			Decisions: map[string]*models.VEXStatement{"CVE-2024-0001": {
				VulnID: "CVE-2024-0001", Status: models.VEXNotAffected, Justification: models.VEXComponentNotPresent,
				ImpactStatement: "not shipped", SourceKind: c.kind, SourceName: c.name, SourceRef: c.ref,
			}},
		}

		g := NewGenerator(nil, nil)

		res, err := g.GenerateOpenVEX([]*Input{in})
		if err != nil {
			t.Fatal(err)
		}

		var doc openVEXDoc
		if err := json.Unmarshal(res.Body, &doc); err != nil {
			t.Fatal(err)
		}

		if notes := doc.Statements[0].StatusNotes; notes != c.note {
			t.Errorf("%q: status notes = %q, want %q", c.kind, notes, c.note)
		}

		cdx, err := g.GenerateCycloneDXVEX([]*Input{in}, "")
		if err != nil {
			t.Fatal(err)
		}

		var bom cdxBOM
		if err := json.Unmarshal(cdx.Body, &bom); err != nil {
			t.Fatal(err)
		}

		want := "not shipped"
		if c.inDetail {
			want += " " + c.note
		}

		if d := bom.Vulnerabilities[0].Analysis.Detail; d != want {
			t.Errorf("%q: analysis detail = %q, want %q", c.kind, d, want)
		}
	}
}
