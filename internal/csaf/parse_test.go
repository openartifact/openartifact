// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package csaf

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func readFixture(t *testing.T) []byte {
	t.Helper()

	body, err := os.ReadFile("testdata/vex.json")
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestParseDocumentStatements(t *testing.T) {
	doc, err := ParseDocument(readFixture(t), "https://acme.example/vex.json")
	if err != nil {
		t.Fatal(err)
	}

	released := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	if doc.TrackingID != "ACME-VEX-2024-0001" || !doc.Released.Equal(released) || doc.URL != "https://acme.example/vex.json" {
		t.Errorf("document = %+v", doc)
	}

	const cve = "CVE-2024-1111"

	want := []models.VEXFeedStatement{
		// The relationship product inherits the purl of its product_reference:
		// the same statement, kept once. Qualifiers, subpath and type case dropped.
		{VulnID: cve, PURL: "pkg:npm/%40acme/lib@1.2.3", Status: models.VEXNotAffected, Justification: models.VEXVulnerableCodeNotInExec},
		{
			VulnID:          cve,
			PURL:            "pkg:pypi/my-tool@3.1",
			Status:          models.VEXNotAffected,
			ImpactStatement: "The vulnerable parser is never called.",
		},
		// A versionless purl is dropped and the CPE identifies the product.
		{
			VulnID: cve, CPE: "cpe:2.3:a:acme:server:5.0:*:*:*:*:*:*:*", Status: models.VEXNotAffected,
			ImpactStatement: "The vulnerable parser is never called.",
		},
		{VulnID: cve, PURL: "pkg:npm/%40acme/lib@2.0.0", Status: models.VEXFixed, ActionStatement: "Upgrade to 2.0.0."},
		{VulnID: cve, PURL: "pkg:golang/example.com/acme/agent@v1.0.0", Status: models.VEXFixed},
		// No cve: the first ids[].text.
		{VulnID: "ACME-2024-7", PURL: "pkg:golang/example.com/acme/agent@v1.0.0", Status: models.VEXNotAffected,
			Justification: models.VEXComponentNotPresent},
	}

	if len(doc.Statements) != len(want) {
		t.Fatalf("got %d statements, want %d: %+v", len(doc.Statements), len(want), doc.Statements)
	}

	for i, got := range doc.Statements {
		if got.DocumentURL != doc.URL || got.TrackingID != doc.TrackingID || got.ReleasedAt == nil || !got.ReleasedAt.Equal(released) {
			t.Errorf("statement %d provenance = %+v", i, got)
		}

		got.DocumentURL, got.TrackingID, got.ReleasedAt = "", "", nil
		if got != want[i] {
			t.Errorf("statement %d = %+v\nwant %+v", i, got, want[i])
		}
	}
}

func TestParseDocumentRejects(t *testing.T) {
	valid := string(readFixture(t))

	cases := map[string]struct {
		body, reason string
	}{
		"not json":      {"{", "not valid JSON"},
		"informational": {strings.Replace(valid, `"csaf_vex"`, `"csaf_informational_advisory"`, 1), "unsupported document category"},
		"no release date": {
			strings.Replace(valid, `"current_release_date": "2024-05-01T10:00:00Z"`, `"current_release_date": "May 1"`, 1),
			"current_release_date",
		},
		"security advisory": {strings.Replace(valid, `"csaf_vex"`, `"csaf_security_advisory"`, 1), ""},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDocument([]byte(c.body), "https://acme.example/vex.json")

			switch {
			case c.reason == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case c.reason != "" && (err == nil || !strings.Contains(err.Error(), c.reason)):
				t.Fatalf("error = %v, want it to contain %q", err, c.reason)
			}
		})
	}
}
