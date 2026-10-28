// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/exploit"
)

// A finding that carries only a GHSA id gets the KEV and EPSS signals of its CVE
// through the mirror's alias table; without the record it keeps its severity.
func TestOSVAliasTableGivesGHSAFindingItsExploitSignals(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)

	enricher := exploit.NewEnricher(pg, 0.1).WithAliases(pg)
	finding := []models.Vulnerability{{ID: "GHSA-jfh8-c2jp-5v3q", Severity: "LOW"}}

	// Empty mirror: nothing to resolve, the existing behaviour.
	out, err := enricher.Annotate(ctx, finding)
	if err != nil {
		t.Fatal(err)
	}

	if out[0][0].KEV || out[0][0].CVE != "" {
		t.Fatalf("without a mirror record the finding has no CVE: %+v", out[0][0].Annotation)
	}

	_, err = pg.ApplyOSVAdvisories(ctx, []models.OSVAdvisory{{
		ID: "GHSA-jfh8-c2jp-5v3q", Modified: time.Now().UTC(), Summary: "log4shell",
		Aliases: []string{"CVE-2021-44228", "CVE-2021-45046"},
		Affected: []models.OSVAffected{{
			Ecosystem: "Maven", Name: "org.apache.logging.log4j:log4j-core", NameNorm: "org.apache.logging.log4j:log4j-core",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := pg.OSVCVEAliases(ctx, []string{"GHSA-jfh8-c2jp-5v3q", "GHSA-unknown"})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || !slices.Equal(got["GHSA-jfh8-c2jp-5v3q"], []string{"CVE-2021-44228", "CVE-2021-45046"}) {
		t.Fatalf("aliases = %v, want the record's CVE ids for the known id only", got)
	}

	added := time.Date(2021, 12, 10, 0, 0, 0, 0, time.UTC)
	if err := pg.ReplaceKEV(ctx, []models.KEVEntry{{CVE: "CVE-2021-44228", DateAdded: added, KnownRansomware: true}},
		models.ExploitFeedState{SourceURL: "https://kev.example/k.json"}); err != nil {
		t.Fatal(err)
	}

	out, err = enricher.Annotate(ctx, finding)
	if err != nil {
		t.Fatal(err)
	}

	if !out[0][0].KEV || out[0][0].CVE != "CVE-2021-44228" || out[0][0].Priority != exploit.PriorityKEV {
		t.Fatalf("a GHSA-only finding must get the KEV signal of its CVE via the alias table: %+v", out[0][0].Annotation)
	}

	aggs, err := enricher.Aggregates(ctx, finding)
	if err != nil {
		t.Fatal(err)
	}

	if aggs[0].KEVCount != 1 {
		t.Fatalf("aggregate kev_count = %d, want 1", aggs[0].KEVCount)
	}
}
