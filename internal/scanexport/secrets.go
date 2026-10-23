// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package scanexport

import (
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// SecretItem is a secret scan with the name of its repository.
type SecretItem struct {
	Repo string
	Scan *models.SecretScanResult
}

const secretsTool = "openartifact-secrets"

func secretRule(f models.SecretFinding, id string) Rule {
	desc := f.Description
	if desc == "" {
		desc = f.Type
	}

	_, score := levelAndScore(f.Severity)

	return Rule{
		ID:               id,
		Name:             id,
		ShortDescription: &Text{Text: truncate(clean(desc), 255)},
		FullDescription:  &Text{Text: truncate(clean(desc), maxMessageLen)},
		Help: &Help{
			Text: "A possible secret was detected in the artifact. Revoke it, rotate it and republish without it.",
		},
		Properties: map[string]any{
			"security-severity": scoreString(score),
			"tags":              []string{"security", "secret"},
		},
	}
}

// SecretsSARIF builds a SARIF log of the scans' findings: one run, one rule per
// secret type, one result per finding, located by the artifact path
// "<repository>/<artifact id>". A finding's value, redacted or not, and its
// surrounding text are never exported. truncated is true when more than
// MaxResults findings existed.
func SecretsSARIF(items []SecretItem) (*Log, bool) {
	var findings []finding

	for _, it := range items {
		s := it.Scan
		uri := artifactURI(it.Repo, s.ArtifactID)
		seen := map[string]int{}

		for _, f := range s.Findings {
			id := ruleID(f.Type)
			level, _ := levelAndScore(f.Severity)
			seen[id]++

			msg := "Possible secret (" + clean(f.Description) + ") in " + clean(s.ArtifactID)
			if f.Description == "" {
				msg = "Possible secret (" + clean(f.Type) + ") in " + clean(s.ArtifactID)
			}

			findings = append(findings, finding{
				tool:    secretsTool,
				rank:    severityRank(f.Severity),
				rule:    secretRule(f, id),
				success: true,
				result: Result{
					RuleID:  id,
					Level:   level,
					Message: Text{Text: truncate(msg, maxMessageLen)},
					Locations: []Location{{PhysicalLocation: PhysicalLocation{
						ArtifactLocation: ArtifactLocation{URI: uri},
						Region:           Region{StartLine: 1},
					}}},
					PartialFingerprints: map[string]string{
						// The occurrence number tells apart two findings of one type
						// in one artifact without using any part of the secret.
						fingerprintKey: fingerprint(it.Repo, s.ArtifactID, f.Type, itoa(seen[id])),
					},
					Properties: map[string]any{
						"repository": it.Repo,
						"severity":   strings.ToLower(f.Severity),
						"scanId":     s.ID,
					},
				},
			})
		}
	}

	runs, truncated := buildRuns(findings, nil, secretsTool)

	return &Log{Schema: sarifSchema, Version: sarifVersion, Runs: runs}, truncated
}

// SecretsCSVHeader is the first row of SecretsCSVRows.
//
//nolint:gochecknoglobals // immutable column list
var SecretsCSVHeader = []string{
	"repository", "artifact_id", "scan_id", "scan_status", "type", "description", "severity",
}

// SecretsCSVRows returns one row per finding (header excluded); no secret
// value is included.
func SecretsCSVRows(items []SecretItem) [][]string {
	var rows [][]string

	for _, it := range items {
		s := it.Scan
		for _, f := range s.Findings {
			rows = append(rows, []string{
				it.Repo, s.ArtifactID, s.ID, s.ScanStatus, f.Type, f.Description, strings.ToLower(f.Severity),
			})
		}
	}

	return rows
}
