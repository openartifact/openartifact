// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
)

const (
	descriptorURL  = "/repos/myrepo/org.example/mod/1.0/mod-1.0.pom"
	descriptorBody = `<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion><licenses><license><name>GPL-3.0</name></license></licenses></project>`
)

func putDescriptor(h *HTTPHandler, body string) (int, []byte) {
	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, descriptorURL, strings.NewReader(body)))
	return w.Code, w.Body.Bytes()
}

func TestUploadLicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			db := &sbtDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}
			h := NewHTTPHandler(formattest.NewLogger(t), c.DB(db), bs, "http://localhost")
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
	db := &policytest.DB{Database: &sbtDB{DB: formattest.NewDB()}, Policies: []*models.LicensePolicy{policy}}
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
