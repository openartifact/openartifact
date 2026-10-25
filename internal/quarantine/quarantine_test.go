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

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
)

type quarantineDB struct {
	databasetest.BaseMockDB

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

func enforce(db database.Database, repoID string, keys ...string) *httptest.ResponseRecorder {
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

func TestEnforceIgnoresDatabasesWithoutSupportAndEmptyKeys(t *testing.T) {
	if rw := enforce(&databasetest.BaseMockDB{}, "r1", "a"); rw.Code != 0 {
		t.Errorf("database without quarantine support must not block (status %d)", rw.Code)
	}

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
