// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Package is what a scan looks up: one package of one ecosystem.
type Package struct {
	Name      string
	Version   string
	Ecosystem string
	// Distro is the distribution release a deb or rpm package belongs to, as
	// "<id>-<version>" ("debian-12", "ubuntu-22.04", "rhel-9"); empty when it is
	// unknown or the ecosystem has no distributions. Only grype and trivy read it.
	Distro string
	// Release and Epoch complete an rpm's Version when the repository stores
	// them separately; zero values add nothing. Only grype and trivy read them.
	Release string
	Epoch   int
}

// ScannerBackend is the interface for vulnerability scanning backends.
type ScannerBackend interface {
	Scan(ctx context.Context, pkg Package) ([]models.Vulnerability, error)
	Name() string
}

// OSVScanner queries the OSV API for vulnerabilities.
type OSVScanner struct {
	log        *logger.Logger
	httpClient *http.Client
	osvBaseURL string
}

// NewOSVScanner creates a new OSV vulnerability scanner.
func NewOSVScanner(log *logger.Logger) *OSVScanner {
	return &OSVScanner{
		log: log,
		httpClient: telemetry.WrapClient(&http.Client{
			Timeout: 30 * time.Second,
		}),
		osvBaseURL: "https://api.osv.dev/v1",
	}
}

func (s *OSVScanner) Name() string { return "osv" }

// osvQueryRequest is the request format for OSV API.
type osvQueryRequest struct {
	Version string `json:"version"`
	Package struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
	} `json:"package"`
}

// osvQueryResponse is the response format from OSV API.
type osvQueryResponse struct {
	Vulns []osvVulnerability `json:"vulns"`
}

// osvVulnerability represents a vulnerability from OSV API.
type osvVulnerability struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	Affected []struct {
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Introduced string `json:"introduced"`
				Fixed      string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
	References []struct {
		URL string `json:"url"`
	} `json:"references"`
	Published        string `json:"published"`
	DatabaseSpecific struct {
		Severity string   `json:"severity"`
		CWEIDs   []string `json:"cwe_ids"`
	} `json:"database_specific"`
}

// Scan queries OSV API for vulnerabilities in a package.
func (s *OSVScanner) Scan(ctx context.Context, pkg Package) ([]models.Vulnerability, error) {
	packageName, version := pkg.Name, pkg.Version

	// Map ecosystem to OSV format
	osvEcosystem := mapEcosystem(pkg.Ecosystem)
	if osvEcosystem == "" {
		return nil, newScanError(s.Name(), FailureUnsupported, fmt.Errorf("%w: %q", errUnsupportedEcosystem, pkg.Ecosystem))
	}

	// Build request
	req := osvQueryRequest{
		Version: version,
	}
	req.Package.Name = packageName
	req.Package.Ecosystem = osvEcosystem

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, newScanError(s.Name(), FailureInternal, fmt.Errorf("failed to marshal OSV request: %w", err))
	}

	// Make HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.osvBaseURL+"/query", bytes.NewReader(reqBody))
	if err != nil {
		return nil, newScanError(s.Name(), FailureInternal, fmt.Errorf("failed to create OSV request: %w", err))
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, s.requestFailure(ctx, fmt.Errorf("OSV API request failed: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, s.requestFailure(ctx, fmt.Errorf("failed to read OSV response: %w", err))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, newScanError(s.Name(), FailureUpstream, fmt.Errorf( //nolint:err113 // dynamic error with runtime context
			"OSV API returned status %d for %s %s@%s", resp.StatusCode, osvEcosystem, packageName, version))
	}

	// Parse response
	var osvResp osvQueryResponse
	if err := json.Unmarshal(body, &osvResp); err != nil {
		return nil, newScanError(s.Name(), FailureInvalidOutput, fmt.Errorf("failed to parse OSV response: %w", err))
	}

	// Convert OSV vulnerabilities to our format
	var vulns []models.Vulnerability

	for _, osvVuln := range osvResp.Vulns {
		vuln := models.Vulnerability{
			ID:          osvVuln.ID,
			Summary:     osvVuln.Summary,
			PublishedAt: osvVuln.Published,
			References:  extractReferencesFromOSV(osvVuln.References),
			CWEs:        osvVuln.DatabaseSpecific.CWEIDs,
		}

		// Extract severity and CVSS
		vuln.Severity, vuln.CVSS = parseSeverityFromOSV(osvVuln.Severity, osvVuln.DatabaseSpecific.Severity)

		// Extract fixed version
		vuln.FixedIn = extractFixedVersionFromOSV(osvVuln.Affected)

		vulns = append(vulns, vuln)
	}

	return vulns, nil
}

// requestFailure classifies a transport error: an expired scan context is a
// timeout or cancellation, any other failure means OSV was unreachable.
func (s *OSVScanner) requestFailure(ctx context.Context, err error) *ScanError {
	if ctx.Err() != nil {
		return ctxFailure(s.Name(), ctx.Err())
	}

	return newScanError(s.Name(), FailureUpstream, err)
}

// TrivyScanner scans vulnerabilities using the Trivy CLI.
type TrivyScanner struct {
	log  *logger.Logger
	tool *cliTool
}

func (s *TrivyScanner) Name() string { return "trivy" }

// trivyOutput represents the JSON output from trivy fs.
type trivyOutput struct {
	Results []trivyResult `json:"Results"`
}

type trivyResult struct {
	Vulnerabilities []trivyVulnerability `json:"Vulnerabilities"`
}

type trivyVulnerability struct {
	VulnerabilityID string               `json:"VulnerabilityID"`
	Title           string               `json:"Title"`
	Severity        string               `json:"Severity"`
	CVSS            map[string]trivyCVSS `json:"CVSS"`
	FixedVersion    string               `json:"FixedVersion"`
	References      []string             `json:"References"`
	PublishedDate   string               `json:"PublishedDate"`
	CweIDs          []string             `json:"CweIDs"`
}

type trivyCVSS struct {
	V3Score float64 `json:"V3Score"`
}

// Scan runs trivy on a temporary manifest file.
func (s *TrivyScanner) Scan(ctx context.Context, pkg Package) ([]models.Vulnerability, error) {
	packageName, version, ecosystem := pkg.Name, pkg.Version, pkg.Ecosystem

	// Create temp directory with a manifest file
	tmpDir, err := os.MkdirTemp("", "trivy-scan-*")
	if err != nil {
		return nil, newScanError(s.Name(), FailureInternal, fmt.Errorf("failed to create temp dir: %w", err))
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if !validPackageToken(packageName) || !validPackageToken(version) || (pkg.Release != "" && !validPackageToken(pkg.Release)) {
		return nil, newScanError(s.Name(), FailureInternal, errInvalidPackage)
	}

	// A deb or rpm is read from a CycloneDX SBOM with the operating system in
	// it (`trivy sbom`); every other ecosystem from a generated lockfile.
	sbom := NeedsDistro(ecosystem)

	if sbom {
		err = s.writeSBOM(tmpDir, pkg)
	} else {
		err = s.writeManifest(tmpDir, packageName, version, ecosystem)
	}

	if err != nil {
		if errors.Is(err, errDistroRequired) {
			err = fmt.Errorf("%w: %q", errUnsupportedEcosystem, ecosystem)
		}

		if errors.Is(err, errUnsupportedEcosystem) {
			return nil, newScanError(s.Name(), FailureUnsupported, err)
		}

		return nil, newScanError(s.Name(), FailureInternal, err)
	}

	bin, pre := s.tool.preflight()
	if pre != nil {
		return nil, pre
	}

	// The database is managed by the registry (Registry.Start), never fetched
	// by a scan: a download inside the scan deadline would time out on a cold
	// cache and needs a writable cache on every run.
	target := []string{"fs", tmpDir}
	if sbom {
		target = []string{"sbom", filepath.Join(tmpDir, trivySBOMFile)}
	}

	args := append([]string{target[0],
		"--scanners", "vuln", "--format", "json", "--quiet", "--offline-scan",
		"--skip-version-check", "--skip-db-update", "--skip-java-db-update",
		"--cache-dir=" + s.tool.cacheDir}, target[1:]...)

	stdout, stderr, err := runScanner(ctx, nil, bin, args...)
	if err != nil {
		// stderr is server-side detail only; it is logged, never returned.
		s.log.Warnw("trivy scan failed",
			"error", err,
			"stderr", stderr,
			"package", packageName,
			"version", version,
			"ecosystem", ecosystem,
		)

		return nil, runFailure(ctx, s.Name(), err, stderr)
	}

	return s.parseTrivyOutput(stdout)
}

// trivySBOMFile is the SBOM written for a deb or rpm.
const trivySBOMFile = "scan.cdx.json"

func (s *TrivyScanner) writeSBOM(dir string, pkg Package) error {
	data, err := buildTrivySBOM(pkg)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, trivySBOMFile), data, 0600)
}

func (s *TrivyScanner) writeManifest(dir, packageName, version, ecosystem string) error {
	switch strings.ToLower(ecosystem) {
	// Trivy reads lockfiles, not the manifests that declare ranges: a bare
	// package.json, Cargo.toml, Gemfile or .csproj yields no packages and so a
	// clean result that means nothing. These ecosystems get a lockfile that
	// pins exactly the package being scanned.
	case "npm":
		data, err := json.Marshal(map[string]any{
			"name":            "scan-pkg",
			"lockfileVersion": 3,
			"requires":        true,
			"packages": map[string]any{
				"":                            map[string]any{"name": "scan-pkg", "dependencies": map[string]string{packageName: version}},
				"node_modules/" + packageName: map[string]any{"version": version},
			},
		})
		if err != nil {
			return err
		}

		return os.WriteFile(filepath.Join(dir, "package-lock.json"), data, 0600)

	case "pypi":
		content := packageName + "==" + version + "\n"
		return os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(content), 0600)

	case "maven":
		// For maven, packageName may be in groupId:artifactId format
		groupId, artifactId := packageName, packageName
		if parts := strings.SplitN(packageName, ":", 2); len(parts) == 2 {
			groupId = parts[0]
			artifactId = parts[1]
		}

		content := fmt.Sprintf(`<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>app</groupId>
  <artifactId>app</artifactId>
  <version>1.0.0</version>
  <dependencies>
    <dependency>
      <groupId>%s</groupId>
      <artifactId>%s</artifactId>
      <version>%s</version>
    </dependency>
  </dependencies>
</project>`, groupId, artifactId, version)

		return os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(content), 0600)

	case "cargo":
		content := fmt.Sprintf("version = 3\n\n[[package]]\nname = %q\nversion = %q\n", packageName, version)

		return os.WriteFile(filepath.Join(dir, "Cargo.lock"), []byte(content), 0600)

	case "go":
		content := fmt.Sprintf(`module scan-pkg

go 1.21

require %s %s
`, packageName, version)

		return os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0600)

	case "rubygems":
		content := fmt.Sprintf("GEM\n  remote: https://rubygems.org/\n  specs:\n    %s (%s)\n\nPLATFORMS\n  ruby\n\nDEPENDENCIES\n  %s (= %s)\n",
			packageName, version, packageName, version)

		return os.WriteFile(filepath.Join(dir, "Gemfile.lock"), []byte(content), 0600)

	case "nuget":
		data, err := json.Marshal(map[string]any{
			"version": 1,
			"dependencies": map[string]any{"net6.0": map[string]any{
				packageName: map[string]any{"type": "Direct", "requested": "[" + version + ", )", "resolved": version},
			}},
		})
		if err != nil {
			return err
		}

		return os.WriteFile(filepath.Join(dir, "packages.lock.json"), data, 0600)

	default:
		return fmt.Errorf("%w: %s", errUnsupportedEcosystem, ecosystem)
	}
}

func (s *TrivyScanner) parseTrivyOutput(data []byte) ([]models.Vulnerability, error) {
	var output trivyOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, newScanError(s.Name(), FailureInvalidOutput, fmt.Errorf("failed to parse trivy output: %w", err))
	}

	var vulns []models.Vulnerability

	seen := make(map[string]bool)

	for _, result := range output.Results {
		for _, tv := range result.Vulnerabilities {
			if seen[tv.VulnerabilityID] {
				continue
			}

			seen[tv.VulnerabilityID] = true

			vuln := models.Vulnerability{
				ID:          tv.VulnerabilityID,
				Summary:     tv.Title,
				FixedIn:     tv.FixedVersion,
				PublishedAt: tv.PublishedDate,
				References:  tv.References,
				CWEs:        tv.CweIDs,
			}

			// Extract CVSS score
			var cvssScore float64

			for _, cvss := range tv.CVSS {
				if cvss.V3Score > 0 {
					cvssScore = cvss.V3Score
					break
				}
			}

			vuln.CVSS = cvssScore

			// Map trivy severity to our format
			vuln.Severity = mapTrivySeverity(tv.Severity, cvssScore)

			vulns = append(vulns, vuln)
		}
	}

	if vulns == nil {
		vulns = []models.Vulnerability{}
	}

	return vulns, nil
}

// mapTrivySeverity maps Trivy severity to OpenArtifact severity format.
func mapTrivySeverity(severity string, cvssScore float64) string {
	sev := strings.ToUpper(severity)
	switch sev {
	case "CRITICAL":
		return "CRITICAL"
	case "HIGH":
		return "HIGH"
	case "MEDIUM":
		return "MEDIUM"
	case "LOW":
		return "LOW"
	default:
		if cvssScore > 0 {
			return cvssScoreToSeverity(cvssScore)
		}

		return "UNKNOWN"
	}
}

// GrypeScanner scans vulnerabilities using the Grype CLI.
type GrypeScanner struct {
	log  *logger.Logger
	tool *cliTool
}

func (s *GrypeScanner) Name() string { return "grype" }

// grypeOutput represents the JSON output from grype.
type grypeOutput struct {
	Matches []grypeMatch `json:"matches"`
}

type grypeMatch struct {
	Vulnerability grypeVulnerability `json:"vulnerability"`
	Artifact      grypeArtifact      `json:"artifact"`
}

type grypeVulnerability struct {
	ID          string      `json:"id"`
	Description string      `json:"description"`
	Severity    string      `json:"severity"`
	CVSS        []grypeCVSS `json:"cvss"`
	Fix         grypeFix    `json:"fix"`
}

type grypeCVSS struct {
	Metrics grypeCVSSMetrics `json:"metrics"`
}

type grypeCVSSMetrics struct {
	BaseScore float64 `json:"baseScore"`
}

type grypeFix struct {
	Versions []string `json:"versions"`
	State    string   `json:"state"`
}

type grypeArtifact struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
}

// Scan runs grype on the given package.
func (s *GrypeScanner) Scan(ctx context.Context, pkg Package) ([]models.Vulnerability, error) {
	packageName, version, ecosystem := pkg.Name, pkg.Version, pkg.Ecosystem

	if !validPackageToken(packageName) || !validPackageToken(version) || (pkg.Release != "" && !validPackageToken(pkg.Release)) {
		return nil, newScanError(s.Name(), FailureInternal, errInvalidPackage)
	}

	// grype takes the package as a Package URL (pkg:pypi/django@3.0). A deb or
	// rpm only means something inside a distribution release (the distro
	// qualifier): without one grype reports no match at all, which would read
	// as a clean package, so such a package is refused instead.
	pkgRef, err := buildPURL(pkg)
	if err != nil {
		if errors.Is(err, errUnsupportedEcosystem) || errors.Is(err, errDistroRequired) {
			return nil, newScanError(s.Name(), FailureUnsupported, fmt.Errorf("%w: %q", err, ecosystem))
		}

		return nil, newScanError(s.Name(), FailureInternal, err)
	}

	bin, pre := s.tool.preflight()
	if pre != nil {
		return nil, pre
	}

	// The database is managed by the registry (Registry.Start), never fetched by a scan.
	env := append(s.tool.env(), "GRYPE_DB_AUTO_UPDATE=false")

	// Without --quiet grype writes its warnings and errors to stderr (stdout
	// stays pure JSON), which is captured with a cap and logged on failure.
	stdout, stderr, err := runScanner(ctx, env, bin, pkgRef, "-o", "json")
	if err != nil {
		// stderr is server-side detail only; it is logged, never returned.
		s.log.Warnw("grype scan failed",
			"error", err,
			"stderr", stderr,
			"package", packageName,
			"version", version,
			"ecosystem", ecosystem,
		)

		return nil, runFailure(ctx, s.Name(), err, stderr)
	}

	return s.parseGrypeOutput(stdout)
}

func (s *GrypeScanner) parseGrypeOutput(data []byte) ([]models.Vulnerability, error) {
	var output grypeOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, newScanError(s.Name(), FailureInvalidOutput, fmt.Errorf("failed to parse grype output: %w", err))
	}

	var vulns []models.Vulnerability

	seen := make(map[string]bool)

	for _, match := range output.Matches {
		if seen[match.Vulnerability.ID] {
			continue
		}

		seen[match.Vulnerability.ID] = true

		vuln := models.Vulnerability{
			ID:      match.Vulnerability.ID,
			Summary: match.Vulnerability.Description,
		}

		// Extract CVSS score
		var cvssScore float64

		for _, cvss := range match.Vulnerability.CVSS {
			if cvss.Metrics.BaseScore > 0 {
				cvssScore = cvss.Metrics.BaseScore
				break
			}
		}

		vuln.CVSS = cvssScore

		// Map severity
		vuln.Severity = mapGrypeSeverity(match.Vulnerability.Severity, cvssScore)

		// Extract fixed version
		if len(match.Vulnerability.Fix.Versions) > 0 {
			vuln.FixedIn = match.Vulnerability.Fix.Versions[0]
		}

		// References are not directly in grype output at top level,
		// but we can leave them empty

		vulns = append(vulns, vuln)
	}

	if vulns == nil {
		vulns = []models.Vulnerability{}
	}

	return vulns, nil
}

// mapGrypeSeverity maps grype severity to our format.
func mapGrypeSeverity(severity string, cvssScore float64) string {
	sev := strings.ToUpper(severity)
	switch sev {
	case "CRITICAL":
		return "CRITICAL"
	case "HIGH":
		return "HIGH"
	case "MEDIUM":
		return "MEDIUM"
	case "LOW":
		return "LOW"
	default:
		if cvssScore > 0 {
			return cvssScoreToSeverity(cvssScore)
		}

		return "UNKNOWN"
	}
}

// mapEcosystem maps internal ecosystem names to OSV ecosystem names.
func mapEcosystem(ecosystem string) string {
	ecosystemMap := map[string]string{
		"npm":      "npm",
		"pypi":     "PyPI",
		"go":       "Go",
		"cargo":    "crates.io",
		"rubygems": "RubyGems",
		"maven":    "Maven",
		"nuget":    "NuGet",
		"deb":      "Debian",
		"rpm":      "Red Hat",
		"helm":     "Helm",
	}

	if mapped, ok := ecosystemMap[strings.ToLower(ecosystem)]; ok {
		return mapped
	}

	return ""
}

// parseSeverityFromOSV extracts severity level and CVSS score from OSV response.
func parseSeverityFromOSV(severities []struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}, dbSeverity string) (string, float64) {
	// Try to get severity from database_specific first
	if dbSeverity != "" {
		severity := strings.ToUpper(dbSeverity)
		switch severity {
		case "CRITICAL":
			return "CRITICAL", 9.0
		case "HIGH":
			return "HIGH", 7.0
		case "MEDIUM", "MODERATE":
			return "MEDIUM", 5.0
		case "LOW":
			return "LOW", 2.0
		}
	}

	// Try to extract from CVSS score
	for _, sev := range severities {
		if sev.Type == "CVSS_V3" {
			// Parse CVSS vector like "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
			// Extract the base score from the vector
			cvssScore := extractCVSSScore(sev.Score)
			if cvssScore > 0 {
				severity := cvssScoreToSeverity(cvssScore)
				return severity, cvssScore
			}
		}
	}

	// Default to UNKNOWN
	return "UNKNOWN", 0.0
}

// extractCVSSScore returns the CVSS v3 base score for a vector string or a
// bare numeric score. It returns 0 when the input cannot be scored.
func extractCVSSScore(cvssVector string) float64 {
	if score, ok := cvss3BaseScore(cvssVector); ok {
		return score
	}

	if score, err := strconv.ParseFloat(strings.TrimSpace(cvssVector), 64); err == nil && score >= 0 && score <= 10 {
		return score
	}

	return 0
}

// cvssScoreToSeverity converts CVSS score to severity level.
func cvssScoreToSeverity(score float64) string {
	switch {
	case score >= 9.0:
		return "CRITICAL"
	case score >= 7.0:
		return "HIGH"
	case score >= 4.0:
		return "MEDIUM"
	case score >= 0.1:
		return "LOW"
	default:
		return "UNKNOWN"
	}
}

// extractReferencesFromOSV extracts URLs from OSV references.
func extractReferencesFromOSV(refs []struct {
	URL string `json:"url"`
}) []string {
	var urls []string

	for _, ref := range refs {
		if ref.URL != "" {
			urls = append(urls, ref.URL)
		}
	}

	return urls
}

// extractFixedVersionFromOSV extracts the fixed version from OSV affected ranges.
func extractFixedVersionFromOSV(affected []struct {
	Ranges []struct {
		Type   string `json:"type"`
		Events []struct {
			Introduced string `json:"introduced"`
			Fixed      string `json:"fixed"`
		} `json:"events"`
	} `json:"ranges"`
}) string {
	for _, aff := range affected {
		for _, r := range aff.Ranges {
			if r.Type == "ECOSYSTEM" {
				for _, event := range r.Events {
					if event.Fixed != "" {
						return event.Fixed
					}
				}
			}
		}
	}

	return ""
}

var errInvalidPackage = errors.New("package name or version contains unsupported characters")

// validPackageToken reports whether a package name or version only uses
// characters that are inert in every manifest format written by
// writeManifest (no quotes, angle brackets, whitespace or control chars).
func validPackageToken(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._-+~@/:!", r):
		default:
			return false
		}
	}

	return true
}
