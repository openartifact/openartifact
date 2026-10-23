// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
)

// blobExists reports whether the blob is still in the store.
func (e *cleanupQuotaEnv) blobExists(sha string) bool {
	e.t.Helper()

	sweepQueuedBlobs(e.t, e.pg, e.store)

	_, exists, err := e.store.ExistsBySHA256(context.Background(), sha)
	if err != nil {
		e.t.Fatalf("ExistsBySHA256: %v", err)
	}

	return exists
}

func (e *cleanupQuotaEnv) countRows(query string, args ...any) int {
	e.t.Helper()

	var n int
	if err := e.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}

	return n
}

// A policy that removes more rows than fit one batch removes all of them and
// their blobs, and hands exactly their usage back.
func TestCleanupPolicyDeletesAcrossBatches(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("helm")

	const versions = 450 // more than two 200-row batches

	shas := make([]string, 0, versions)

	var keptBytes int64

	for i := range versions {
		// Version 0 is the newest, the rest ever older.
		sha, size := e.pkg(repoID, "helm", "chart", fmt.Sprintf("1.0.%d", i), 10, 1+i)
		if i == 0 {
			keptBytes = size
			continue
		}

		shas = append(shas, sha)
	}

	got := e.runPolicy(repoID, 1)
	if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != versions-1 {
		t.Fatalf("policy run: deleted = %v, error = %q; want %d deleted", got.LastRunDeleted, got.LastRunError, versions-1)
	}

	if left := e.countRows(`SELECT count(*) FROM helm_charts WHERE repository_id = $1`, repoID); left != 1 {
		t.Fatalf("%d rows left, want only the newest version", left)
	}

	for _, sha := range shas {
		if e.blobExists(sha) {
			t.Fatalf("blob %s of a deleted version is still in storage", sha[:12])
		}
	}

	if size, count := e.usage(repoID); size != keptBytes || count != 1 {
		t.Errorf("usage after cleanup = %d / %d, want %d / 1", size, count, keptBytes)
	}
}

// A blob pinned by a release bundle keeps its row (and bytes) while the other
// old versions of the same batch go.
func TestCleanupPolicySkipsBundledBlob(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("helm")

	pinnedSHA, _ := e.pkg(repoID, "helm", "chart", "1.0.0", 100, 30)
	goneSHA, _ := e.pkg(repoID, "helm", "chart", "1.1.0", 100, 20)
	e.pkg(repoID, "helm", "chart", "2.0.0", 100, 10)

	bundleID := e.queryID(`INSERT INTO release_bundles (name, version) VALUES ('rel', '1') RETURNING id`)
	e.exec(`INSERT INTO release_bundle_items (bundle_id, source_repo_id, sha256, artifact_path, format) VALUES ($1, $2, $3, 'chart-1.0.0.tgz', 'helm')`,
		bundleID, repoID, pinnedSHA)

	got := e.runPolicy(repoID, 1)
	if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 1 {
		t.Fatalf("policy run: deleted = %v, error = %q; want 1 deleted", got.LastRunDeleted, got.LastRunError)
	}

	if !e.blobExists(pinnedSHA) || e.countRows(`SELECT count(*) FROM helm_charts WHERE repository_id = $1 AND version = '1.0.0'`, repoID) != 1 {
		t.Error("the version pinned by a release bundle was removed")
	}

	if e.blobExists(goneSHA) || e.countRows(`SELECT count(*) FROM helm_charts WHERE repository_id = $1 AND version = '1.1.0'`, repoID) != 0 {
		t.Error("the unpinned old version was kept")
	}
}

// Released versions of an immutable repository are never cleaned up.
func TestCleanupPolicySkipsImmutableReleases(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("helm")

	releaseSHA, _ := e.pkg(repoID, "helm", "chart", "1.0.0", 100, 30)
	preSHA, _ := e.pkg(repoID, "helm", "chart", "1.1.0-rc.1", 100, 20)
	e.pkg(repoID, "helm", "chart", "2.0.0", 100, 10)
	e.exec(`UPDATE repositories SET immutable_releases = true WHERE id = $1`, repoID)

	got := e.runPolicy(repoID, 1)
	if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 1 {
		t.Fatalf("policy run: deleted = %v, error = %q; want only the pre-release deleted", got.LastRunDeleted, got.LastRunError)
	}

	if !e.blobExists(releaseSHA) {
		t.Error("a released version of an immutable repository was removed")
	}

	if e.blobExists(preSHA) {
		t.Error("the pre-release version was kept")
	}
}

// With trash retention the rows move to the trash together: their blobs and
// quota usage stay until the purge.
func TestCleanupPolicyMovesBatchToTrash(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	e.pg.SetTrashRetentionDays(7)

	repoID := e.repo("helm")

	sha1, b1 := e.pkg(repoID, "helm", "chart", "1.0.0", 100, 30)
	sha2, b2 := e.pkg(repoID, "helm", "chart", "1.1.0", 100, 20)
	_, b3 := e.pkg(repoID, "helm", "chart", "2.0.0", 100, 10)

	got := e.runPolicy(repoID, 1)
	if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 2 {
		t.Fatalf("policy run: deleted = %v, error = %q; want 2 moved", got.LastRunDeleted, got.LastRunError)
	}

	if n := e.countRows(`SELECT count(*) FROM helm_charts WHERE repository_id = $1 AND deleted_at IS NOT NULL AND deleted_by = 'cleanup-policy'`, repoID); n != 2 {
		t.Errorf("%d rows in the trash, want 2", n)
	}

	if n := e.countRows(`SELECT count(*) FROM helm_charts WHERE repository_id = $1 AND deleted_at IS NULL`, repoID); n != 1 {
		t.Errorf("%d live rows, want 1", n)
	}

	if !e.blobExists(sha1) || !e.blobExists(sha2) {
		t.Error("blobs of trashed rows were removed from storage before the purge")
	}

	if size, count := e.usage(repoID); size != b1+b2+b3 || count != 3 {
		t.Errorf("usage = %d / %d, want %d / 3 (kept until the purge)", size, count, b1+b2+b3)
	}
}

// A blob also referenced by a row that stays is not removed from storage.
func TestCleanupPolicyKeepsSharedBlob(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("helm")

	sharedSHA, _ := e.pkg(repoID, "helm", "chart", "1.0.0", 100, 30)
	e.pkg(repoID, "helm", "chart", "2.0.0", 100, 10)

	// The newest version shares the oldest one's blob.
	e.exec(`UPDATE helm_charts SET blob_id = (SELECT blob_id FROM helm_charts WHERE repository_id = $1 AND version = '1.0.0')
	        WHERE repository_id = $1 AND version = '2.0.0'`, repoID)

	got := e.runPolicy(repoID, 1)
	if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 1 {
		t.Fatalf("policy run: deleted = %v, error = %q; want 1 deleted", got.LastRunDeleted, got.LastRunError)
	}

	if !e.blobExists(sharedSHA) {
		t.Error("a blob still referenced by the kept version was removed from storage")
	}
}
