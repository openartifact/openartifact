// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ivy

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
)

// policyDB is an ivyDB whose license policies are those of a policytest Case.
type policyDB struct {
	*ivyDB

	lister policytest.Lister
}

// ListLicensePolicies answers the case's policies; the double has its own, so the embedded
// Lister would be ambiguous.
func (d policyDB) ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error) {
	return d.lister.ListLicensePolicies(ctx, repoID)
}

const (
	descriptorURL  = "/repos/myrepo/org.example/mod/1.0/ivy-1.0.xml"
	descriptorBody = `<ivy-module version="2.0"><info organisation="org.example" module="mod" revision="1.0"><license name="GPL-3.0" url="https://www.gnu.org/licenses/gpl-3.0"/></info></ivy-module>`
)

func putDescriptor(h *HTTPHandler, body string) (int, []byte) {
	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, descriptorURL, strings.NewReader(body)))
	return w.Code, w.Body.Bytes()
}

func TestUploadLicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			db := &ivyDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}
			h := NewHTTPHandler(formattest.NewLogger(t), policyDB{db, c.Lister()}, bs, "http://localhost")
			h.SetPublisher(pub)

			code, body := putDescriptor(h, descriptorBody)
			c.AssertResponse(t, code, body)

			if c.Want == policytest.Admitted {
				if code != http.StatusCreated || db.art == nil || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
					t.Fatalf("not stored: status=%d art=%v blobs=%d events=%d", code, db.art, len(bs.Blobs), len(pub.Events()))
				}

				return
			}

			if db.art != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
				t.Fatalf("rejected upload stored something: art=%v blobs=%d events=%d", db.art, len(bs.Blobs), len(pub.Events()))
			}
		})
	}
}

func TestUploadWithoutLicenseAdmittedUnderLicensePolicy(t *testing.T) {
	policy := policytest.Policy("block", []string{"MIT"}, nil)
	db := policyDB{&ivyDB{DB: formattest.NewDB()}, policytest.Case{Policies: []*models.LicensePolicy{policy}}.Lister()}
	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "http://localhost")

	// A non-descriptor file and a descriptor without a license are both undeclared.
	for _, tc := range []struct{ url, body string }{
		{descriptorURL, "<ivy-module><info organisation=\"o\" module=\"m\"/></ivy-module>"},
		{"/repos/myrepo/org.example/mod/1.0/mod-1.0.jar", "binary"},
	} {
		w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, tc.url, strings.NewReader(tc.body)))
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d: %s", tc.url, w.Code, w.Body.String())
		}
	}
}
