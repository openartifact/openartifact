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

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
)

const (
	djangoRowID  = "11111111-1111-1111-1111-111111111111"
	djangoSHA256 = "django-wheel-sha256"
)

// artifactScanDB resolves package-listing row ids to stored blobs.
type artifactScanDB struct {
	*scanPolicyDB

	blobs map[string]*models.ComponentBlob
}

func (db *artifactScanDB) GetRepoComponentBlob(_ context.Context, _, id string) (*models.ComponentBlob, error) {
	return db.blobs[id], nil
}

// storedWheel is a wheel in the blob store behind a package-listing row.
func storedWheel(t *testing.T, metadata string) (*artifactScanDB, *formattest.BlobStore) {
	t.Helper()

	return storedArtifact(t, "pypi", "Django-3.0-py3-none-any.whl", archivetest.Zip(t,
		archivetest.File("django/__init__.py", ""),
		archivetest.File("Django-3.0.dist-info/METADATA", "Metadata-Version: 2.1\nName: Django\nVersion: 3.0\n"+metadata),
	))
}

func storedArtifact(t *testing.T, format, filename string, data []byte) (*artifactScanDB, *formattest.BlobStore) {
	t.Helper()

	bs := formattest.NewBlobStore()
	bs.Blobs[djangoSHA256] = data

	db := &artifactScanDB{
		scanPolicyDB: newScanPolicyDB(),
		blobs: map[string]*models.ComponentBlob{
			djangoRowID: {ID: djangoRowID, Format: format, Filename: filename, SHA256: djangoSHA256},
		},
	}

	return db, bs
}

func scanArtifact(t *testing.T, db *artifactScanDB, bs *formattest.BlobStore) (int, map[string]any) {
	t.Helper()

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	h := NewLicenseHandler(db, log, ev, nil, bs)

	rw := serveAs(h.RegisterRoutes, http.MethodPost, "/api/v1/repositories/repo/license/scan", `{"artifact_id":"`+djangoRowID+`"}`)

	var out map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	return rw.Code, out
}

func stringsOf(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))

	for _, it := range items {
		s, _ := it.(string)
		out = append(out, s)
	}

	return out
}

// A Django 3.0 style wheel declares "License: BSD" and the classifier "BSD
// License": the scan reports the raw value and flags it as unresolved.
func TestLicenseScan_ReadsTheStoredArtifact(t *testing.T) {
	db, bs := storedWheel(t, "License: BSD\nClassifier: License :: OSI Approved :: BSD License\n")

	code, out := scanArtifact(t, db, bs)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, out)
	}

	if got := stringsOf(out["licenses"]); len(got) != 1 || got[0] != "BSD" {
		t.Errorf("licenses = %v, want [BSD]", got)
	}

	if got := stringsOf(out["unresolved_licenses"]); len(got) != 1 || got[0] != "BSD" {
		t.Errorf("unresolved_licenses = %v, want [BSD]", got)
	}

	if stored := db.licenses[djangoRowID]; stored == nil || len(stored.UnresolvedLicenses) != 1 {
		t.Errorf("stored result = %+v, want the unresolved license persisted", stored)
	}

	if out["license_source"] != models.LicenseSourceArtifact {
		t.Errorf("license_source = %v, want artifact", out["license_source"])
	}
}

func TestLicenseScan_ResolvedLicenseIsNotFlagged(t *testing.T) {
	db, bs := storedWheel(t, "License-Expression: MIT OR Apache-2.0\n")

	_, out := scanArtifact(t, db, bs)

	if got := stringsOf(out["licenses"]); len(got) != 1 || got[0] != "MIT OR Apache-2.0" {
		t.Errorf("licenses = %v", got)
	}

	if got := stringsOf(out["unresolved_licenses"]); len(got) != 0 {
		t.Errorf("unresolved_licenses = %v, want none", got)
	}

	if _, isNull := out["unresolved_licenses"].([]any); !isNull {
		t.Errorf("unresolved_licenses must be an array, got %T", out["unresolved_licenses"])
	}
}

// A property can add a license but never hide the artifact's own.
func TestLicenseScan_PropertiesCannotHideTheArtifactLicense(t *testing.T) {
	db, bs := storedWheel(t, "License-Expression: GPL-3.0-only\n")
	db.props["license"] = "MIT"
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0-only"})}

	code, out := scanArtifact(t, db, bs)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, out)
	}

	got := stringsOf(out["licenses"])
	if len(got) != 2 || got[0] != "GPL-3.0-only" || got[1] != "MIT" {
		t.Errorf("licenses = %v, want the file's then the property's", got)
	}

	if out["quarantined"] != true {
		t.Errorf("the GPL file under a deny policy must quarantine: %v", out)
	}
}

func TestLicenseScan_BlockPolicyOnRawArtifactLicense(t *testing.T) {
	db, bs := storedWheel(t, "License: BSD\n")
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"BSD"})}

	_, out := scanArtifact(t, db, bs)

	if out["quarantined"] != true {
		t.Errorf("BSD under a deny policy must quarantine: %v", out)
	}
}

func TestLicenseScan_UnsafeArtifactIsRefused(t *testing.T) {
	db, bs := storedWheel(t, "Description: "+strings.Repeat("A", 2<<20)+"\n")

	code, out := scanArtifact(t, db, bs)
	if code != http.StatusUnprocessableEntity || out["code"] != "unsafe_artifact" {
		t.Fatalf("status = %d body = %v, want 422 unsafe_artifact", code, out)
	}

	if len(db.licenses) != 0 {
		t.Error("an unsafe artifact must not get a scan result")
	}
}

func TestLicenseScan_UnreadableArtifactFallsBackToProperties(t *testing.T) {
	db, bs := storedArtifact(t, "pypi", "Django-3.0-py3-none-any.whl", []byte("not a zip"))
	db.props["license"] = "MIT"

	code, out := scanArtifact(t, db, bs)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, out)
	}

	if got := stringsOf(out["licenses"]); len(got) != 1 || got[0] != "MIT" {
		t.Errorf("licenses = %v, want the property's", got)
	}

	if out["license_source"] != models.LicenseSourceClientDeclared {
		t.Errorf("license_source = %v, want client_declared", out["license_source"])
	}
}

func TestLicenseScan_UnsupportedFormatUsesProperties(t *testing.T) {
	db, bs := storedArtifact(t, "raw", "notes.txt", []byte("hello"))
	db.props["license"] = "MIT"

	_, out := scanArtifact(t, db, bs)

	if got := stringsOf(out["licenses"]); len(got) != 1 || got[0] != "MIT" {
		t.Errorf("licenses = %v, want the property's", got)
	}
}

// Editing license properties re-evaluates policies on the artifact's own
// licenses too: a harmless property cannot lift a block.
func TestProperties_ReevaluationReadsTheStoredArtifact(t *testing.T) {
	db, bs := storedWheel(t, "License-Expression: GPL-3.0-only\n")
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0-only"})}

	log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})
	h := NewPropertiesHandler(log, db, bs)
	register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

	rw := serveAs(register, http.MethodPatch, "/api/v1/repositories/repo/artifacts/"+djangoRowID+"/properties", `{"license":"MIT"}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	stored := db.licenses[djangoRowID]
	if stored == nil || !stored.Quarantined {
		t.Fatalf("stored = %+v, want quarantined: the artifact's GPL license still applies", stored)
	}
}

func TestScanRubyGem_ReadsGemspecLicenses(t *testing.T) {
	// The metadata.gz of a gem is the YAML gemspec.
	gemspec := "--- !ruby/object:Gem::Specification\nname: demo\nversion: !ruby/object:Gem::Version\n  version: 1.0.0\nplatform: ruby\nlicenses:\n- GPL-3.0-only\n"

	db, bs := storedArtifact(t, "rubygems", "demo-1.0.0.gem", archivetest.Gem(t, gemspec))

	_, out := scanArtifact(t, db, bs)

	if got := stringsOf(out["licenses"]); len(got) != 1 || got[0] != "GPL-3.0-only" {
		t.Errorf("licenses = %v, want [GPL-3.0-only]", got)
	}
}

// Under a blocking policy, a package whose file cannot be read must not be
// judged on what clients declared: the scan refuses it, and so does a change of
// its license properties.
func TestLicenseScan_UnderivableArtifactUnderBlockPolicy(t *testing.T) {
	files := map[string]struct {
		format, filename string
		data             []byte
	}{
		"damaged wheel":          {"pypi", "Django-3.0-py3-none-any.whl", []byte("not a zip")},
		"egg":                    {"pypi", "Django-3.0-py3.8.egg", []byte("egg")},
		"npm without a manifest": {"npm", "demo-1.0.0.tgz", archivetest.TarGz(t, archivetest.File("package/index.js", ""))},
		"unparsable pom":         {"maven", "demo-1.0.0.pom", []byte("<project><unclosed>")},
	}

	for name, f := range files {
		t.Run(name, func(t *testing.T) {
			db, bs := storedArtifact(t, f.format, f.filename, f.data)
			db.props["license"] = "MIT"
			db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0-only"})}

			code, out := scanArtifact(t, db, bs)
			if code != http.StatusUnprocessableEntity || out["code"] != "unreadable_artifact" {
				t.Fatalf("status = %d body = %v, want 422 unreadable_artifact", code, out)
			}

			if len(db.licenses) != 0 {
				t.Error("a refused scan must not store a result")
			}

			log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})
			h := NewPropertiesHandler(log, db, bs)
			register := func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }

			rw := serveAs(register, http.MethodPatch, "/api/v1/repositories/repo/artifacts/"+djangoRowID+"/properties", `{"license":"MIT"}`)
			if rw.Code != http.StatusUnprocessableEntity || !strings.Contains(rw.Body.String(), "unreadable_artifact") {
				t.Fatalf("properties status = %d (%s), want 422 unreadable_artifact", rw.Code, rw.Body.String())
			}
		})
	}
}

// Without a blocking policy the properties are the fallback, and the result
// says so.
func TestLicenseScan_UnderivableArtifactWithoutBlockPolicy(t *testing.T) {
	for _, action := range []string{"", "warn", "allow"} {
		t.Run("policy "+action, func(t *testing.T) {
			db, bs := storedArtifact(t, "pypi", "Django-3.0-py3.8.egg", []byte("egg"))
			db.props["license"] = "MIT"

			if action != "" {
				db.policies = []*models.LicensePolicy{policytest.Policy(action, nil, []string{"MIT"})}
			}

			code, out := scanArtifact(t, db, bs)
			if code != http.StatusOK {
				t.Fatalf("status = %d: %v", code, out)
			}

			if got := stringsOf(out["licenses"]); len(got) != 1 || got[0] != "MIT" {
				t.Errorf("licenses = %v, want the property's", got)
			}

			if out["license_source"] != models.LicenseSourceClientDeclared {
				t.Errorf("license_source = %v, want client_declared", out["license_source"])
			}
		})
	}
}

// A readable package that declares no license is not "underivable": nothing
// was hidden from the server, the result is just client-declared.
func TestLicenseScan_ReadableWithoutLicenseIsClientDeclared(t *testing.T) {
	db, bs := storedWheel(t, "")
	db.props["license"] = "MIT"
	db.policies = []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0-only"})}

	code, out := scanArtifact(t, db, bs)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, out)
	}

	if out["license_source"] != models.LicenseSourceClientDeclared {
		t.Errorf("license_source = %v, want client_declared", out["license_source"])
	}
}
