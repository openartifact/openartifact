// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// cacheEntry inserts a proxy cache entry that expired expiredAgo ago (a
// negative duration is the future); blobKey "" makes it a negative entry.
func (s *purgeSeed) cacheEntry(repoID, key, blobKey string, expiredAgo time.Duration) {
	s.t.Helper()

	var bk any
	if blobKey != "" {
		bk = blobKey
	}

	s.exec(`INSERT INTO proxy_cache_entries (repository_id, cache_key, blob_key, status_code, size_bytes, expires_at)
		VALUES ($1, $2, $3, 200, 1, now() - make_interval(secs => $4))`, repoID, key, bk, expiredAgo.Seconds())
}

func (s *purgeSeed) proxyRepo(name string, maxStale int) string {
	s.t.Helper()

	org := trashTestOrgID

	repo := &models.Repository{Name: name, Format: "npm", RepoType: "proxy", ProjectID: &org, UpstreamURL: "https://example.com/", MaxStaleSeconds: maxStale}
	if err := s.pg.CreateRepository(context.Background(), repo); err != nil {
		s.t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	return repo.ID
}

// Eviction honours the grace period and each repository's stale window, drops
// negative entries as soon as grace ends, keeps a blob another entry still
// points at and removes one nothing references, across several batches.
func TestProxyCacheEvictionBatches(t *testing.T) {
	s := newPurgeSeed(t)
	repo := s.proxyRepo("evict-npm", 3600)
	w := NewProxyCacheWorker(s.pg, s.blobs, testLogger(t))

	_, usedSHA := s.blob("still referenced", 48*time.Hour)
	_, goneSHA := s.blob("only expired entries", 48*time.Hour)

	// Within grace, within stale window (1h grace + 1h stale), fresh: all kept.
	s.cacheEntry(repo, "keep/grace", goneSHA, 30*time.Minute)
	s.cacheEntry(repo, "keep/stale", goneSHA, 90*time.Minute)
	s.cacheEntry(repo, "keep/fresh", usedSHA, -time.Hour)
	s.cacheEntry(repo, "keep/neg-grace", "", 30*time.Minute)

	// Past both windows: evicted. More than two batches worth.
	const expired = evictBatchSize*2 + 123

	for i := range expired {
		blob := goneSHA
		if i%2 == 1 {
			blob = usedSHA
		}

		s.cacheEntry(repo, fmt.Sprintf("old/%d", i), blob, 5*time.Hour)
	}

	// A negative entry is evicted as soon as grace ends, no stale window.
	s.cacheEntry(repo, "old/neg", "", 2*time.Hour)

	// goneSHA is still referenced by the kept "keep/grace" row: nothing is
	// deleted from storage yet.
	n, err := w.evict(context.Background())
	if err != nil {
		t.Fatalf("evict: %v", err)
	}

	if n != expired+1 {
		t.Fatalf("evicted %d entries, want %d", n, expired+1)
	}

	if left := s.count(`SELECT count(*) FROM proxy_cache_entries WHERE repository_id = $1`, repo); left != 4 {
		t.Fatalf("%d entries left, want the 4 kept ones", left)
	}

	if _, ok := s.blobs.Blobs[goneSHA]; !ok {
		t.Fatal("blob still referenced by kept entries was deleted from storage")
	}

	if _, ok := s.blobs.Blobs[usedSHA]; !ok {
		t.Fatal("blob referenced by a fresh entry was deleted from storage")
	}

	// Drop the kept references; a later run removes the now unreferenced blob.
	s.exec(`UPDATE proxy_cache_entries SET expires_at = now() - interval '5 hours' WHERE cache_key IN ('keep/grace', 'keep/stale')`)

	if _, err := w.evict(context.Background()); err != nil {
		t.Fatalf("second evict: %v", err)
	}

	if _, ok := s.blobs.Blobs[goneSHA]; ok {
		t.Fatal("blob with no remaining reference was not deleted from storage")
	}

	if _, ok := s.blobs.Blobs[usedSHA]; !ok {
		t.Fatal("blob referenced by a fresh entry was deleted from storage")
	}

	if left := s.count(`SELECT count(*) FROM proxy_cache_entries WHERE repository_id = $1`, repo); left != 2 {
		t.Fatalf("%d entries left after second run, want 2", left)
	}
}

// A cached file stays for the repository's max_stale_seconds past grace, a
// different repository with a shorter window loses the same age.
func TestProxyCacheEvictionPerRepoStaleWindow(t *testing.T) {
	s := newPurgeSeed(t)
	long := s.proxyRepo("evict-long", 7*24*3600)
	short := s.proxyRepo("evict-short", 60)
	w := NewProxyCacheWorker(s.pg, s.blobs, testLogger(t))

	_, sha := s.blob("stale content", 48*time.Hour)

	s.cacheEntry(long, "pkg", sha, 3*24*time.Hour)
	s.cacheEntry(short, "pkg", sha, 3*24*time.Hour)

	if _, err := w.evict(context.Background()); err != nil {
		t.Fatalf("evict: %v", err)
	}

	if s.count(`SELECT count(*) FROM proxy_cache_entries WHERE repository_id = $1`, long) != 1 {
		t.Fatal("entry inside its repository's stale window was evicted")
	}

	if s.count(`SELECT count(*) FROM proxy_cache_entries WHERE repository_id = $1`, short) != 0 {
		t.Fatal("entry past its repository's stale window was kept")
	}

	if _, ok := s.blobs.Blobs[sha]; !ok {
		t.Fatal("blob still referenced by the long-window entry was deleted")
	}
}
