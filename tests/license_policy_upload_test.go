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
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
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

	return buildNPMPublishBodyWithTarball(t, packageName, version, buildTarballBytes(t, map[string]any{"name": packageName, "version": version}), extra)
}

// buildNPMPublishBodyWithTarball publishes the given tarball with extra merged
// into the publish JSON's version manifest, the way a client controls it.
func buildNPMPublishBodyWithTarball(t *testing.T, packageName, version string, tarball []byte, extra map[string]any) []byte {
	t.Helper()

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

// The publish JSON's manifest is client-supplied: policies must judge the
// package.json inside the tarball.
func TestNPMPublish_LicensePolicyReadsTheTarball(t *testing.T) {
	deny := []*models.LicensePolicy{policytest.Policy(licensepolicy.ActionBlock, nil, []string{"GPL-3.0-only"})}

	tests := []struct {
		name     string
		tarball  map[string]any
		manifest map[string]any
		want     policytest.Outcome
	}{
		{"manifest has no license", map[string]any{"license": "GPL-3.0-only"}, nil, policytest.Blocked},
		{"manifest claims MIT, tarball is GPL", map[string]any{"license": "GPL-3.0-only"}, map[string]any{"license": "MIT"}, policytest.Blocked},
		{"manifest claims GPL, tarball is MIT", map[string]any{"license": "MIT"}, map[string]any{"license": "GPL-3.0-only"}, policytest.Admitted},
		{"legacy licenses array in the tarball", map[string]any{"licenses": []any{map[string]any{"type": "GPL-3.0-only"}}}, nil, policytest.Blocked},
		{"tarball declares nothing: the manifest is the fallback", nil, map[string]any{"license": "GPL-3.0-only"}, policytest.Blocked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := policytest.Case{Want: tc.want, Policies: deny}
			db := newMockDatabase()
			bs := newMockBlobStore()
			db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}

			pkgJSON := map[string]any{"name": "lodash", "version": "1.0.0"}
			maps.Copy(pkgJSON, tc.tarball)

			tarball := buildTarballBytes(t, pkgJSON)
			rec := publishNPM(t, newLicensedNPMRouter(c.DB(db), bs), buildNPMPublishBodyWithTarball(t, "lodash", "1.0.0", tarball, tc.manifest))

			c.AssertResponse(t, rec.Code, rec.Body.Bytes())

			if c.Stopped() && (len(bs.blobs) != 0 || len(db.npmVersions) != 0 || len(db.npmPackages) != 0) {
				t.Errorf("rejected publish stored data: blobs=%d versions=%d packages=%d", len(bs.blobs), len(db.npmVersions), len(db.npmPackages))
			}

			if !c.Stopped() && len(db.npmVersions) != 1 {
				t.Errorf("admitted publish not stored")
			}
		})
	}
}

func TestNPMPublish_RejectsUnsafeTarballs(t *testing.T) {
	tarballs := map[string][]byte{
		"two package.json files": archivetest.TarGz(t,
			archivetest.File("package/package.json", `{"name":"lodash","version":"1.0.0","license":"MIT"}`),
			archivetest.File("evil/package.json", `{"name":"lodash","version":"1.0.0","license":"GPL-3.0-only"}`)),
		"oversized package.json": archivetest.TarGz(t,
			archivetest.File("package/package.json", `{"name":"lodash","version":"1.0.0","license":"MIT","x":"`+strings.Repeat("A", 2<<20)+`"}`)),
	}

	for name, tarball := range tarballs {
		t.Run(name, func(t *testing.T) {
			db := newMockDatabase()
			bs := newMockBlobStore()
			db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}

			rec := publishNPM(t, newLicensedNPMRouter(db, bs), buildNPMPublishBodyWithTarball(t, "lodash", "1.0.0", tarball, map[string]any{"license": "MIT"}))

			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), licensepolicy.CodeMetadataUnsafe) {
				t.Fatalf("want 422 %s, got %d: %s", licensepolicy.CodeMetadataUnsafe, rec.Code, rec.Body.String())
			}

			if len(bs.blobs) != 0 || len(db.npmVersions) != 0 || len(db.npmPackages) != 0 {
				t.Errorf("rejected publish stored data")
			}
		})
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

// Maven poms name licenses in free text; names that mean exactly one SPDX
// license (or a canonical license URL) are matched by policies.
func TestMavenUpload_LicensePolicyReadsThePOM(t *testing.T) {
	poms := map[string]string{
		"license name": `<licenses><license><name>The Apache Software License, Version 2.0</name></license></licenses>`,
		"license url":  `<licenses><license><url>https://www.apache.org/licenses/LICENSE-2.0.txt</url></license></licenses>`,
	}

	for name, licenses := range poms {
		pom := `<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>myapp</artifactId><version>1.0.0</version>` + licenses + `</project>`

		for _, c := range policytest.Cases("Apache-2.0") {
			t.Run(name+"/"+c.Name, func(t *testing.T) {
				db := newMockDatabase()
				bs := newMockBlobStore()
				db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

				log, _ := logger.New("info", "json", "stdout")
				router := chi.NewRouter()
				api.NewMavenUploadHandler(log, c.DB(db), bs, nil).Register(router)

				req := httptest.NewRequestWithContext(context.Background(), http.MethodPut,
					"/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.pom", strings.NewReader(pom))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				if c.Want == policytest.Admitted {
					if rec.Code != http.StatusCreated {
						t.Fatalf("admitted upload status = %d: %s", rec.Code, rec.Body.String())
					}

					return
				}

				c.AssertResponse(t, rec.Code, rec.Body.Bytes())

				if len(bs.blobs) != 0 {
					t.Errorf("rejected upload stored %d blobs", len(bs.blobs))
				}
			})
		}
	}
}

// A tarball whose licenses cannot be derived is judged on the publish JSON's
// manifest only when no blocking policy applies; with one it is refused
// whatever the manifest claims.
func TestNPMPublish_UnderivableLicenses(t *testing.T) {
	tarballs := map[string][]byte{
		"not a tarball":        []byte("not a tarball"),
		"no package.json":      archivetest.TarGz(t, archivetest.File("package/index.js", "")),
		"invalid package.json": archivetest.TarGz(t, archivetest.File("package/package.json", "{not json")),
	}

	for name, tarball := range tarballs {
		for _, c := range policytest.UnderivableCases() {
			t.Run(name+"/"+c.Name, func(t *testing.T) {
				db := newMockDatabase()
				bs := newMockBlobStore()
				db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo"}

				rec := publishNPM(t, newLicensedNPMRouter(c.DB(db), bs),
					buildNPMPublishBodyWithTarball(t, "lodash", "1.0.0", tarball, map[string]any{"license": "MIT"}))

				c.AssertResponse(t, rec.Code, rec.Body.Bytes())

				stored := len(bs.blobs) != 0 || len(db.npmVersions) != 0 || len(db.npmPackages) != 0
				if c.Stopped() && stored {
					t.Errorf("rejected publish stored data")
				}

				if !c.Stopped() && len(db.npmVersions) != 1 {
					t.Errorf("admitted publish not stored")
				}
			})
		}
	}
}

// A pom that cannot be parsed declares nothing a policy can check: a blocking
// policy refuses it instead of admitting it unchecked.
func TestMavenUpload_UnparsablePOMUnderBlockPolicy(t *testing.T) {
	for _, c := range policytest.UnderivableCases() {
		t.Run(c.Name, func(t *testing.T) {
			db := newMockDatabase()
			bs := newMockBlobStore()
			db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

			log, _ := logger.New("info", "json", "stdout")
			router := chi.NewRouter()
			api.NewMavenUploadHandler(log, c.DB(db), bs, nil).Register(router)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPut,
				"/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.pom", strings.NewReader("<project><unclosed>"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			c.AssertResponse(t, rec.Code, rec.Body.Bytes())

			if c.Stopped() && len(bs.blobs) != 0 {
				t.Errorf("rejected upload stored %d blobs", len(bs.blobs))
			}
		})
	}
}
