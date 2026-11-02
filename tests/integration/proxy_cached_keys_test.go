// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TestListCachedProxyKeys checks which cache keys count as cached files of a
// package: live entries with a stored body under the prefix, of that repository
// only, in key order, and a prefix is matched literally (no LIKE wildcards).
func TestListCachedProxyKeys(t *testing.T) {
	ctx := t.Context()

	repo := newCurationProxy(t, "cached-keys-px", curation.ModeOff)
	other := newCurationProxy(t, "cached-keys-other", curation.ModeOff)

	put := func(r *models.Repository, key string, status int, blob string) {
		t.Helper()

		entry := &models.ProxyCacheEntry{
			RepositoryID: r.ID, CacheKey: key, BlobKey: blob, StatusCode: status, ExpiresAt: time.Now().Add(time.Hour),
		}
		if err := harness.Database.UpsertProxyCacheEntry(ctx, entry); err != nil {
			t.Fatalf("UpsertProxyCacheEntry %s: %v", key, err)
		}
	}

	blob := strings.Repeat("ca", 32)
	if _, err := harness.Database.EnsureBlob(ctx, blob, "application/gzip", 4); err != nil {
		t.Fatalf("EnsureBlob: %v", err)
	}

	put(repo, "lib/-/lib-1.1.0.tgz", http.StatusOK, blob)
	put(repo, "lib/-/lib-1.0.0.tgz", http.StatusOK, blob)
	put(repo, "lib/-/lib-0.9.0.tgz", http.StatusNotFound, "")
	put(repo, "lib", http.StatusOK, blob)
	put(repo, "libx/-/libx-2.0.0.tgz", http.StatusOK, blob)
	put(repo, "l_b/-/l_b-1.0.0.tgz", http.StatusOK, blob)
	put(other, "lib/-/lib-3.0.0.tgz", http.StatusOK, blob)

	got, err := harness.Database.ListCachedProxyKeys(ctx, repo.ID, "lib/-/", 10)
	if err != nil {
		t.Fatalf("ListCachedProxyKeys: %v", err)
	}

	if want := []string{"lib/-/lib-1.0.0.tgz", "lib/-/lib-1.1.0.tgz"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v (live files of this repository under the prefix, in key order)", got, want)
	}

	if got, err = harness.Database.ListCachedProxyKeys(
		ctx,
		repo.ID,
		"l_b/",
		10,
	); err != nil ||
		!slices.Equal(got, []string{"l_b/-/l_b-1.0.0.tgz"}) {
		t.Errorf("a prefix with an underscore = %v, %v; want only the literal match", got, err)
	}

	if got, err = harness.Database.ListCachedProxyKeys(ctx, repo.ID, "lib/-/", 1); err != nil || len(got) != 1 {
		t.Errorf("limit 1 = %v, %v; want one key", got, err)
	}
}

// TestProxyCacheDroppedOnUpstreamChange checks that changing the upstream URL
// (or credentials) of a proxy repository drops its cached rows, negative ones
// included, and that an unrelated update keeps them.
func TestProxyCacheDroppedOnUpstreamChange(t *testing.T) {
	ctx := t.Context()

	repo := newCurationProxy(t, "cache-upstream-px", curation.ModeOff)
	other := newCurationProxy(t, "cache-upstream-other", curation.ModeOff)

	blob := strings.Repeat("cb", 32)
	if _, err := harness.Database.EnsureBlob(ctx, blob, "application/json", 4); err != nil {
		t.Fatalf("EnsureBlob: %v", err)
	}

	put := func(r *models.Repository, key string, status int, blobKey string) {
		t.Helper()

		entry := &models.ProxyCacheEntry{
			RepositoryID: r.ID, CacheKey: key, BlobKey: blobKey, StatusCode: status, ExpiresAt: time.Now().Add(time.Hour),
		}
		if err := harness.Database.UpsertProxyCacheEntry(ctx, entry); err != nil {
			t.Fatalf("UpsertProxyCacheEntry %s: %v", key, err)
		}
	}

	cached := func(r *models.Repository, key string) bool {
		t.Helper()

		e, err := harness.Database.GetProxyCacheEntry(ctx, r.ID, key)
		if err != nil {
			t.Fatalf("GetProxyCacheEntry %s: %v", key, err)
		}

		return e != nil
	}

	seed := func() {
		put(repo, "doc", http.StatusOK, blob)
		put(repo, "missing", http.StatusNotFound, "")
		put(other, "doc", http.StatusOK, blob)
	}

	seed()

	repo.Description = "unrelated change"
	if err := harness.Database.UpdateRepository(ctx, repo); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	if !cached(repo, "doc") || !cached(repo, "missing") {
		t.Fatal("an update that leaves the upstream alone dropped the cache")
	}

	repo.UpstreamURL = "https://mirror.example.com"
	if err := harness.Database.UpdateRepository(ctx, repo); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	if cached(repo, "doc") || cached(repo, "missing") {
		t.Error("changing upstream_url kept cached rows of the old upstream")
	}

	if !cached(other, "doc") {
		t.Error("changing one repository's upstream dropped another's cache")
	}

	seed()

	repo.UpstreamUsername = "bot"
	if err := harness.Database.UpdateRepository(ctx, repo); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	if cached(repo, "doc") || cached(repo, "missing") {
		t.Error("changing the upstream credentials kept cached rows")
	}
}
