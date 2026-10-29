// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/internal/workq"
)

const forwardScanBody = `{"artifact_id":"left-pad@1.0.0","package_name":"left-pad","package_version":"1.0.0","ecosystem":"npm","scanner":"osv"}`

// scanRemoteDB adds the reads and the delete of a scan request to scanPolicyDB.
type scanRemoteDB struct {
	*scanPolicyDB

	stored  *models.VulnScanResult
	deleted []string
}

func (db *scanRemoteDB) GetVulnScanResultByID(context.Context, string, string) (*models.VulnScanResult, error) {
	if db.stored == nil {
		return nil, nil
	}

	cp := *db.stored

	return &cp, nil
}

func (db *scanRemoteDB) DeleteVulnScanResult(_ context.Context, _, id string) error {
	db.deleted = append(db.deleted, id)
	return nil
}

// scanRequestArg matches the payload of the scan request of testScanID.
type scanRequestArg struct{ want vulnScanRequest }

func (m scanRequestArg) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	if !ok {
		return false
	}

	var got vulnScanRequest
	if json.Unmarshal(b, &got) != nil {
		return false
	}

	return got == m.want
}

func newForwardingScanHandler(t *testing.T, db *scanRemoteDB) (*VulnHandler, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	t.Cleanup(func() { _ = sqlDB.Close() })

	h := newScanPolicyVulnHandler(t, db.scanPolicyDB, nil)
	h.db = db

	return h.ForwardScansTo(workq.NewStore(sqlDB)), mock
}

func TestForwardingScanHandlerQueuesTheScanForAWorker(t *testing.T) {
	db := &scanRemoteDB{scanPolicyDB: newScanPolicyDB()}
	h, mock := newForwardingScanHandler(t, db)

	mock.ExpectExec("INSERT INTO worker_requests").
		WithArgs(VulnScanRequestKind, testScanID, scanRequestArg{vulnScanRequest{
			RepoID: testRepoID, ResultID: testScanID,
			Package: vuln.Package{Name: "left-pad", Version: "1.0.0", Ecosystem: "npm"},
		}}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

	rw := serveAs(register, http.MethodPost, "/api/v1/repositories/repo/vuln/scan", forwardScanBody)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	if len(db.created) != 1 || db.created[0].ScanStatus != "scanning" {
		t.Fatalf("created = %+v, want one scan in the scanning state", db.created)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-db.updated:
		t.Fatalf("a forwarding handler ran the scan itself: %+v", r)
	default:
	}

	if len(h.scanSem) != 0 {
		t.Fatal("a forwarding handler reserved a scan slot")
	}
}

func TestForwardingScanHandlerRemovesTheScanItCouldNotQueue(t *testing.T) {
	db := &scanRemoteDB{scanPolicyDB: newScanPolicyDB()}
	h, mock := newForwardingScanHandler(t, db)

	mock.ExpectExec("INSERT INTO worker_requests").WillReturnError(errors.New("connection refused"))

	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

	rw := serveAs(register, http.MethodPost, "/api/v1/repositories/repo/vuln/scan", forwardScanBody)
	if rw.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rw.Code)
	}

	if len(db.deleted) != 1 || db.deleted[0] != testScanID {
		t.Fatalf("deleted = %v, want the scan row that nobody will run", db.deleted)
	}
}

func scanRequestFor(t *testing.T) workq.Request {
	t.Helper()

	payload, err := json.Marshal(vulnScanRequest{
		RepoID: testRepoID, ResultID: testScanID,
		Package: vuln.Package{Name: "left-pad", Version: "1.0.0", Ecosystem: "npm"},
	})
	if err != nil {
		t.Fatal(err)
	}

	return workq.Request{Kind: VulnScanRequestKind, Key: testScanID, Payload: payload}
}

func newScanningResult() *models.VulnScanResult {
	return &models.VulnScanResult{
		ID: testScanID, RepositoryID: testRepoID, ArtifactID: "left-pad@1.0.0",
		PackageName: "left-pad", PackageVersion: "1.0.0", Ecosystem: "npm",
		Scanner: "osv", ScanStatus: "scanning",
	}
}

func TestHandleScanRequestRunsTheScanAndSettlesIt(t *testing.T) {
	db := &scanRemoteDB{scanPolicyDB: newScanPolicyDB(), stored: newScanningResult()}
	h := newScanPolicyVulnHandler(t, db.scanPolicyDB, map[string]vuln.ScannerBackend{
		"osv": stubScanner{name: "osv", vulns: []models.Vulnerability{{ID: "GHSA-1", Severity: "HIGH"}}},
	})
	h.db = db

	settled := make(chan bool, 1)

	res := h.handleScanRequest(context.Background(), scanRequestFor(t), func(done bool) { settled <- done })
	if res != workq.Later {
		t.Fatalf("result = %v, want Later: the scan outlives the handler", res)
	}

	stored := waitVulnUpdate(t, db.scanPolicyDB)
	if stored.ScanStatus != "completed" || stored.VulnCount != 1 {
		t.Fatalf("stored status=%q vulns=%d, want completed/1", stored.ScanStatus, stored.VulnCount)
	}

	if done := <-settled; !done {
		t.Fatal("a finished scan must settle as done")
	}
}

func TestHandleScanRequestSkipsAScanThatIsNotPendingAnymore(t *testing.T) {
	for name, stored := range map[string]*models.VulnScanResult{
		"deleted meanwhile":    nil,
		"already scanned":      {ID: testScanID, ScanStatus: "completed"},
		"failed by an earlier": {ID: testScanID, ScanStatus: "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			db := &scanRemoteDB{scanPolicyDB: newScanPolicyDB(), stored: stored}
			h := newScanPolicyVulnHandler(t, db.scanPolicyDB, nil)
			h.db = db

			res := h.handleScanRequest(context.Background(), scanRequestFor(t), func(bool) { t.Fatal("nothing was started") })
			if res != workq.Done {
				t.Fatalf("result = %v, want Done", res)
			}

			if len(h.scanSem) != 0 {
				t.Fatal("the scan slot was not released")
			}
		})
	}
}

func TestHandleScanRequestRetriesWhenEverySlotIsTaken(t *testing.T) {
	db := &scanRemoteDB{scanPolicyDB: newScanPolicyDB(), stored: newScanningResult()}
	h := newScanPolicyVulnHandler(t, db.scanPolicyDB, nil)
	h.db = db

	for range maxConcurrentVulnScans {
		h.scanSem <- struct{}{}
	}

	if res := h.handleScanRequest(context.Background(), scanRequestFor(t), func(bool) {}); res != workq.Retry {
		t.Fatalf("result = %v, want Retry", res)
	}
}

func TestHandleScanRequestDropsAMalformedPayload(t *testing.T) {
	db := &scanRemoteDB{scanPolicyDB: newScanPolicyDB()}
	h := newScanPolicyVulnHandler(t, db.scanPolicyDB, nil)

	if res := h.handleScanRequest(
		context.Background(),
		workq.Request{Key: "x", Payload: []byte("nope")},
		func(bool) {},
	); res != workq.Done {
		t.Fatalf("result = %v, want Done", res)
	}
}
