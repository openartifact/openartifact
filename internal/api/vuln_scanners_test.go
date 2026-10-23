// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/vuln"
)

func registerScannerRoutes(h *VulnHandler) func(chi.Router) {
	return func(r chi.Router) { h.RegisterRoutes(r, "/api/v1/repositories/{name}") }
}

const vulnScannersPath = "/api/v1/repositories/main/vuln/scanners"

type scannerJSON struct {
	Name        string  `json:"name"`
	Available   bool    `json:"available"`
	Ready       bool    `json:"ready"`
	DBState     *string `json:"db_state"`
	DBUpdatedAt *string `json:"db_updated_at"`

	InstallState *string `json:"install_state"`
	Code         *string `json:"code"`
	Message      *string `json:"message"`
	DocsURL      *string `json:"docs_url"`
}

func decodeScanners(t *testing.T, body []byte) []scannerJSON {
	t.Helper()

	var got struct {
		Scanners []scannerJSON `json:"scanners"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}

	return got.Scanners
}

func TestVulnScanners_ReportsReadinessWithoutLeakingPaths(t *testing.T) {
	dir := t.TempDir()
	missingTrivy := filepath.Join(dir, "no-such-dir", "trivy")
	missingGrype := filepath.Join(dir, "no-such-dir", "grype")

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	registry := vuln.NewRegistry(log, vuln.Options{
		CacheDir: filepath.Join(dir, "cache"),
		Trivy:    vuln.TrivyOptions{Path: missingTrivy},
		Grype:    vuln.GrypeOptions{Path: missingGrype},
	})
	h := NewVulnHandler(nil, log, ev, registry)

	rw := serveAs(registerScannerRoutes(h), http.MethodGet, vulnScannersPath, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body)
	}

	body := rw.Body.String()
	for _, secret := range []string{missingTrivy, missingGrype, dir} {
		if strings.Contains(body, secret) {
			t.Errorf("response leaks the server path %q: %s", secret, body)
		}
	}

	got := decodeScanners(t, rw.Body.Bytes())

	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}

	if strings.Join(names, ",") != "osv,trivy,grype" {
		t.Fatalf("scanner order = %v", names)
	}

	osv := got[0]
	if !osv.Available || !osv.Ready || osv.Code != nil || osv.Message != nil || osv.DocsURL != nil || osv.DBState != nil {
		t.Errorf("osv must be ready with no error fields: %+v", osv)
	}

	for _, s := range got[1:] {
		assertMissingBinary(t, s)
	}
}

func assertMissingBinary(t *testing.T, s scannerJSON) {
	t.Helper()

	if s.Available || s.Ready {
		t.Errorf("%s: a missing binary must not be available or ready: %+v", s.Name, s)
	}

	if s.Code == nil || *s.Code != vuln.CodeScannerUnavailable {
		t.Errorf("%s: code = %v, want %s", s.Name, s.Code, vuln.CodeScannerUnavailable)
	}

	if s.Message == nil || *s.Message == "" {
		t.Errorf("%s: message must say what to do", s.Name)
	}

	if s.DocsURL == nil || *s.DocsURL != vuln.DocsURL {
		t.Errorf("%s: docs_url = %v, want %s", s.Name, s.DocsURL, vuln.DocsURL)
	}
}

func TestVulnScanners_AdminOnly(t *testing.T) {
	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	h := NewVulnHandler(nil, log, ev, vuln.NewRegistry(log, vuln.Options{CacheDir: t.TempDir()}))

	rw := serveWithPrincipal(registerScannerRoutes(h), &auth.Principal{IsAnonymous: true}, http.MethodGet, vulnScannersPath, "")
	if rw.Code != http.StatusForbidden {
		t.Errorf("anonymous status = %d, want 403", rw.Code)
	}
}

func TestVulnScanners_ReportsInstallState(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	log, ev := newBundlesScansEnv(t, &bundlesScansMockDB{})
	registry := vuln.NewRegistry(log, vuln.Options{
		CacheDir:        t.TempDir(),
		DownloadBaseURL: "https://mirror.internal.example/gh",
		Trivy:           vuln.TrivyOptions{Install: true},
	})
	h := NewVulnHandler(nil, log, ev, registry)

	rw := serveAs(registerScannerRoutes(h), http.MethodGet, vulnScannersPath, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body)
	}

	if strings.Contains(rw.Body.String(), "mirror.internal.example") {
		t.Errorf("response leaks the configured download host: %s", rw.Body)
	}

	got := decodeScanners(t, rw.Body.Bytes())

	trivy := got[1]
	if trivy.Available || trivy.Ready || trivy.InstallState == nil || *trivy.InstallState != vuln.InstallStateInstalling ||
		trivy.Code == nil || *trivy.Code != vuln.CodeScannerInstalling {
		t.Errorf("trivy with install auto must report installing: %+v", trivy)
	}

	// Without install auto the scanner has no install state.
	if grype := got[2]; grype.InstallState != nil {
		t.Errorf("grype install_state = %v, want null", *grype.InstallState)
	}
}
