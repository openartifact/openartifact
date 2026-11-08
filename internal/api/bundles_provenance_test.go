// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/attestation"
	"github.com/openartifact/openartifact/internal/database/models"
)

// itemsDB serves the items of a bundle; every other BundlesStore method would panic.
type itemsDB struct {
	BundlesStore

	items []*models.ReleaseBundleItem
}

func (d itemsDB) ListBundleItems(context.Context, string) ([]*models.ReleaseBundleItem, error) {
	return d.items, nil
}

// gateStub answers CheckProvenance from a set of digests that lack provenance.
type gateStub struct {
	missing map[string]bool
	err     error
	asked   []string
}

func (g *gateStub) CheckProvenance(_ context.Context, _, _, digest string) error {
	g.asked = append(g.asked, digest)

	if g.err != nil {
		return g.err
	}

	if g.missing[digest] {
		return attestation.ErrProvenanceRequired
	}

	return nil
}

func TestProvenanceFailure(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	items := []*models.ReleaseBundleItem{
		{SourceRepoID: "src", SHA256: strings.Repeat("c", 64), Format: "maven"},
		{SourceRepoID: "src", SHA256: a, Format: "docker"},
		{SourceRepoID: "src", SHA256: b, Format: "docker"},
	}

	tests := []struct {
		name       string
		gate       *gateStub
		target     string
		wantStatus int
		wantCode   string
		wantInMsg  string
		wantAsked  []string
	}{
		{"every image has provenance", &gateStub{}, "docker", 0, "", "", []string{"sha256:" + a, "sha256:" + b}},
		{
			"an image lacks provenance", &gateStub{missing: map[string]bool{"sha256:" + b: true}}, "docker",
			http.StatusUnprocessableEntity, "PROVENANCE_REQUIRED", b, []string{"sha256:" + a, "sha256:" + b},
		},
		{
			"the check fails", &gateStub{err: errors.New("db down")}, "docker",
			http.StatusInternalServerError, "internal_error", "", []string{"sha256:" + a},
		},
		{"a target that is not docker is not checked", &gateStub{missing: map[string]bool{"sha256:" + a: true}}, "maven", 0, "", "", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &BundlesHandler{db: itemsDB{items: items}, provenance: tc.gate}
			h.log = newServerTestLogger(t)

			fail := h.provenanceFailure(
				context.Background(),
				&models.ReleaseBundle{ID: "bundle"},
				&models.Repository{ID: "dst", Format: tc.target},
			)

			switch {
			case tc.wantStatus == 0 && fail != nil:
				t.Fatalf("failure = %+v, want none", fail)
			case tc.wantStatus != 0 && fail == nil:
				t.Fatalf("no failure, want %d %s", tc.wantStatus, tc.wantCode)
			case fail != nil && (fail.status != tc.wantStatus || fail.code != tc.wantCode || !strings.Contains(fail.msg, tc.wantInMsg)):
				t.Fatalf("failure = %+v, want %d %s containing %q", fail, tc.wantStatus, tc.wantCode, tc.wantInMsg)
			}

			if strings.Join(tc.gate.asked, ",") != strings.Join(tc.wantAsked, ",") {
				t.Fatalf("checked %v, want %v", tc.gate.asked, tc.wantAsked)
			}
		})
	}
}

func TestProvenanceFailureWithoutGate(t *testing.T) {
	h := &BundlesHandler{db: itemsDB{}}

	if fail := h.provenanceFailure(context.Background(), &models.ReleaseBundle{}, &models.Repository{Format: "docker"}); fail != nil {
		t.Fatalf("failure = %+v, want none when no attestation service is wired", fail)
	}
}
