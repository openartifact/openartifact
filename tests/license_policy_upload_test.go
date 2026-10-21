// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

// blockAllowListPolicy is a block policy that only admits MIT.
func blockAllowListPolicy() policytest.Case {
	return policytest.Case{
		Name:     "block-allow-list",
		Policies: []*models.LicensePolicy{policytest.Policy(licensepolicy.ActionBlock, []string{"MIT"}, nil)},
		Want:     policytest.Blocked,
	}
}

// buildLicensedNPMPublishBody is buildPublishBody with extra manifest fields
// (license / licenses) merged into the published version manifest.
func buildLicensedNPMPublishBody(t *testing.T, packageName, version string, extra map[string]any) []byte {
	t.Helper()

	tarball := buildTarballBytes(t, map[string]any{"name": packageName, "version": version})

	integrity, sha1Hex, _, _, err := pkgnpm.ComputeIntegrity(bytes.NewReader(tarball))
	if err != nil {
		t.Fatalf("ComputeIntegrity: %v", err)
	}

	tarballName := packageName + "-" + version + ".tgz"

	manifest := map[string]any{
		"name":    packageName,
		"version": version,
		"dist": map[string]any{
			"integrity": integrity,
			"shasum":    sha1Hex,
			"tarball":   "http://example.com/" + tarballName,
		},
	}
	maps.Copy(manifest, extra)

	manifestRaw, _ := json.Marshal(manifest)

	raw, _ := json.Marshal(map[string]any{
		"name":      packageName,
		"versions":  map[string]any{version: json.RawMessage(manifestRaw)},
		"dist-tags": map[string]string{"latest": version},
		"_attachments": map[string]any{
			tarballName: map[string]any{
				"content_type": "application/octet-stream",
				"data":         base64.StdEncoding.EncodeToString(tarball),
				"length":       len(tarball),
			},
		},
	})

	return raw
}

func publishNPM(t *testing.T, router chi.Router, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/repos/myrepo/lodash", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestNPMPublish_LicensePolicy(t *testing.T) {
	manifests := map[string]map[string]any{
		"license-string":      {"license": "GPL-3.0"},
		"legacy-license-obj":  {"license": map[string]any{"type": "GPL-3.0", "url": "https://x"}},
		"legacy-licenses-arr": {"licenses": []any{map[string]any{"type": "GPL-3.0"}}},
	}

	for mname, extra := range manifests {
		for _, c := range policytest.Cases("GPL-3.0") {
			t.Run(mname+"/"+c.Name, func(t *testing.T) {
				db := newMockDatabase()
				bs := newMockBlobStore()
				db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}

				router := newLicensedNPMRouter(c.DB(db), bs)
				rec := publishNPM(t, router, buildLicensedNPMPublishBody(t, "lodash", "1.0.0", extra))

				c.AssertResponse(t, rec.Code, rec.Body.Bytes())

				if c.Stopped() {
					if len(bs.blobs) != 0 || len(db.npmVersions) != 0 || len(db.npmPackages) != 0 {
						t.Errorf("rejected publish stored data: blobs=%d versions=%d packages=%d", len(bs.blobs), len(db.npmVersions), len(db.npmPackages))
					}

					return
				}

				if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
					t.Fatalf("admitted publish status = %d: %s", rec.Code, rec.Body.String())
				}

				if len(bs.blobs) == 0 || len(db.npmVersions) != 1 {
					t.Errorf("admitted publish not stored: blobs=%d versions=%d", len(bs.blobs), len(db.npmVersions))
				}
			})
		}
	}
}

func TestNPMPublish_NoLicenseAdmittedUnderBlockPolicy(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}

	router := newLicensedNPMRouter(blockAllowListPolicy().DB(db), bs)

	rec := publishNPM(t, router, buildPublishBody(t, "lodash", "1.0.0"))
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	if len(db.npmVersions) != 1 {
		t.Errorf("unlicensed package not stored")
	}
}

// newLicensedNPMRouter serves the npm upload handler over the case's policy store.
func newLicensedNPMRouter(db database.Database, bs *MockBlobStore) chi.Router {
	log, _ := logger.New("info", "json", "stdout")
	h := api.NewNPMUploadHandler(log, db, bs, "http://localhost:8080", nil)
	router := chi.NewRouter()
	router.Use(authenticateTestBearer)
	h.Register(router)

	return router
}

func uploadRPMWithLicense(t *testing.T, db database.Database, bs *MockBlobStore, pub *recordingPublisher, license string) *httptest.ResponseRecorder {
	t.Helper()

	router := newRPMUploadRouter(t, db, bs, pub)

	return uploadRPM(t, router, buildMinimalRPMWithLicense(license), "lic-repo")
}

func TestRPMUpload_LicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPLv3+") {
		t.Run(c.Name, func(t *testing.T) {
			db := newMockDatabase()
			bs := newMockBlobStore()
			seedTestRepo(db, "repo-lic", "lic-repo")

			pub := &recordingPublisher{}
			rec := uploadRPMWithLicense(t, c.DB(db), bs, pub, "GPLv3+")

			c.AssertResponse(t, rec.Code, rec.Body.Bytes())

			if c.Stopped() {
				if len(db.components) != 0 || len(bs.blobs) != 0 || len(pub.snapshot()) != 0 {
					t.Errorf("rejected upload stored data: components=%d blobs=%d events=%d", len(db.components), len(bs.blobs), len(pub.snapshot()))
				}

				return
			}

			if rec.Code != http.StatusOK {
				t.Fatalf("admitted upload status = %d: %s", rec.Code, rec.Body.String())
			}

			if len(db.components) != 1 || len(bs.blobs) != 1 || len(pub.snapshot()) != 1 {
				t.Errorf("admitted upload not stored: components=%d blobs=%d events=%d", len(db.components), len(bs.blobs), len(pub.snapshot()))
			}
		})
	}
}

func TestRPMUpload_NoLicenseAdmittedUnderBlockPolicy(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	seedTestRepo(db, "repo-lic", "lic-repo")

	rec := uploadRPMWithLicense(t, blockAllowListPolicy().DB(db), bs, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	if len(db.components) != 1 {
		t.Errorf("unlicensed package not stored")
	}
}

func mavenPom(license string) []byte {
	lic := ""
	if license != "" {
		lic = "<licenses><license><name>" + license + "</name></license></licenses>"
	}

	return []byte(`<?xml version="1.0"?><project><modelVersion>4.0.0</modelVersion>` +
		`<groupId>com.example</groupId><artifactId>myapp</artifactId><version>1.0.0</version>` + lic + `</project>`)
}

func newLicensedMavenRouter(t *testing.T, db database.Database, pub *mavenRecordingPublisher) chi.Router {
	t.Helper()

	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, newMockBlobStore(), nil)
	h.SetPublisher(pub)

	router := chi.NewRouter()
	h.Register(router)

	return router
}

func TestMavenUpload_LicensePolicy(t *testing.T) {
	const base = "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0"

	for _, c := range policytest.Cases("GPL-3.0-only") {
		t.Run(c.Name, func(t *testing.T) {
			mock := newMockDatabase()
			mock.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

			pub := &mavenRecordingPublisher{}
			router := newLicensedMavenRouter(t, c.DB(mock), pub)

			// The jar goes up first and is not license-bearing.
			if code := mavenDo(router, http.MethodPut, base+".jar", []byte("jar-bytes")); code != http.StatusCreated {
				t.Fatalf("jar PUT = %d, want 201", code)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, base+".pom", bytes.NewReader(mavenPom("GPL-3.0-only")))
			router.ServeHTTP(rec, req)

			c.AssertResponse(t, rec.Code, rec.Body.Bytes())

			if c.Stopped() {
				if len(mock.mavenArtifacts) != 1 || len(pub.events) != 1 {
					t.Errorf("rejected pom stored data: artifacts=%d events=%d", len(mock.mavenArtifacts), len(pub.events))
				}

				return
			}

			if rec.Code != http.StatusCreated {
				t.Fatalf("admitted pom status = %d: %s", rec.Code, rec.Body.String())
			}

			if len(mock.mavenArtifacts) != 2 || len(pub.events) != 2 {
				t.Errorf("admitted pom not stored: artifacts=%d events=%d", len(mock.mavenArtifacts), len(pub.events))
			}
		})
	}
}

func TestMavenUpload_NoLicenseAdmittedUnderBlockPolicy(t *testing.T) {
	mock := newMockDatabase()
	mock.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

	router := newLicensedMavenRouter(t, blockAllowListPolicy().DB(mock), &mavenRecordingPublisher{})

	code := mavenDo(router, http.MethodPut, "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.pom", mavenPom(""))
	if code != http.StatusCreated {
		t.Fatalf("pom PUT = %d, want 201", code)
	}
}
