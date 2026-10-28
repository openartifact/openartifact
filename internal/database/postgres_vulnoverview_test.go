// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestVulnOverviewQuery_SeveritySortIsUnchangedByTheExploitColumns(t *testing.T) {
	query, args, err := vulnOverviewQuery(models.VulnScanListParams{Limit: 10, EPSSThreshold: 0.1})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(query, "kev_count >") {
		t.Error("the severity sort ranks by priority")
	}

	if len(args) != 1 || args[0] != 11 {
		t.Errorf("args = %v, want only the page size", args)
	}

	if !strings.Contains(query, "ORDER BY vr.critical_count DESC, vr.high_count DESC") {
		t.Errorf("order is not the severity order: %s", query)
	}
}

func TestVulnOverviewQuery_PrioritySortRanksFirstAndBindsTheThresholdOnce(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	params := models.VulnScanListParams{
		Sort: models.VulnSortPriority, Limit: 10, EPSSThreshold: 0.25, Priority: "epss",
		After: &models.VulnScanCursor{Rank: 5, CriticalCount: 1, SortAt: at, ID: "00000000-0000-0000-0000-000000000001"},
	}

	query, args, err := vulnOverviewQuery(params)
	if err != nil {
		t.Fatal(err)
	}

	if args[0] != 0.25 {
		t.Errorf("first arg = %v, want the EPSS threshold", args[0])
	}

	// threshold, cursor rank + 4 counts + time, id, limit+1.
	if len(args) != 9 {
		t.Errorf("args = %v", args)
	}

	for _, want := range []string{"ORDER BY (CASE WHEN vr.kev_count > 0 THEN 6", "vr.max_epss >= $1::float8", "$2::int"} {
		if !strings.Contains(query, want) {
			t.Errorf("query lacks %q:\n%s", want, query)
		}
	}
}

func TestVulnOverviewQuery_KEVFilterUsesThePartialIndexPredicate(t *testing.T) {
	yes := true

	query, _, err := vulnOverviewQuery(models.VulnScanListParams{Limit: 5, KEV: &yes})
	if err != nil || !strings.Contains(query, "vr.kev_count > 0") {
		t.Errorf("kev=true query = %v, %v", query, err)
	}
}

func TestVulnOverviewQuery_RefusesUnknownSortAndPriority(t *testing.T) {
	for _, p := range []models.VulnScanListParams{{Sort: "newest"}, {Priority: "none"}, {Priority: "urgent"}} {
		p.Limit = 5
		if _, _, err := vulnOverviewQuery(p); !errors.Is(err, ErrInvalidVulnScanFilter) {
			t.Errorf("%+v: err = %v", p, err)
		}
	}
}
