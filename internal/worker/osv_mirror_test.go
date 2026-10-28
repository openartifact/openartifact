// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func vulns(ids ...string) []models.Vulnerability {
	out := make([]models.Vulnerability, len(ids))
	for i, id := range ids {
		out[i] = models.Vulnerability{ID: id, Severity: "HIGH"}
	}

	return out
}

func TestDiffFindings(t *testing.T) {
	t.Parallel()

	added, removed := diffFindings(vulns("A", "B"), vulns("B", "C", "D"))

	if len(added) != 2 || added[0].ID != "C" || added[1].ID != "D" {
		t.Fatalf("added = %v, want C and D", added)
	}

	if !slices.Equal(removed, []string{"A"}) {
		t.Fatalf("removed = %v, want [A]", removed)
	}

	if a, r := diffFindings(nil, nil); len(a) != 0 || len(r) != 0 {
		t.Fatalf("empty diff = %v, %v", a, r)
	}
}

func TestSameFindingsTreatsNilAndEmptyAlike(t *testing.T) {
	t.Parallel()

	if !sameFindings(nil, []models.Vulnerability{}) {
		t.Fatal("no findings must equal no findings, nil or not")
	}

	if !sameFindings(vulns("A"), vulns("A")) || sameFindings(vulns("A"), vulns("B")) {
		t.Fatal("findings compare by content")
	}

	changed := vulns("A")
	changed[0].Severity = "LOW"

	if sameFindings(vulns("A"), changed) {
		t.Fatal("a changed severity is a change")
	}
}

func TestRematchCandidateScope(t *testing.T) {
	t.Parallel()

	since := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	old := since.Add(-time.Hour)
	fresh := since.Add(time.Minute)
	touched := map[string]struct{}{"django-rest-framework": {}}

	incremental := &rematchRun{eco: "PyPI", scope: rematchScope{touched: touched, since: &since}}

	if !incremental.candidate("Django_Rest.Framework", old) {
		t.Error("a package an advisory names must be matched, whatever its age")
	}

	if incremental.candidate("requests", old) {
		t.Error("an old package no advisory names must be skipped")
	}

	if !incremental.candidate("requests", fresh) {
		t.Error("a version stored since the last re-match must be matched")
	}

	if !(&rematchRun{eco: "npm", scope: rematchScope{full: true}}).candidate("anything", old) {
		t.Error("a full re-match matches everything")
	}
}

func TestRematchRunDeduplicatesProxyVersions(t *testing.T) {
	t.Parallel()

	run := &rematchRun{seen: map[uint64]struct{}{}}

	if !run.firstSeen("repo", "pkg", "1.0") || run.firstSeen("repo", "pkg", "1.0") {
		t.Fatal("the second cached file of a version must be skipped")
	}

	if !run.firstSeen("repo", "pkg", "1.1") || !run.firstSeen("other", "pkg", "1.0") {
		t.Fatal("another version or repository is a different package")
	}
}
