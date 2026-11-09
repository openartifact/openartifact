// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vexsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/csaf"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/pkg/logger"
)

const feedID = "feed-1"

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return log
}

// scannedTargets are the artifacts the feed fixture is matched against.
func scannedTargets() []models.VEXTarget {
	return []models.VEXTarget{
		{RepositoryID: "npm-repo", RepositoryFormat: "npm", ArtifactID: "left-pad@1.3.0", Vulns: []models.Vulnerability{
			{ID: "GHSA-aaaa-bbbb-cccc", Aliases: []string{"CVE-2024-1111"}},
			{ID: "CVE-2024-2222"},
		}},
		{RepositoryID: "npm-repo", RepositoryFormat: "npm", ArtifactID: "left-pad@1.2.0", Vulns: []models.Vulnerability{
			{ID: "CVE-2024-1111"},
		}},
		{RepositoryID: "raw-repo", RepositoryFormat: "raw", ArtifactID: "acme-server@2.0.1", Vulns: []models.Vulnerability{
			{ID: "cve-2024-1111"},
		}},
		{RepositoryID: "raw-repo", RepositoryFormat: "raw", ArtifactID: "acme-server@2.0.2", Vulns: []models.Vulnerability{
			{ID: "CVE-2024-1111"},
		}},
	}
}

// vendorServer serves the CSAF fixture at /vex.json, nothing else (no hash, no
// signature). hit, when set, is signalled on each request for the document,
// which then waits for release.
type vendorServer struct {
	*httptest.Server

	hit     chan struct{}
	release chan struct{}
}

func newVendorServer(t *testing.T) *vendorServer {
	t.Helper()

	body, err := os.ReadFile("testdata/vendor-vex.json")
	if err != nil {
		t.Fatal(err)
	}

	v := &vendorServer{}
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vex.json" {
			http.NotFound(w, r)

			return
		}

		if v.hit != nil {
			v.hit <- struct{}{}
			<-v.release
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(v.Close)

	return v
}

func newTestService(t *testing.T, db *memStore, srv *vendorServer) *Service {
	t.Helper()

	var fetcher *csaf.Fetcher
	if srv != nil {
		fetcher = csaf.NewFetcher(srv.Client())
	}

	s := NewService(db, fetcher, sbom.NewGenerator(nil, testLogger(t)), testLogger(t))
	t.Cleanup(s.Stop)

	return s
}

func syncFixture(t *testing.T, requireSignature bool) (*memStore, string, models.VEXFeedSyncResult) {
	t.Helper()

	srv := newVendorServer(t)
	db := newMemStore(scannedTargets()...)
	db.addFeed(&models.VEXFeed{ID: feedID, Name: "acme", URL: srv.URL + "/vex.json", Enabled: true, RequireSignature: requireSignature})

	res, err := newTestService(t, db, srv).SyncFeed(context.Background(), feedID)
	if err != nil {
		t.Fatalf("SyncFeed: %v", err)
	}

	return db, srv.URL + "/vex.json", res
}

func TestSyncFeedRecordsTheFetchedDocuments(t *testing.T) {
	db, _, res := syncFixture(t, false)

	wantCursor := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	if !res.Success || res.Documents != 1 || res.Rejected != 0 || res.Error != "" ||
		res.CursorAt == nil || !res.CursorAt.Equal(wantCursor) {
		t.Fatalf("result = %+v, want one clean document with cursor %s", res, wantCursor)
	}

	if len(db.syncs) != 1 || db.syncs[0].Documents != 1 {
		t.Fatalf("recorded syncs = %+v, want the one result", db.syncs)
	}
}

func TestSyncFeedMaterializesMatchingScannedVulnerabilities(t *testing.T) {
	db, docURL, _ := syncFixture(t, false)

	// The scan reports the GHSA id with the feed's CVE as alias: the decision
	// is recorded under the scan's id, so it suppresses that finding.
	purl := db.statement("npm-repo", "left-pad@1.3.0", "GHSA-aaaa-bbbb-cccc")
	if purl == nil {
		t.Fatal("no statement for the purl-matched artifact's GHSA finding")
	}

	if purl.Status != models.VEXNotAffected || purl.Justification != models.VEXVulnerableCodeNotPresent ||
		purl.SourceKind != models.VEXSourceCSAF || purl.SourceName != "acme" || purl.SourceRef != docURL ||
		purl.FeedID != feedID || purl.CreatedBy != "" {
		t.Errorf("purl-matched statement = %+v", purl)
	}

	// CPE fallback: product acme_server matches package acme-server at the exact version.
	cpe := db.statement("raw-repo", "acme-server@2.0.1", "cve-2024-1111")
	if cpe == nil || cpe.Status != models.VEXNotAffected || cpe.SourceName != "acme" {
		t.Errorf("CPE-matched statement = %+v, want not_affected from acme", cpe)
	}

	// Other versions, and vulnerabilities the scans do not report
	// (CVE-2024-9999) or the feed does not speak about (CVE-2024-2222), get nothing.
	if n := db.statementCount(); n != 2 {
		t.Errorf("statements = %d, want only the purl and the CPE match", n)
	}
}

func TestSyncFeedNeverReplacesAnotherSource(t *testing.T) {
	srv := newVendorServer(t)
	db := newMemStore(scannedTargets()...)
	db.addFeed(&models.VEXFeed{ID: feedID, Name: "acme", URL: srv.URL + "/vex.json", Enabled: true})
	db.put(&models.VEXStatement{
		RepositoryID: "npm-repo", ArtifactID: "left-pad@1.3.0", VulnID: "GHSA-aaaa-bbbb-cccc",
		Status: models.VEXAffected, SourceKind: models.VEXSourceLocal, CreatedBy: "alice",
	})

	if _, err := newTestService(t, db, srv).SyncFeed(context.Background(), feedID); err != nil {
		t.Fatal(err)
	}

	got := db.statement("npm-repo", "left-pad@1.3.0", "GHSA-aaaa-bbbb-cccc")
	if got.SourceKind != models.VEXSourceLocal || got.Status != models.VEXAffected {
		t.Errorf("local decision replaced by %+v", got)
	}
}

func TestSyncFeedRejectsUnsignedDocumentWhenSignatureRequired(t *testing.T) {
	db, _, res := syncFixture(t, true)

	if !res.Success || res.Documents != 0 || res.Rejected != 1 || !strings.HasPrefix(res.Error, "1 document rejected: ") {
		t.Fatalf("result = %+v, want one rejected document", res)
	}

	if len(db.syncs) != 1 || db.syncs[0].Error != res.Error {
		t.Errorf("recorded syncs = %+v, want the rejection summary", db.syncs)
	}

	if n := db.statementCount(); n != 0 {
		t.Errorf("statements = %d, want none from a rejected document", n)
	}
}

func TestSyncFeedRecordsFetchFailure(t *testing.T) {
	srv := newVendorServer(t)
	db := newMemStore(scannedTargets()...)
	db.addFeed(&models.VEXFeed{ID: feedID, Name: "acme", URL: srv.URL + "/missing.json", Enabled: true})

	res, err := newTestService(t, db, srv).SyncFeed(context.Background(), feedID)
	if err != nil {
		t.Fatal(err)
	}

	if res.Success || res.Error == "" || len(db.syncs) != 1 || db.syncs[0].Success {
		t.Errorf("result = %+v, syncs = %+v, want a recorded failure", res, db.syncs)
	}
}

func TestApplyFeedsDropsMatchesTheScansNoLongerReport(t *testing.T) {
	db, _, _ := syncFixture(t, false)

	// A rescan of left-pad 1.3.0 no longer reports the vulnerability.
	db.mu.Lock()
	db.targets[0].Vulns = []models.Vulnerability{{ID: "CVE-2024-2222"}}
	db.mu.Unlock()

	s := newTestService(t, db, nil)
	if err := s.ApplyFeeds(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := db.statement("npm-repo", "left-pad@1.3.0", "GHSA-aaaa-bbbb-cccc"); got != nil {
		t.Errorf("stale feed statement kept: %+v", got)
	}

	if got := db.statement("raw-repo", "acme-server@2.0.1", "cve-2024-1111"); got == nil {
		t.Error("the CPE match was dropped")
	}
}

func TestStartSyncRefusesAFeedAlreadySyncing(t *testing.T) {
	srv := newVendorServer(t)
	srv.hit, srv.release = make(chan struct{}), make(chan struct{})

	db := newMemStore(scannedTargets()...)
	db.addFeed(&models.VEXFeed{ID: feedID, Name: "acme", URL: srv.URL + "/vex.json", Enabled: true})
	s := newTestService(t, db, srv)

	if !s.StartSync(feedID) {
		t.Fatal("first StartSync did not start")
	}

	<-srv.hit

	if s.StartSync(feedID) {
		t.Error("second StartSync started while the first runs")
	}

	if _, err := s.SyncFeed(context.Background(), feedID); !errors.Is(err, ErrSyncRunning) {
		t.Errorf("SyncFeed during a sync = %v, want ErrSyncRunning", err)
	}

	close(srv.release)
	// Wait for the background sync without cancelling it, as Stop would.
	s.wg.Wait()

	if len(db.syncs) != 1 || !db.syncs[0].Success {
		t.Errorf("recorded syncs = %+v, want the background sync", db.syncs)
	}
}
