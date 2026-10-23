// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestValidateVEXStatement(t *testing.T) {
	base := models.VEXStatement{ArtifactID: "lib@1.0.0", VulnID: "CVE-2024-0001", Status: "affected"}

	cases := []struct {
		name   string
		mutate func(s *models.VEXStatement)
		valid  bool
	}{
		{"affected", func(*models.VEXStatement) {}, true},
		{"not_affected needs a justification", func(s *models.VEXStatement) { s.Status = "not_affected" }, false},
		{"not_affected with justification", func(s *models.VEXStatement) {
			s.Status, s.Justification = "not_affected", "component_not_present"
		}, true},
		{"unknown justification", func(s *models.VEXStatement) {
			s.Status, s.Justification = "not_affected", "because"
		}, false},
		{"justification on affected", func(s *models.VEXStatement) { s.Justification = "component_not_present" }, false},
		{"unknown status", func(s *models.VEXStatement) { s.Status = "maybe" }, false},
		{"empty vuln id", func(s *models.VEXStatement) { s.VulnID = "" }, false},
		{"vuln id with whitespace", func(s *models.VEXStatement) { s.VulnID = "CVE 1" }, false},
		{"control chars in statement", func(s *models.VEXStatement) { s.ImpactStatement = "a\x00b" }, false},
		{"under_investigation", func(s *models.VEXStatement) { s.Status = "under_investigation" }, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := base
			c.mutate(&s)

			if msg := validateVEXStatement(&s); (msg == "") != c.valid {
				t.Fatalf("validateVEXStatement = %q, valid want %v", msg, c.valid)
			}
		})
	}
}

func TestValidateSBOMRequestFormats(t *testing.T) {
	cases := []struct {
		format, spec string
		valid        bool
	}{
		{"cyclonedx", "1.4", true},
		{"cyclonedx", "1.5", true},
		{"cyclonedx", "1.6", true},
		{"cyclonedx", "", true},
		{"cyclonedx", "1.3", false},
		{"spdx", "2.3", true},
		{"spdx-json", "2.3", true},
		{"spdx-json", "", true},
		{"spdx", "2.2", false},
		{"xml", "1", false},
		{"", "1.5", false},
	}

	for _, c := range cases {
		if msg := validateSBOMRequest("lib@1", c.format, c.spec); (msg == "") != c.valid {
			t.Errorf("validateSBOMRequest(%q,%q) = %q, valid want %v", c.format, c.spec, msg, c.valid)
		}
	}
}
