// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
)

const (
	annotatedManifest = `{"schemaVersion":2,"mediaType":"` + ociManifestType +
		`","annotations":{"org.opencontainers.image.licenses":"GPL-3.0-only"}}`
	plainManifest = `{"schemaVersion":2,"mediaType":"` + ociManifestType + `"}`
)

// assertManifestStored checks whether the manifest body was persisted (row, tag
// and manifest blob).
func assertManifestStored(t *testing.T, g *registry, db *regFakeDB, body string, want bool) {
	t.Helper()

	exists, err := g.h.blobStore.Exists(context.Background(), manifestBlobKey(digestOf([]byte(body))))
	if err != nil {
		t.Fatalf("exists: %v", err)
	}

	if exists != want || (len(db.manifests) == 1) != want || (len(db.tags) == 1) != want {
		t.Fatalf("stored=%v manifests=%d tags=%d, want stored=%v", exists, len(db.manifests), len(db.tags), want)
	}
}

func TestPutManifestLicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0-only") {
		t.Run(c.Name, func(t *testing.T) {
			db := newRegFakeDB("reg")
			g := newRegistry(t, c.DB(db))

			rec := g.putManifest("reg/app", "1", annotatedManifest)
			c.AssertResponse(t, rec.Code, rec.Body.Bytes())

			if c.Want == policytest.Admitted && rec.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}

			assertManifestStored(t, g, db, annotatedManifest, c.Want == policytest.Admitted)
		})
	}
}

func TestPutManifestConfigLabelLicensePolicy(t *testing.T) {
	cfg := []byte(`{"config":{"Labels":{"org.opencontainers.image.licenses":"GPL-3.0"}}}`)
	manifest := `{"schemaVersion":2,"mediaType":"` + ociManifestType +
		`","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + digestOf(cfg) + `","size":70}}`

	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			db := newRegFakeDB("reg")
			g := newRegistry(t, c.DB(db))

			if rec := g.pushBlob("reg/app", cfg); rec.Code != http.StatusCreated {
				t.Fatalf("push config status = %d: %s", rec.Code, rec.Body.String())
			}

			rec := g.putManifest("reg/app", "1", manifest)
			c.AssertResponse(t, rec.Code, rec.Body.Bytes())

			if c.Want == policytest.Admitted && rec.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}

			assertManifestStored(t, g, db, manifest, c.Want == policytest.Admitted)
		})
	}
}

func TestPutManifestWithoutLicenseAdmittedUnderLicensePolicy(t *testing.T) {
	db := newRegFakeDB("reg")
	policy := policytest.Policy("block", []string{"MIT"}, nil)
	g := newRegistry(t, &policytest.DB{Database: db, Policies: []*models.LicensePolicy{policy}})

	if rec := g.putManifest("reg/app", "1", plainManifest); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBlobUploadNotSubjectToLicensePolicy(t *testing.T) {
	db := newRegFakeDB("reg")
	policy := policytest.Policy("block", nil, []string{"GPL-3.0"})
	g := newRegistry(t, &policytest.DB{Database: db, Policies: []*models.LicensePolicy{policy}})

	if rec := g.pushBlob("reg/app", []byte(`{"config":{"Labels":{"org.opencontainers.image.licenses":"GPL-3.0"}}}`)); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}
