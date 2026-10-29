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
