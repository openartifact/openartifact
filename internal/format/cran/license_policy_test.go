// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"context"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
)

// policyDB is a cranDB whose license policies are those of a policytest Case.
type policyDB struct {
	*cranDB

	lister policytest.Lister
}

func (d policyDB) ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error) {
	return d.lister.ListLicensePolicies(ctx, repoID)
}

func uploadLicensed(t *testing.T, db policyDB, license string) (int, []byte) {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost")
	body := sourcePackage(t, "mypkg", "Package: mypkg\nVersion: 1.0\nTitle: T\nDescription: D\nLicense: "+license+"\n")
	w := put(h, "src/contrib/mypkg_1.0.tar.gz", body)

	return w.Code, w.Body.Bytes()
}

func TestUploadLicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			db := newCRANDB()

			code, body := uploadLicensed(t, policyDB{db, c.Lister()}, "GPL-3.0")
			c.AssertResponse(t, code, body)

			if c.Want == policytest.Admitted {
				if code != http.StatusCreated || len(db.files) != 1 {
					t.Fatalf("status = %d, files = %d: %s", code, len(db.files), body)
				}

				return
			}

			if len(db.files) != 0 || db.used != 0 {
				t.Fatalf("rejected upload stored something: files=%d used=%d", len(db.files), db.used)
			}
		})
	}
}

// R licenses are alternatives separated by "|" with an optional "+ file
// LICENSE": each alternative is judged, not the whole string.
func TestUploadJudgesEachLicenseAlternative(t *testing.T) {
	policy := policytest.Policy("block", nil, []string{"GPL-3.0"})
	lister := policytest.Case{Policies: []*models.LicensePolicy{policy}}.Lister()

	for license, want := range map[string]int{
		"GPL-3.0":                           http.StatusForbidden,
		"MIT + file LICENSE":                http.StatusCreated,
		"MIT + file LICENSE | GPL-3.0":      http.StatusForbidden,
		"Apache License (>= 2.0)":           http.StatusCreated,
		"Unlimited":                         http.StatusCreated,
		"file LICENSE":                      http.StatusCreated,
		"MIT + file LICENSE | BSD_3_clause": http.StatusCreated,
	} {
		if code, body := uploadLicensed(t, policyDB{newCRANDB(), lister}, license); code != want {
			t.Errorf("License %q: status = %d, want %d: %s", license, code, want, body)
		}
	}
}
