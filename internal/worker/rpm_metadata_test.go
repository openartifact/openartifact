// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"slices"
	"testing"
)

func TestSupersededMetadata(t *testing.T) {
	oldSHA := map[string]string{
		"repodata/old-primary.xml.gz":   "aa",
		"repodata/same-other.xml.gz":    "bb",
		"repodata/repomd.xml":           "r1",
		"repodata/repomd.xml.asc":       "s1",
		"public.key":                    "k1",
		"repodata/dup-filelists.xml.gz": "aa", // same blob under two paths
	}
	newSHA := map[string]string{
		"repodata/new-primary.xml.gz": "cc",
		"repodata/same-other.xml.gz":  "bb",
		"repodata/repomd.xml":         "r2",
		"public.key":                  "k1",
	}

	stale, reclaim := supersededMetadata(oldSHA, newSHA)

	wantStale := []string{"repodata/dup-filelists.xml.gz", "repodata/old-primary.xml.gz", "repodata/repomd.xml.asc"}
	if !slices.Equal(stale, wantStale) {
		t.Errorf("stalePaths = %v, want %v", stale, wantStale)
	}

	// "bb" and "k1" are unchanged; "aa" is deduplicated; r1 was re-pointed.
	wantReclaim := []string{"aa", "r1", "s1"}
	if !slices.Equal(reclaim, wantReclaim) {
		t.Errorf("reclaim = %v, want %v", reclaim, wantReclaim)
	}
}

func TestSupersededMetadataKeepsBlobStillInNewSet(t *testing.T) {
	// An old row whose blob the new set still uses under another path is not reclaimed.
	stale, reclaim := supersededMetadata(
		map[string]string{"repodata/a.xml.gz": "xx"},
		map[string]string{"repodata/b.xml.gz": "xx"},
	)

	if !slices.Equal(stale, []string{"repodata/a.xml.gz"}) || len(reclaim) != 0 {
		t.Errorf("stale=%v reclaim=%v, want [a] and none", stale, reclaim)
	}
}

func TestSupersededMetadataFirstRun(t *testing.T) {
	stale, reclaim := supersededMetadata(nil, map[string]string{"repodata/repomd.xml": "r"})
	if len(stale) != 0 || len(reclaim) != 0 {
		t.Errorf("first generation must not reclaim anything: stale=%v reclaim=%v", stale, reclaim)
	}
}
