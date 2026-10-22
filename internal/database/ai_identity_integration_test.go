// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Migration 023 adds the identity columns with a ” default so rows written
// by older releases stay valid.
func TestMigration023AIArtifactIdentityColumns(t *testing.T) {
	db := scratchDB(t)

	migrateThrough(t, db, "022")
	migrateAll(t, db)

	for _, col := range []string{"artifact_name", "artifact_version"} {
		def := mustScan[string](t, db,
			`SELECT column_default FROM information_schema.columns WHERE table_name = 'ai_results' AND column_name = $1`, col)
		if def != "''::text" {
			t.Errorf("%s default = %q, want ''::text", col, def)
		}

		nullable := mustScan[string](t, db,
			`SELECT is_nullable FROM information_schema.columns WHERE table_name = 'ai_results' AND column_name = $1`, col)
		if nullable != "NO" {
			t.Errorf("%s is_nullable = %q, want NO", col, nullable)
		}
	}

	// Re-applying is a no-op.
	migrateAll(t, db)
}

// Name and version written with an analysis come back from get and list, and
// a later status change keeps whatever the writer supplies.
func TestAIAnalysisPersistsArtifactIdentity(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repoID := newQuotaRepo(t, pg, "ai-identity", 0, 0)

	a := &models.AIAnalysis{
		RepositoryID: repoID, ArtifactID: "art-1", ArtifactFormat: "npm",
		ArtifactName: "left-pad", ArtifactVersion: "1.3.0", Status: "processing", LicenseStatus: "UNKNOWN",
	}

	if err := pg.UpsertAIAnalysis(ctx, a); err != nil {
		t.Fatalf("UpsertAIAnalysis(processing): %v", err)
	}

	a.Status, a.Summary, a.ModelUsed, a.LicenseStatus = "completed", "pads strings", "gpt-test-2026", "SAFE"

	if err := pg.UpsertAIAnalysis(ctx, a); err != nil {
		t.Fatalf("UpsertAIAnalysis(completed): %v", err)
	}

	got, err := pg.GetAIAnalysis(ctx, repoID, "art-1")
	if err != nil || got == nil {
		t.Fatalf("GetAIAnalysis = %v, %v", got, err)
	}

	if got.ArtifactName != "left-pad" || got.ArtifactVersion != "1.3.0" || got.ModelUsed != "gpt-test-2026" || got.Status != "completed" {
		t.Errorf("get = %+v", got)
	}

	list, total, err := pg.ListAIAnalyses(ctx, repoID, "", false, 10, 0)
	if err != nil || total != 1 || len(list) != 1 || list[0].ArtifactName != "left-pad" || list[0].ArtifactVersion != "1.3.0" {
		t.Errorf("list = %+v total=%d err=%v", list, total, err)
	}
}
