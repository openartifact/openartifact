// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
)

// The group grp of the curation tests serves hosted, then px.
func (db *curationMockDB) ListGroupMemberRepositories(context.Context, string) ([]*models.Repository, error) {
	return []*models.Repository{db.repos["hosted"], db.repos["px"]}, nil
}

func (db *curationMockDB) HostedNamesHeld(context.Context, []string, []string) ([]models.HeldName, error) {
	return nil, nil
}

func (db *curationMockDB) HostedVersionsStored(context.Context, []string, []models.NameVersion) ([]models.StoredVersion, error) {
	return nil, nil
}

const batchPath = "/api/v1/repositories/grp/availability"

// TestAvailabilityBatchEndpoint: the POST answers one result per package version
// in order, through the group's members.
func TestAvailabilityBatchEndpoint(t *testing.T) {
	db := newCurationDB()
	cache := &batchCache{versions: map[string]map[string][]proxy.DatedVersion{
		"r-px": {"a": {{Version: "1.0.0", Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}},
	}}

	rw := curationRequest(t, db, cache, http.MethodPost, batchPath,
		`{"packages":[{"package":"a","version":"1.0.0"},{"package":"a","version":"2.0.0"},{"package":"b","version":"1.0.0"}]}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rw.Code, rw.Body.String())
	}

	resp := decodeResp[responses.AvailabilityBatchResponse](t, rw)

	got := make([]string, len(resp.Results))
	for i, r := range resp.Results {
		got[i] = r.Package + "@" + r.Version + " " + r.Status + " " + r.Repository
	}

	want := "a@1.0.0 available px; a@2.0.0 not_found grp; b@1.0.0 unknown px"
	if resp.Repository != "grp" || strings.Join(got, "; ") != want {
		t.Errorf("answer %s: %v, want %s", resp.Repository, got, want)
	}
}

// TestAvailabilityBatchEndpointValidation: a body out of bounds is 400 before
// anything is looked up.
func TestAvailabilityBatchEndpointValidation(t *testing.T) {
	many := make([]responses.AvailabilityQuery, MaxAvailabilityBatch+1)
	for i := range many {
		many[i] = responses.AvailabilityQuery{Package: "a", Version: "1"}
	}

	tooMany, err := json.Marshal(responses.AvailabilityBatchRequest{Packages: many})
	if err != nil {
		t.Fatal(err)
	}

	for name, body := range map[string]string{
		"not JSON":    "{",
		"no packages": `{}`,
		"empty list":  `{"packages":[]}`,
		"no version":  `{"packages":[{"package":"a"}]}`,
		"too long":    `{"packages":[{"package":"a","version":"` + strings.Repeat("1", MaxAvailabilityParam+1) + `"}]}`,
		"too many":    string(tooMany),
	} {
		if rw := curationRequest(t, newCurationDB(), &batchCache{}, http.MethodPost, batchPath, body); rw.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, rw.Code, rw.Body.String())
		}
	}
}
