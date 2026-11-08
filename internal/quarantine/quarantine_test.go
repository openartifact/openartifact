// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quarantine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type quarantineDB struct {
	repoID  string
	keys    map[string]bool
	err     error
	lastKey []string
}

func (db *quarantineDB) IsArtifactQuarantined(_ context.Context, repoID string, keys []string) (bool, error) {
	db.lastKey = keys

	if db.err != nil {
		return false, db.err
	}

	for _, k := range keys {
		if repoID == db.repoID && db.keys[k] {
			return true, nil
		}
	}

	return false, nil
}

func enforce(db Checker, repoID string, keys ...string) *httptest.ResponseRecorder {
	rw := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)

	if Enforce(rw, r, db, repoID, keys...) {
		return rw
	}

	rw.Code = 0 // not handled: the caller would serve the artifact

	return rw
}

func TestEnforceBlocksQuarantinedArtifact(t *testing.T) {
	db := &quarantineDB{repoID: "r1", keys: map[string]bool{"bad.tgz": true}}

	rw := enforce(db, "r1", "other", "bad.tgz")
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rw.Code)
	}

	if got := rw.Body.String(); !strings.Contains(got, "ARTIFACT_QUARANTINED") {
		t.Errorf("body = %q, want the ARTIFACT_QUARANTINED code", got)
	}

	if rw := enforce(db, "r1", "good.tgz"); rw.Code != 0 {
		t.Errorf("clean artifact blocked with status %d", rw.Code)
	}

	if rw := enforce(db, "r2", "bad.tgz"); rw.Code != 0 {
		t.Errorf("quarantine of repo r1 leaked into repo r2 (status %d)", rw.Code)
	}
}

func TestEnforceFailsClosedOnDatabaseError(t *testing.T) {
	db := &quarantineDB{repoID: "r1", err: errors.New("db down")}

	if rw := enforce(db, "r1", "a"); rw.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail closed)", rw.Code)
	}
}

// TestEnforceFailsClosedWithoutChecker: a missing checker is a 500 and an
// ErrNoChecker, never "not quarantined" (the check used to be skipped for a
// database without the method).
func TestEnforceFailsClosedWithoutChecker(t *testing.T) {
	if rw := enforce(nil, "r1", "a"); rw.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail closed)", rw.Code)
	}

	if _, err := IsQuarantined(context.Background(), nil, "r1", "a"); !errors.Is(err, ErrNoChecker) {
		t.Fatalf("IsQuarantined(nil) error = %v, want ErrNoChecker", err)
	}
}

func TestIsQuarantinedWrapsDatabaseError(t *testing.T) {
	boom := errors.New("db down")
	db := &quarantineDB{repoID: "r1", err: boom}

	blocked, err := IsQuarantined(context.Background(), db, "r1", "a")
	if blocked || !errors.Is(err, boom) {
		t.Fatalf("IsQuarantined = (%v, %v), want (false, wrapped %v)", blocked, err, boom)
	}
}

// The test fake satisfies the interface every handler takes.
var _ Checker = (*quarantineDB)(nil)

func TestEnforceIgnoresEmptyKeysAndRepoID(t *testing.T) {
	db := &quarantineDB{repoID: "r1", keys: map[string]bool{"": true}}
	if rw := enforce(db, "r1"); rw.Code != 0 {
		t.Errorf("no keys must not block (status %d)", rw.Code)
	}

	if rw := enforce(db, "", "a"); rw.Code != 0 || db.lastKey != nil {
		t.Errorf("empty repo id must short-circuit without a lookup (status %d, keys %v)", rw.Code, db.lastKey)
	}
}

func TestPathKeys(t *testing.T) {
	got := PathKeys("/a/b.tgz", "", "c/d", "/")
	want := []string{"/a/b.tgz", "a/b.tgz", "c/d", "/c/d"}

	if !slices.Equal(got, want) {
		t.Errorf("PathKeys = %v, want %v", got, want)
	}
}

type reasonDB struct {
	quarantineDB

	reason Reason
	err    error
}

func (db *reasonDB) ArtifactHoldReason(context.Context, string, []string) (Reason, error) {
	return db.reason, db.err
}

func TestEnforceWith_TellsWhyTheArtifactIsHeld(t *testing.T) {
	held := quarantineDB{repoID: "r", keys: map[string]bool{"a": true}}

	tests := []struct {
		name   string
		db     Checker
		keys   []string
		called bool
		reason Reason
	}{
		{"not held", &reasonDB{quarantineDB: held, reason: ReasonLicense}, []string{"b"}, false, ""},
		{"license hold", &reasonDB{quarantineDB: held, reason: ReasonLicense}, []string{"a"}, true, ReasonLicense},
		{"scan hold", &reasonDB{quarantineDB: held, reason: ReasonScan}, []string{"a"}, true, ReasonScan},
		{"store without reasons", &held, []string{"a"}, true, ReasonScan},
		{
			"reason lookup fails", &reasonDB{quarantineDB: held, reason: ReasonLicense, err: errors.New("db down")},
			[]string{"a"}, true, ReasonScan,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Reason

			called := false
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			w := httptest.NewRecorder()

			answered := EnforceWith(w, r, tt.db, "r", func(reason Reason) { called, got = true, reason }, tt.keys...)
			if answered != tt.called || called != tt.called || got != tt.reason {
				t.Errorf("answered %v, refuse called %v with %q; want %v with %q", answered, called, got, tt.called, tt.reason)
			}
		})
	}
}
