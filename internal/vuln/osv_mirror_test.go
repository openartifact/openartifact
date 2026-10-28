// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// fakeOSVStore serves a fixed mirror: the embedded interface is nil, so a
// method the code under test should not call panics.
type fakeOSVStore struct {
	database.OSVStore

	state      *models.OSVMirrorState
	advisories map[string][]models.OSVAdvisoryMatch
}

func (f *fakeOSVStore) GetOSVMirrorState(context.Context, string) (*models.OSVMirrorState, error) {
	return f.state, nil
}

func (f *fakeOSVStore) ListOSVAdvisoriesForPackages(
	_ context.Context,
	_ string,
	names []string,
) (map[string][]models.OSVAdvisoryMatch, error) {
	out := map[string][]models.OSVAdvisoryMatch{}

	for _, n := range names {
		if m, ok := f.advisories[n]; ok {
			out[n] = m
		}
	}

	return out, nil
}

func lodashMatch(id, severityScore, fixed string) models.OSVAdvisoryMatch {
	published := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	ranges, _ := json.Marshal([]map[string]any{{"type": "SEMVER", "events": []map[string]string{{"introduced": "0"}, {"fixed": fixed}}}})

	return models.OSVAdvisoryMatch{
		Advisory: models.OSVAdvisory{
			ID: id, Summary: "flaw " + id, Published: &published, DBSeverity: severityScore,
			Aliases: []string{"CVE-2024-1"}, Refs: []string{"https://example.test/" + id},
		},
		Affected: []models.OSVAffected{{Ecosystem: "npm", Name: "lodash", NameNorm: "lodash", Ranges: ranges}},
	}
}

func TestMirrorMatch(t *testing.T) {
	t.Parallel()

	hw := time.Now()
	m := NewMirror(&fakeOSVStore{
		state: &models.OSVMirrorState{Ecosystem: "npm", HighWater: &hw},
		advisories: map[string][]models.OSVAdvisoryMatch{
			"lodash": {lodashMatch("GHSA-2", "LOW", "4.17.21"), lodashMatch("GHSA-1", "HIGH", "4.17.0")},
		},
	})

	res, err := m.Match(context.Background(), "npm", []NameVersion{
		{
			Name:    "lodash",
			Version: "4.16.0",
		},
		{Name: "lodash", Version: "4.17.20"},
		{Name: "lodash", Version: "4.17.21"},
		{Name: "other", Version: "1.0.0"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(res[0]) != 2 || res[0][0].ID != "GHSA-1" || res[0][1].ID != "GHSA-2" {
		t.Fatalf("4.16.0 findings = %v, want both advisories sorted by id", res[0])
	}

	if len(res[1]) != 1 || res[1][0].ID != "GHSA-2" || res[1][0].FixedIn != "4.17.21" || res[1][0].Severity != "LOW" {
		t.Fatalf("4.17.20 findings = %+v, want GHSA-2 fixed in 4.17.21", res[1])
	}

	if len(res[2]) != 0 || len(res[3]) != 0 {
		t.Fatalf("a fixed version and an unrelated package must have no findings: %v %v", res[2], res[3])
	}

	if res[0][0].PublishedAt != "2024-01-02T03:04:05Z" {
		t.Fatalf("published = %q", res[0][0].PublishedAt)
	}
}

func TestMirrorReady(t *testing.T) {
	t.Parallel()

	hw := time.Now()

	for name, tc := range map[string]struct {
		state *models.OSVMirrorState
		eco   string
		want  bool
	}{
		"never imported":    {nil, "npm", false},
		"import incomplete": {&models.OSVMirrorState{Ecosystem: "npm"}, "npm", false},
		"imported":          {&models.OSVMirrorState{Ecosystem: "npm", HighWater: &hw}, "npm", true},
		"unsupported":       {&models.OSVMirrorState{Ecosystem: "Debian:11", HighWater: &hw}, "Debian:11", false},
	} {
		got, err := NewMirror(&fakeOSVStore{state: tc.state}).Ready(context.Background(), tc.eco)
		if err != nil || got != tc.want {
			t.Errorf("%s: Ready = %v, %v; want %v", name, got, err, tc.want)
		}
	}
}

func TestOSVScannerUsesMirrorOnlyWhenPopulated(t *testing.T) {
	t.Parallel()

	var online atomic.Int32

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		online.Add(1)
		_, _ = w.Write([]byte(`{"vulns": []}`))
	}))
	t.Cleanup(api.Close)

	hw := time.Now()
	store := &fakeOSVStore{
		state:      &models.OSVMirrorState{Ecosystem: "npm", HighWater: &hw},
		advisories: map[string][]models.OSVAdvisoryMatch{"lodash": {lodashMatch("GHSA-1", "HIGH", "4.17.21")}},
	}

	s := newTestScanner(t).WithMirror(NewMirror(store))
	s.osvBaseURL = api.URL

	pkg := Package{Name: "lodash", Version: "4.17.20", Ecosystem: "npm"}

	vulns, err := s.Scan(context.Background(), pkg)
	if err != nil || len(vulns) != 1 || vulns[0].ID != "GHSA-1" || online.Load() != 0 {
		t.Fatalf("mirror scan = %v, %v, online requests %d; want one finding and no online request", vulns, err, online.Load())
	}

	store.state = nil

	if _, err := s.Scan(context.Background(), pkg); err != nil || online.Load() == 0 {
		t.Fatalf("an unpopulated mirror must fall back to the online API (err %v, requests %d)", err, online.Load())
	}
}

func TestCountSeverities(t *testing.T) {
	t.Parallel()

	c, h, m, l := CountSeverities([]models.Vulnerability{
		{Severity: "CRITICAL"}, {Severity: "high"}, {Severity: "HIGH"}, {Severity: "MEDIUM"}, {Severity: "LOW"}, {Severity: "UNKNOWN"},
	})
	if c != 1 || h != 2 || m != 1 || l != 1 {
		t.Fatalf("CountSeverities = %d %d %d %d", c, h, m, l)
	}
}
