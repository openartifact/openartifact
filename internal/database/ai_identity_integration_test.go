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

// A later "processing" or "failed" upsert records the new status but must not
// blank the last completed analysis: readers keep seeing its summary.
func TestAIAnalysisUpsertKeepsCompletedResult(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repoID := newQuotaRepo(t, pg, "ai-keep", 0, 0)

	done := &models.AIAnalysis{
		RepositoryID: repoID, ArtifactID: "art-1", ArtifactFormat: "npm", ArtifactName: "left-pad", ArtifactVersion: "1.3.0",
		Status: "completed", Summary: "pads strings", Tags: []string{"util"}, LicenseStatus: "SAFE", LicenseName: "MIT",
		SecurityNotes: "none", ModelUsed: "m-1", PromptTokens: 10, CompletionTokens: 20,
	}
	if err := pg.UpsertAIAnalysis(ctx, done); err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"processing", "failed"} {
		again := &models.AIAnalysis{
			RepositoryID: repoID, ArtifactID: "art-1", ArtifactFormat: "npm", ArtifactName: "left-pad", ArtifactVersion: "1.4.0",
			Status: status, LicenseStatus: "UNKNOWN",
		}
		if status == "failed" {
			again.ErrorMessage = "provider down"
		}

		if err := pg.UpsertAIAnalysis(ctx, again); err != nil {
			t.Fatal(err)
		}

		assertKeepsCompletedResult(t, pg, repoID, status, again.ErrorMessage)
	}

	// A new completed run replaces the result.
	done.Summary, done.ModelUsed, done.ArtifactVersion = "pads more", "m-2", "1.4.0"
	if err := pg.UpsertAIAnalysis(ctx, done); err != nil {
		t.Fatal(err)
	}

	got, _ := pg.GetAIAnalysis(ctx, repoID, "art-1")
	if got == nil || got.Summary != "pads more" || got.ModelUsed != "m-2" {
		t.Errorf("completed rerun = %+v", got)
	}
}

func assertKeepsCompletedResult(t *testing.T, pg *PostgresDB, repoID, status, wantErr string) {
	t.Helper()

	got, err := pg.GetAIAnalysis(context.Background(), repoID, "art-1")
	if err != nil || got == nil {
		t.Fatalf("GetAIAnalysis = %v, %v", got, err)
	}

	if got.Status != status || got.ErrorMessage != wantErr {
		t.Errorf("%s: status = %q, error = %q", status, got.Status, got.ErrorMessage)
	}

	if got.Summary != "pads strings" || got.LicenseStatus != "SAFE" || got.LicenseName != "MIT" || got.ModelUsed != "m-1" ||
		got.PromptTokens != 10 || got.CompletionTokens != 20 || len(got.Tags) != 1 {
		t.Errorf("%s wiped the completed result: %+v", status, got)
	}

	if got.ArtifactVersion != "1.4.0" {
		t.Errorf("%s: artifact_version = %q, want the latest identity", status, got.ArtifactVersion)
	}
}

// Migration 024 rewrites the Go ids stored by older releases.
func TestMigration024GoFormatID(t *testing.T) {
	db := scratchDB(t)

	migrateThrough(t, db, "023")

	const systemOrg = "00000000-0000-0000-0000-000000000001"

	repo := mustScan[string](t, db, `INSERT INTO repositories (name, format, org_id) VALUES ('gomods', 'go', $1) RETURNING id`, systemOrg)
	oldRef := mustScan[string](
		t,
		db,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, 'a', 'gomod') RETURNING id`,
		repo,
	)
	npmRef := mustScan[string](
		t,
		db,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, 'b', 'npm') RETURNING id`,
		repo,
	)

	oldJob := mustScan[string](
		t,
		db,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner) VALUES ($1, 'vuln', 'osv') RETURNING id`,
		oldRef,
	)
	npmJob := mustScan[string](
		t,
		db,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner) VALUES ($1, 'vuln', 'osv') RETURNING id`,
		npmRef,
	)

	mustExec(t, db, `INSERT INTO vuln_results (scan_job_id, ecosystem) VALUES ($1, 'gomod')`, oldJob)
	mustExec(t, db, `INSERT INTO vuln_results (scan_job_id, ecosystem) VALUES ($1, 'npm')`, npmJob)

	migrateAll(t, db)

	if got := mustScan[string](t, db, `SELECT format FROM artifact_refs WHERE id = $1`, oldRef); got != "go" {
		t.Errorf("artifact_refs.format = %q, want go", got)
	}

	if got := mustScan[string](t, db, `SELECT format FROM artifact_refs WHERE id = $1`, npmRef); got != "npm" {
		t.Errorf("other formats must not change, got %q", got)
	}

	if got := mustScan[string](t, db, `SELECT ecosystem FROM vuln_results WHERE scan_job_id = $1`, oldJob); got != "go" {
		t.Errorf("vuln_results.ecosystem = %q, want go", got)
	}

	if got := mustScan[string](t, db, `SELECT ecosystem FROM vuln_results WHERE scan_job_id = $1`, npmJob); got != "npm" {
		t.Errorf("other ecosystems must not change, got %q", got)
	}
}
