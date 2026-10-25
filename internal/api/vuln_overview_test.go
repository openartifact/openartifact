// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

const overviewScanID = "44444444-4444-4444-4444-444444444444"

type vulnOverviewMockDB struct {
	databasetest.BaseMockDB

	page       *models.VulnScanOverviewPage
	summary    *models.VulnScanSummary
	err        error
	listCalls  int
	sumCalls   int
	gotParams  models.VulnScanListParams
	gotSumRepo string
}

func (db *vulnOverviewMockDB) ListVulnScanOverview(_ context.Context, p models.VulnScanListParams) (*models.VulnScanOverviewPage, error) {
	db.listCalls++
	db.gotParams = p

	return db.page, db.err
}

func (db *vulnOverviewMockDB) VulnScanOverviewSummary(_ context.Context, repo string) (*models.VulnScanSummary, error) {
	db.sumCalls++
	db.gotSumRepo = repo

	return db.summary, db.err
}

// serveOverview serves GET target through the overview routes as principal
// (nil: no principal at all).
func serveOverview(t *testing.T, db *vulnOverviewMockDB, principal *auth.Principal, target string) *httptest.ResponseRecorder {
	t.Helper()

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	h := NewVulnHandler(db, log, ev, nil)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal != nil {
				r = r.WithContext(middleware.WithPrincipal(r.Context(), principal))
			}

			next.ServeHTTP(w, r)
		})
	})
	h.RegisterOverviewRoutes(router)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func adminPrincipal() *auth.Principal { return &auth.Principal{Username: "admin", IsLegacy: true} }

func TestVulnOverview_AdminOnly(t *testing.T) {
	for name, principal := range map[string]*auth.Principal{
		"no principal": nil,
		"anonymous":    {IsAnonymous: true},
	} {
		for _, target := range []string{"/api/v1/vuln/scans", "/api/v1/vuln/scans/summary"} {
			t.Run(name+" "+target, func(t *testing.T) {
				db := &vulnOverviewMockDB{}

				rw := serveOverview(t, db, principal, target)
				if rw.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403 (%s)", rw.Code, rw.Body.String())
				}

				if db.listCalls+db.sumCalls != 0 {
					t.Error("the database was queried for a caller that may not read the overview")
				}
			})
		}
	}
}

func TestVulnOverview_FiltersReachTheQuery(t *testing.T) {
	yes := true
	no := false

	tests := []struct {
		query string
		want  models.VulnScanListParams
	}{
		{"", models.VulnScanListParams{Limit: 25}},
		{"limit=7", models.VulnScanListParams{Limit: 7}},
		{"limit=500", models.VulnScanListParams{Limit: 100}},
		{"limit=0", models.VulnScanListParams{Limit: 25}},
		{"limit=abc", models.VulnScanListParams{Limit: 25}},
		{"repo=npm-main", models.VulnScanListParams{Repo: "npm-main", Limit: 25}},
		{"severity=high", models.VulnScanListParams{Severity: "high", Limit: 25}},
		{"status=scanning", models.VulnScanListParams{Status: "scanning", Limit: 25}},
		{"scanner=grype", models.VulnScanListParams{Scanner: "grype", Limit: 25}},
		{"quarantined=true", models.VulnScanListParams{Quarantined: &yes, Limit: 25}},
		{"quarantined=false", models.VulnScanListParams{Quarantined: &no, Limit: 25}},
		{"repo=a&severity=critical&status=failed&scanner=osv&quarantined=true&limit=3",
			models.VulnScanListParams{Repo: "a", Severity: "critical", Status: "failed", Scanner: "osv", Quarantined: &yes, Limit: 3}},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			db := &vulnOverviewMockDB{page: &models.VulnScanOverviewPage{}}

			rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans?"+tt.query)
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
			}

			got := db.gotParams
			if got.Repo != tt.want.Repo || got.Severity != tt.want.Severity || got.Status != tt.want.Status ||
				got.Scanner != tt.want.Scanner || got.Limit != tt.want.Limit || got.After != nil ||
				(got.Quarantined == nil) != (tt.want.Quarantined == nil) ||
				(got.Quarantined != nil && *got.Quarantined != *tt.want.Quarantined) {
				t.Errorf("params = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestVulnOverview_InvalidFiltersAreRejected(t *testing.T) {
	for _, query := range []string{
		"severity=urgent", "severity=CRITICAL", "status=done", "status=pending", "scanner=snyk", "scanner=osv,trivy",
		"quarantined=yes", "quarantined=1", "repo=" + strings.Repeat("x", 300),
	} {
		t.Run(query, func(t *testing.T) {
			db := &vulnOverviewMockDB{page: &models.VulnScanOverviewPage{}}

			rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans?"+query)
			if rw.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rw.Code, rw.Body.String())
			}

			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil || body.Code != "validation_failed" {
				t.Errorf("code = %q (%v), want validation_failed", body.Code, err)
			}

			if db.listCalls != 0 {
				t.Error("an invalid filter reached the database")
			}
		})
	}
}

func TestVulnOverview_CursorRoundTripsAndForgeriesAreRejected(t *testing.T) {
	at := time.Date(2026, 5, 4, 3, 2, 1, 123456000, time.UTC)
	cursor := &models.VulnScanCursor{CriticalCount: 3, HighCount: 2, MediumCount: 1, LowCount: 0, SortAt: at, ID: overviewScanID}

	t.Run("a cursor the server issued is accepted as given", func(t *testing.T) {
		db := &vulnOverviewMockDB{page: &models.VulnScanOverviewPage{}}

		rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans?cursor="+encodeVulnCursor(cursor))
		if rw.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
		}

		got := db.gotParams.After
		if got == nil || *got != *cursor {
			t.Errorf("cursor = %+v, want %+v", got, cursor)
		}
	})

	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	micros := strconv.FormatInt(at.UnixMicro(), 10)

	forged := map[string]string{
		"not base64":             "!!!",
		"standard base64 padded": base64.StdEncoding.EncodeToString([]byte("1:2:3:4:" + micros + ":" + overviewScanID)),
		"empty segments":         b64(":::::"),
		"too few fields":         b64("1:2:" + micros + ":" + overviewScanID),
		"too many fields":        b64("1:2:3:4:" + micros + ":" + overviewScanID + ":x"),
		"negative count":         b64("-1:2:3:4:" + micros + ":" + overviewScanID),
		"count overflows int32":  b64("99999999999:2:3:4:" + micros + ":" + overviewScanID),
		"non numeric count":      b64("a:2:3:4:" + micros + ":" + overviewScanID),
		"time before year 1":     b64("1:2:3:4:-99999999999999999:" + overviewScanID),
		"time after year 9999":   b64("1:2:3:4:999999999999999999:" + overviewScanID),
		"time is not a number":   b64("1:2:3:4:soon:" + overviewScanID),
		"id is not a uuid":       b64("1:2:3:4:" + micros + ":1 OR 1=1"),
		"audit cursor":           encodeAuditCursor(&models.AuditCursor{CreatedAt: at, ID: overviewScanID}),
	}

	for name, c := range forged {
		t.Run(name, func(t *testing.T) {
			db := &vulnOverviewMockDB{page: &models.VulnScanOverviewPage{}}

			rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans?cursor="+url.QueryEscape(c))
			if rw.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rw.Code, rw.Body.String())
			}

			if db.listCalls != 0 {
				t.Error("a forged cursor reached the database")
			}
		})
	}
}

func TestVulnOverview_ResponseIsASummaryPageWithACursor(t *testing.T) {
	scanned := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := func(id string) *models.VulnScanOverviewRow {
		return &models.VulnScanOverviewRow{
			ID: id, RepositoryID: testRepoID, ArtifactID: "pkg/a", PackageName: "a", PackageVersion: "1.0.0", Ecosystem: "npm",
			Scanner: "osv,trivy", ScanStatus: "partial", VulnCount: 4, CriticalCount: 1, HighCount: 1, MediumCount: 1, LowCount: 1,
			Quarantined: true, ScanError: "trivy is not available", FailedScanners: []string{"trivy"}, ScannedAt: &scanned,
			CreatedAt: scanned, UpdatedAt: scanned,
			// The overview never selects findings; a row that had them must still not return them.
			Vulnerabilities: []models.Vulnerability{{ID: "CVE-2026-0001", Summary: "leaky"}},
			RepositoryName:  "npm-main", RepositoryFormat: "npm", SortAt: scanned,
		}
	}

	db := &vulnOverviewMockDB{page: &models.VulnScanOverviewPage{
		Scans: []*models.VulnScanOverviewRow{row(overviewScanID)},
		Next:  &models.VulnScanCursor{CriticalCount: 1, HighCount: 1, MediumCount: 1, LowCount: 1, SortAt: scanned, ID: overviewScanID},
	}}

	rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	var raw map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}

	if raw["has_more"] != true {
		t.Errorf("has_more = %v, want true", raw["has_more"])
	}

	next, _ := raw["next_cursor"].(string)
	if _, err := decodeVulnCursor(next); err != nil {
		t.Errorf("next_cursor %q does not decode: %v", next, err)
	}

	scans, _ := raw["scans"].([]any)
	if len(scans) != 1 {
		t.Fatalf("scans = %v", raw["scans"])
	}

	got, _ := scans[0].(map[string]any)
	if _, has := got["vulnerabilities"]; has {
		t.Error("the overview row carries the findings")
	}

	for key, want := range map[string]any{
		"id": overviewScanID, "repo_id": testRepoID, "repository_name": "npm-main", "repository_format": "npm", "artifact_id": "pkg/a",
		"package_name": "a", "package_version": "1.0.0", "ecosystem": "npm", "scanner": "osv,trivy", "scan_status": "partial",
		"vuln_count": 4.0, "critical_count": 1.0, "high_count": 1.0, "medium_count": 1.0, "low_count": 1.0, "quarantined": true,
		"scan_error": "trivy is not available", "scanned_at": "2026-01-02T03:04:05Z",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}

	if fs, _ := got["failed_scanners"].([]any); len(fs) != 1 || fs[0] != "trivy" {
		t.Errorf("failed_scanners = %v", got["failed_scanners"])
	}
}

func TestVulnOverview_LastPageHasNoCursorAndAnEmptyListIsNotNull(t *testing.T) {
	db := &vulnOverviewMockDB{page: &models.VulnScanOverviewPage{}}

	rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}

	body := rw.Body.String()
	if !strings.Contains(body, `"scans":[]`) || !strings.Contains(body, `"has_more":false`) || strings.Contains(body, "next_cursor") {
		t.Errorf("body = %s", body)
	}
}

func TestVulnOverview_DatabaseErrorsDoNotLeak(t *testing.T) {
	for _, target := range []string{"/api/v1/vuln/scans", "/api/v1/vuln/scans/summary"} {
		t.Run(target, func(t *testing.T) {
			db := &vulnOverviewMockDB{err: errors.New("pq: relation secret_table does not exist")}

			rw := serveOverview(t, db, adminPrincipal(), target)
			if rw.Code != http.StatusInternalServerError || strings.Contains(rw.Body.String(), "secret_table") {
				t.Errorf("status = %d, body = %s", rw.Code, rw.Body.String())
			}
		})
	}

	t.Run("an invalid filter the handler let through is a 400", func(t *testing.T) {
		db := &vulnOverviewMockDB{err: database.ErrInvalidVulnScanFilter}

		if rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans"); rw.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rw.Code)
		}
	})
}

func TestVulnOverviewSummary(t *testing.T) {
	db := &vulnOverviewMockDB{summary: &models.VulnScanSummary{
		Total: 10, Completed: 7, Partial: 1, Failed: 1, Scanning: 1, Quarantined: 2,
		WithCritical: 3, WithHigh: 5, WithMedium: 6, WithLow: 7,
	}}

	rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans/summary?repo=npm-main")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	if db.gotSumRepo != "npm-main" {
		t.Errorf("repo = %q, want npm-main", db.gotSumRepo)
	}

	var got map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]float64{"total": 10, "completed": 7, "partial": 1, "failed": 1, "scanning": 1, "quarantined": 2} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}

	sev, _ := got["severity_at_least"].(map[string]any)
	if sev["critical"] != 3.0 || sev["high"] != 5.0 || sev["medium"] != 6.0 || sev["low"] != 7.0 {
		t.Errorf("severity_at_least = %v", sev)
	}

	if rw := serveOverview(t, db, adminPrincipal(), "/api/v1/vuln/scans/summary?repo="+strings.Repeat("x", 300)); rw.Code != http.StatusBadRequest {
		t.Errorf("a long repo: status = %d, want 400", rw.Code)
	}
}
