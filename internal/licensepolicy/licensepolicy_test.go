// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensepolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errBackend = errors.New("backend down")

// memStore is an in-memory licensepolicy.Store.
type memStore struct {
	policies  []*models.LicensePolicy
	listErr   error
	listCalls int
	results   map[string]*models.LicenseScanResult
	createErr error
}

func newMemStore(policies ...*models.LicensePolicy) *memStore {
	return &memStore{policies: policies, results: map[string]*models.LicenseScanResult{}}
}

func (s *memStore) ListLicensePolicies(context.Context, string) ([]*models.LicensePolicy, error) {
	s.listCalls++

	return s.policies, s.listErr
}

func (s *memStore) GetLicenseScanResult(_ context.Context, _, artifactID string) (*models.LicenseScanResult, error) {
	return s.results[artifactID], nil
}

func (s *memStore) CreateLicenseScanResult(_ context.Context, r *models.LicenseScanResult) error {
	if s.createErr != nil {
		return s.createErr
	}

	cp := *r
	s.results[r.ArtifactID] = &cp

	return nil
}

func testLog() *logger.Logger {
	log, _ := logger.New("error", "json", "stderr")

	return log
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		policy   *models.LicensePolicy
		licenses []string
		want     []string // actions of the violations
	}{
		{"denied license under block", policytest.Policy("block", nil, []string{"GPL-3.0"}), []string{"GPL-3.0"}, []string{"block"}},
		{"denied license under warn", policytest.Policy("warn", nil, []string{"GPL-3.0"}), []string{"GPL-3.0"}, []string{"warn"}},
		{"denied license under allow", policytest.Policy("allow", nil, []string{"GPL-3.0"}), []string{"GPL-3.0"}, []string{"allow"}},
		{"license outside the allow list", policytest.Policy("block", []string{"MIT"}, nil), []string{"GPL-3.0"}, []string{"block"}},
		{"license inside the allow list", policytest.Policy("block", []string{"MIT"}, nil), []string{"MIT"}, nil},
		{"other license is not denied", policytest.Policy("block", nil, []string{"GPL-3.0"}), []string{"MIT"}, nil},
		{"-only spelling of a denied family", policytest.Policy("block", nil, []string{"GPL-3.0"}), []string{"GPL-3.0-only"}, []string{"block"}},
		{"-or-later spelling of a denied family", policytest.Policy("block", nil, []string{"GPL-3.0"}), []string{"GPL-3.0-or-later"}, []string{"block"}},
		{"plus spelling of a denied family", policytest.Policy("block", nil, []string{"GPL-3.0-only"}), []string{"GPL-3.0+"}, []string{"block"}},
		{"legacy rpm spelling of a denied family", policytest.Policy("block", nil, []string{"GPL-3.0"}), []string{"GPLv3+"}, []string{"block"}},
		{"expression containing a denied license", policytest.Policy("block", nil, []string{"GPL-3.0"}), []string{"MIT OR GPL-3.0"}, []string{"block"}},
		{"nothing declared never violates", policytest.Policy("block", []string{"MIT"}, []string{"GPL-3.0"}), nil, nil},
		{"blank entries are not licenses", policytest.Policy("block", []string{"MIT"}, nil), []string{"", "  "}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore(tt.policy)

			got, err := licensepolicy.Evaluate(context.Background(), store, testLog(), "repo-1", tt.licenses)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}

			actions := make([]string, 0, len(got))
			for _, v := range got {
				actions = append(actions, v.Action)
			}

			if !slices.Equal(actions, tt.want) {
				t.Errorf("violation actions = %v, want %v", actions, tt.want)
			}

			if len(tt.licenses) == 0 && store.listCalls != 0 {
				t.Error("the policy store must not be read when no license is declared")
			}
		})
	}
}

func TestEvaluate_DisabledPolicyIsIgnored(t *testing.T) {
	p := policytest.Policy("block", nil, []string{"GPL-3.0"})
	p.Enabled = false

	got, err := licensepolicy.Evaluate(context.Background(), newMemStore(p), testLog(), "repo-1", []string{"GPL-3.0"})
	if err != nil || len(got) != 0 {
		t.Fatalf("violations=%v err=%v, want none", got, err)
	}
}

func TestEnforce(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/upload", nil)

			stopped := licensepolicy.Enforce(rec, req, c.DB(nil), testLog(), "repo-1", "GPL-3.0")

			if stopped != c.Stopped() {
				t.Fatalf("Enforce returned %v, want %v", stopped, c.Stopped())
			}

			if !stopped {
				if rec.Body.Len() != 0 || rec.Code != http.StatusOK {
					t.Fatalf("an admitted upload must not get a response from Enforce, got %d %q", rec.Code, rec.Body.String())
				}

				return
			}

			c.AssertResponse(t, rec.Code, rec.Body.Bytes())

			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}

func TestEnforce_BlockedBody(t *testing.T) {
	store := newMemStore(policytest.Policy("block", nil, []string{"GPL-3.0"}))
	store.policies[0].Name = "no copyleft"

	rec := httptest.NewRecorder()

	if !licensepolicy.Enforce(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", nil), store, testLog(), "repo-1", " GPLv3 ", "GPL-3.0-only", "MIT") {
		t.Fatal("upload must be stopped")
	}

	var body struct {
		Error    string   `json:"error"`
		Code     string   `json:"code"`
		Licenses []string `json:"licenses"`
		Policies []struct {
			PolicyName string `json:"policy_name"`
			Action     string `json:"action"`
			Violation  string `json:"violation"`
		} `json:"policies"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if rec.Code != http.StatusForbidden || body.Code != "LICENSE_BLOCKED" || body.Error == "" {
		t.Fatalf("got %d %+v", rec.Code, body)
	}

	if len(body.Policies) != 1 || body.Policies[0].PolicyName != "no copyleft" || body.Policies[0].Action != "block" || body.Policies[0].Violation == "" {
		t.Errorf("policies = %+v", body.Policies)
	}

	// Aliases collapse to one entry: the client sees each license once.
	if len(body.Licenses) != 2 {
		t.Errorf("licenses = %v, want the two distinct licenses (GPL-3.0 once, MIT)", body.Licenses)
	}
}

func TestEnforce_NoLicenseSkipsPolicyStore(t *testing.T) {
	store := newMemStore(policytest.Policy("block", []string{"MIT"}, nil))
	store.listErr = errBackend // would fail closed if it were read

	rec := httptest.NewRecorder()

	if licensepolicy.Enforce(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", nil), store, testLog(), "repo-1") {
		t.Fatal("a package that declares no license is not stopped by a license policy")
	}

	if store.listCalls != 0 {
		t.Error("policy store must not be read")
	}
}

func TestRecord_Modes(t *testing.T) {
	tests := []struct {
		name            string
		policy          *models.LicensePolicy
		wantQuarantined bool
		wantViolations  int
	}{
		{"allow action", policytest.Policy("allow", nil, []string{"GPL-3.0"}), false, 1},
		{"warn action", policytest.Policy("warn", nil, []string{"GPL-3.0"}), false, 1},
		{"block action denied license", policytest.Policy("block", nil, []string{"GPL-3.0"}), true, 1},
		{"block action allow list", policytest.Policy("block", []string{"MIT"}, nil), true, 1},
		{"block action but license fine", policytest.Policy("block", nil, []string{"AGPL-3.0"}), false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore(tt.policy)

			res, violations, err := licensepolicy.Record(context.Background(), store, testLog(), "repo-1", "pkg@1.0.0", []string{"GPL-3.0"}, models.LicenseSourceArtifact)
			if err != nil {
				t.Fatalf("Record: %v", err)
			}

			if res.Quarantined != tt.wantQuarantined || len(violations) != tt.wantViolations {
				t.Errorf("quarantined=%v violations=%d, want %v/%d", res.Quarantined, len(violations), tt.wantQuarantined, tt.wantViolations)
			}

			stored := store.results["pkg@1.0.0"]
			if stored == nil || stored.Quarantined != tt.wantQuarantined || stored.ScanStatus != "completed" {
				t.Errorf("stored result = %+v", stored)
			}
		})
	}
}

func TestRecord_QuarantineLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(policytest.Policy("block", nil, []string{"GPL-3.0"}))
	record := func(licenses ...string) *models.LicenseScanResult {
		t.Helper()

		res, _, err := licensepolicy.Record(ctx, store, testLog(), "repo-1", "pkg@1.0.0", licenses, models.LicenseSourceArtifact)
		if err != nil {
			t.Fatalf("Record: %v", err)
		}

		return res
	}

	if !record("GPL-3.0").Quarantined {
		t.Fatal("a denied license must quarantine the artifact")
	}

	// A re-scan that finds nothing, or something harmless, is not a way out.
	if !record().Quarantined || !record("MIT").Quarantined {
		t.Fatal("a quarantined artifact stays quarantined until an admin approves it")
	}

	// approve is what the quarantine endpoint does to the stored result.
	approve := func() {
		stored := store.results["pkg@1.0.0"]
		stored.Quarantined = false
		stored.QuarantineApproved = true
	}

	// An admin approves the artifact for the licenses it currently has (MIT).
	approve()

	if res := record("MIT"); res.Quarantined || !res.QuarantineApproved {
		t.Fatalf("a re-scan of the approved licenses must keep the approval, got %+v", res)
	}

	// The approval does not extend to a different, denied license.
	if res := record("GPL-3.0"); !res.Quarantined || res.QuarantineApproved {
		t.Fatalf("new licenses need a new approval, got %+v", res)
	}

	// Approving GPL-3.0 holds for every spelling of it.
	approve()

	for _, spelling := range []string{"GPL-3.0", "GPL-3.0-only", "GPLv3"} {
		if res := record(spelling); res.Quarantined || !res.QuarantineApproved {
			t.Fatalf("approval must hold for the spelling %q, got %+v", spelling, res)
		}
	}
}

func TestRecord_Errors(t *testing.T) {
	t.Run("policy store unreadable", func(t *testing.T) {
		store := newMemStore(policytest.Policy("block", nil, []string{"GPL-3.0"}))
		store.listErr = errBackend

		if _, _, err := licensepolicy.Record(context.Background(), store, testLog(), "repo-1", "a", []string{"GPL-3.0"}, models.LicenseSourceArtifact); err == nil {
			t.Fatal("Record must fail when policies cannot be read")
		}

		if len(store.results) != 0 {
			t.Error("nothing may be stored when the policies could not be evaluated")
		}
	})

	t.Run("result cannot be stored", func(t *testing.T) {
		store := newMemStore()
		store.createErr = errBackend

		if _, _, err := licensepolicy.Record(context.Background(), store, testLog(), "repo-1", "a", []string{"MIT"}, models.LicenseSourceArtifact); err == nil {
			t.Fatal("Record must report a storage failure")
		}
	})
}

func TestRefuseUnderivable(t *testing.T) {
	for _, c := range policytest.UnderivableCases() {
		t.Run(c.Name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/upload", nil)

			stopped := licensepolicy.RefuseUnderivable(rec, req, c.DB(nil), testLog(), "repo-1")

			if stopped != c.Stopped() {
				t.Fatalf("RefuseUnderivable returned %v, want %v", stopped, c.Stopped())
			}

			if !stopped {
				if rec.Body.Len() != 0 {
					t.Fatalf("an admitted upload must not get a response, got %d %q", rec.Code, rec.Body.String())
				}

				return
			}

			c.AssertResponse(t, rec.Code, rec.Body.Bytes())
		})
	}
}

func TestHasBlocking(t *testing.T) {
	blocking, err := licensepolicy.HasBlocking(context.Background(), newMemStore(policytest.Policy("warn", nil, nil), policytest.Policy("block", nil, nil)), "repo-1")
	if err != nil || !blocking {
		t.Fatalf("HasBlocking = %v, %v; want true", blocking, err)
	}

	if _, err := licensepolicy.HasBlocking(context.Background(), &memStore{listErr: errBackend}, "repo-1"); err == nil {
		t.Fatal("a policy store failure must be returned")
	}
}

func TestRecord_StoresTheLicenseSource(t *testing.T) {
	for _, source := range []string{models.LicenseSourceArtifact, models.LicenseSourceClientDeclared} {
		store := newMemStore()

		res, _, err := licensepolicy.Record(context.Background(), store, testLog(), "repo-1", "pkg@1.0.0", []string{"MIT"}, source)
		if err != nil {
			t.Fatalf("Record: %v", err)
		}

		if res.LicenseSource != source || store.results["pkg@1.0.0"].LicenseSource != source {
			t.Errorf("source = %q, stored %q, want %q", res.LicenseSource, store.results["pkg@1.0.0"].LicenseSource, source)
		}
	}
}
