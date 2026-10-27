// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
)

// These tests cover DELETE /api/v1/repositories/{name}/trash ("empty trash")
// and the quota error's trash hint over the real handlers and Postgres.

const temRepoTrash = "/api/v1/repositories/%s/trash"

func TestTrashEmpty_PurgesItemsFreesUnsharedBlobsAndQuota(t *testing.T) {
	env := tpoNewEnv(t)

	// The harness has no audit logger; wire the real one for this test.
	logger := audit.NewLogger(harness.Database, harness.Log, nil, 100)
	audit.SetDefault(logger)
	t.Cleanup(func() {
		audit.SetDefault(nil)
		_ = logger.Shutdown(context.Background())
	})

	const repoA, repoB = "tem-empty-a", "tem-empty-b"

	tpoCreateRepo(t, env, repoA, "maven", nil)
	tpoCreateRepo(t, env, repoB, "maven", nil)

	one, two, shared := tpoContent("tem-one"), tpoContent("tem-two"), tpoContent("tem-shared")
	onePath, twoPath, sharedPath := tpoMavenPath("one", "1.0.0"), tpoMavenPath("two", "1.0.0"), tpoMavenPath("shared", "1.0.0")

	tpoPut(t, env, tpoMavenURL(repoA, onePath), one, http.StatusCreated)
	tpoPut(t, env, tpoMavenURL(repoA, twoPath), two, http.StatusCreated)
	tpoPut(t, env, tpoMavenURL(repoA, sharedPath), shared, http.StatusCreated)
	// The same bytes live in another repository, which must be left alone.
	tpoPut(t, env, tpoMavenURL(repoB, sharedPath), shared, http.StatusCreated)

	for _, path := range []string{onePath, twoPath, sharedPath} {
		tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repoA, path), nil), http.StatusNoContent, "delete "+path)
	}

	if got := tpoRepoTrash(t, env, repoA).Total; got != 3 {
		t.Fatalf("trash of %s holds %d items, want 3", repoA, got)
	}

	tpoAssertQuota(t, repoA, len(one)+len(two)+len(shared), 3)

	target := strings.Replace(temRepoTrash, "%s", repoA, 1)

	// Deleting is not enough: emptying needs `manage`.
	tpoExpectError(
		t,
		tpoSend(t, env, tpoWriter, http.MethodDelete, target, nil),
		http.StatusForbidden,
		"forbidden",
		"empty trash as writer",
	)
	tpoExpectError(t, tpoSend(t, env, "", http.MethodDelete, target, nil), http.StatusForbidden, "forbidden", "empty trash as anonymous")

	if got := tpoRepoTrash(t, env, repoA).Total; got != 3 {
		t.Fatalf("a refused request left %d trashed items, want 3", got)
	}

	tpoExpectError(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, strings.Replace(temRepoTrash, "%s", "tem-nope", 1), nil),
		http.StatusNotFound, "not_found", "empty trash of an unknown repository")

	rec := tpoSend(t, env, tpoAdmin, http.MethodDelete, target, nil)
	tpoExpect(t, rec, http.StatusOK, "empty trash as admin")

	if res := tpoDecode[responses.TrashEmptyResponse](t, rec); res.Purged != 3 || res.Remaining != 0 {
		t.Errorf("response = %+v, want purged 3, remaining 0", res)
	}

	if rows := tpoCount(t, `SELECT COUNT(*) FROM maven_artifacts WHERE repository_id = $1`, tpoRepoID(t, repoA)); rows != 0 {
		t.Errorf("%d maven rows of %s survived", rows, repoA)
	}

	if got := tpoRepoTrash(t, env, repoA).Total; got != 0 {
		t.Errorf("trash still lists %d items", got)
	}

	tpoAssertBlobFreed(t, "first unshared blob", one)
	tpoAssertBlobFreed(t, "second unshared blob", two)
	tpoAssertBlobKept(t, "blob a live row of another repository still uses", shared)
	tpoAssertServes(t, env, tpoMavenURL(repoB, sharedPath), shared)
	tpoAssertQuota(t, repoA, 0, 0)

	// The repository itself stays.
	if !tpoRepoListed(t, env, repoA) {
		t.Errorf("repository %s vanished with its trash", repoA)
	}

	// Drain the asynchronous audit writer before reading.
	if err := logger.Shutdown(t.Context()); err != nil {
		t.Fatalf("audit shutdown: %v", err)
	}

	purged := 0

	for _, row := range tpoAuditTrail(t, repoA) {
		if row.action == "artifact.purged" && row.actor == tpoAdminName && strings.HasPrefix(row.message, "purged (manual): ") {
			purged++
		}
	}

	if purged != 3 {
		t.Errorf("%d artifact.purged audit events, want 3", purged)
	}

	// Nothing left: still a success.
	audit.SetDefault(nil)

	rec = tpoSend(t, env, tpoAdmin, http.MethodDelete, target, nil)
	tpoExpect(t, rec, http.StatusOK, "empty an empty trash")

	if res := tpoDecode[responses.TrashEmptyResponse](t, rec); res.Purged != 0 || res.Remaining != 0 {
		t.Errorf("empty trash response = %+v, want zeroes", res)
	}
}

func TestTrashEmpty_QuotaErrorHintsAtTrash(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tem-quota"

	tpoCreateRepo(t, env, repo, "maven", nil)
	tpoExec(t, `INSERT INTO storage_quotas (repository_id, max_artifact_count)
		VALUES ((SELECT id FROM repositories WHERE name = $1), 1)
		ON CONFLICT (repository_id) DO UPDATE SET max_artifact_count = 1`, repo)

	first, second := tpoContent("tem-first"), tpoContent("tem-second")
	firstPath, secondPath := tpoMavenPath("first", "1.0.0"), tpoMavenPath("second", "1.0.0")

	tpoPut(t, env, tpoMavenURL(repo, firstPath), first, http.StatusCreated)
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repo, firstPath), nil), http.StatusNoContent, "delete first")

	rec := tpoPut(t, env, tpoMavenURL(repo, secondPath), second, http.StatusInsufficientStorage)
	if body := rec.Body.String(); !strings.Contains(body, "trashed items count until purged") || !strings.Contains(body, "quota_exceeded") {
		t.Errorf("quota error = %q, want the trash hint and the quota_exceeded code", body)
	}

	// Emptying the trash makes room again.
	tpoExpect(
		t,
		tpoSend(t, env, tpoAdmin, http.MethodDelete, strings.Replace(temRepoTrash, "%s", repo, 1), nil),
		http.StatusOK,
		"empty trash",
	)
	tpoPut(t, env, tpoMavenURL(repo, secondPath), second, http.StatusCreated)
}
