// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// stubBackend is a ScannerBackend with a canned answer.
type stubBackend struct {
	name  string
	vulns []models.Vulnerability
	err   error
}

func (s stubBackend) Name() string { return s.name }

func (s stubBackend) Scan(context.Context, Package) ([]models.Vulnerability, error) {
	return s.vulns, s.err
}

// leftPad is the package the scanner tests look up, in the given ecosystem.
func leftPad(ecosystem string) Package {
	return Package{Name: "left-pad", Version: "1.0.0", Ecosystem: ecosystem}
}

func vulnIDs(vs []models.Vulnerability) []string {
	ids := make([]string, 0, len(vs))
	for _, v := range vs {
		ids = append(ids, v.ID)
	}

	slices.Sort(ids)

	return ids
}

func TestRun_Outcomes(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	boom := errors.New("dial tcp 10.0.0.7:443: connection refused (token=s3cret)")

	tests := []struct {
		name         string
		backends     []ScannerBackend
		wantStatus   string
		wantVulns    []string
		wantFailed   []string
		wantPublicIn []string
	}{
		{
			name:       "single scanner finds nothing",
			backends:   []ScannerBackend{stubBackend{name: "osv"}},
			wantStatus: StatusCompleted,
			wantVulns:  []string{},
			wantFailed: []string{},
		},
		{
			name: "findings of every scanner are merged and deduplicated",
			backends: []ScannerBackend{
				stubBackend{name: "osv", vulns: []models.Vulnerability{{ID: "CVE-1"}, {ID: "CVE-2"}}},
				stubBackend{name: "trivy", vulns: []models.Vulnerability{{ID: "CVE-2"}, {ID: "CVE-3"}}},
			},
			wantStatus: StatusCompleted,
			wantVulns:  []string{"CVE-1", "CVE-2", "CVE-3"},
			wantFailed: []string{},
		},
		{
			name:         "the only scanner fails",
			backends:     []ScannerBackend{stubBackend{name: "trivy", err: boom}},
			wantStatus:   StatusFailed,
			wantVulns:    []string{},
			wantFailed:   []string{"trivy"},
			wantPublicIn: []string{"trivy: scan failed"},
		},
		{
			name: "one of two scanners fails",
			backends: []ScannerBackend{
				stubBackend{name: "osv", vulns: []models.Vulnerability{{ID: "CVE-9"}}},
				stubBackend{name: "grype", err: &ScanError{Scanner: "grype", Kind: FailureUnavailable, Err: boom}},
			},
			wantStatus:   StatusPartial,
			wantVulns:    []string{"CVE-9"},
			wantFailed:   []string{"grype"},
			wantPublicIn: []string{"grype: scanner is not available on the server (scanner_unavailable)"},
		},
		{
			name: "every scanner fails",
			backends: []ScannerBackend{
				stubBackend{name: "trivy", err: &ScanError{Scanner: "trivy", Kind: FailureTimeout, Err: boom}},
				stubBackend{name: "osv", err: &ScanError{Scanner: "osv", Kind: FailureUpstream, Err: boom}},
			},
			wantStatus: StatusFailed,
			wantVulns:  []string{},
			wantFailed: []string{"osv", "trivy"},
			wantPublicIn: []string{
				"osv: vulnerability database request failed",
				"trivy: scan timed out",
			},
		},
		{
			name:       "no scanner at all is a failure, not a clean result",
			backends:   nil,
			wantStatus: StatusFailed,
			wantVulns:  []string{},
			wantFailed: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Run(context.Background(), log, tt.backends, Package{Name: "pkg", Version: "1.0.0", Ecosystem: "npm"})

			if out.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", out.Status, tt.wantStatus)
			}

			if got := vulnIDs(out.Vulnerabilities); !slices.Equal(got, tt.wantVulns) {
				t.Errorf("vulnerabilities = %v, want %v", got, tt.wantVulns)
			}

			if got := out.FailedScanners(); !slices.Equal(got, tt.wantFailed) {
				t.Errorf("FailedScanners = %v, want %v", got, tt.wantFailed)
			}

			public := out.PublicError()
			for _, want := range tt.wantPublicIn {
				if !strings.Contains(public, want) {
					t.Errorf("PublicError = %q, want it to contain %q", public, want)
				}
			}

			for _, leaked := range []string{"s3cret", "10.0.0.7", "connection refused"} {
				if strings.Contains(public, leaked) {
					t.Errorf("PublicError = %q leaks %q", public, leaked)
				}
			}
		})
	}
}

func TestRun_FailedScannersNeverNil(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	out := Run(
		context.Background(),
		log,
		[]ScannerBackend{stubBackend{name: "osv"}},
		Package{Name: "pkg", Version: "1.0.0", Ecosystem: "npm"},
	)

	if out.FailedScanners() == nil {
		t.Error("FailedScanners must be an empty slice, not nil, so it serializes as []")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want FailureKind
	}{
		{"typed error keeps its kind", &ScanError{Scanner: "x", Kind: FailureExecution}, FailureExecution},
		{
			"wrapped typed error keeps its kind",
			errors.Join(errors.New("ctx"), &ScanError{Scanner: "x", Kind: FailureUpstream}),
			FailureUpstream,
		},
		{"deadline", context.DeadlineExceeded, FailureTimeout},
		{"canceled", context.Canceled, FailureCanceled},
		{"output too large", ErrScannerOutputTooLarge, FailureOutputTooLarge},
		{"anything else", errors.New("disk on fire at /var/secret"), FailureInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify("x", tt.err)
			if got.Kind != tt.want {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.want)
			}

			if strings.Contains(got.PublicMessage(), "secret") {
				t.Errorf("PublicMessage %q leaks the raw error", got.PublicMessage())
			}
		})
	}
}

func TestScanError_PublicMessageIsFixedPerKind(t *testing.T) {
	kinds := []FailureKind{
		FailureUnavailable, FailureTimeout, FailureCanceled, FailureExecution, FailureInvalidOutput,
		FailureOutputTooLarge, FailureUnsupported, FailureUpstream, FailureDatabase, FailureInternal,
	}

	seen := map[string]FailureKind{}

	for _, k := range kinds {
		se := &ScanError{Scanner: "trivy", Kind: k, Err: errors.New("/home/runner/.cache stderr: panic")}

		msg := se.PublicMessage()
		// Only the two setup failures carry a code and the docs link.
		linked := k == FailureUnavailable || k == FailureDatabase
		if msg == "" || (!linked && strings.ContainsAny(msg, "/:")) || (linked && !strings.Contains(msg, DocsURL)) {
			t.Errorf("%s: PublicMessage %q must be a plain fixed sentence", k, msg)
		}

		if other, dup := seen[msg]; dup && k != FailureInternal && other != FailureInternal {
			t.Errorf("%s and %s share the message %q", k, other, msg)
		}

		seen[msg] = k

		if !strings.Contains(se.Error(), "panic") {
			t.Errorf("%s: Error() must keep the full detail for the server log, got %q", k, se.Error())
		}
	}
}

// OSV reports GHSA-… with the CVE as an alias where trivy and grype report the
// CVE itself: one vulnerability, counted once, at the highest severity any
// scanner gave it.
func TestRun_MergesFindingsByAlias(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	osvFinding := models.Vulnerability{
		ID: "GHSA-aaaa-bbbb-cccc", Aliases: []string{"CVE-2024-0001"}, Severity: "MEDIUM", CVSS: 5.5,
		Summary: "from osv", References: []string{"https://osv.example/a"}, CWEs: []string{"CWE-79"},
	}
	trivyFinding := models.Vulnerability{
		ID: "CVE-2024-0001", Severity: "HIGH", CVSS: 8.1, FixedIn: "1.0.1",
		References: []string{"https://osv.example/a", "https://trivy.example/b"}, CWEs: []string{"CWE-79", "CWE-80"},
	}

	for name, order := range map[string][]ScannerBackend{
		"osv first":   {stubBackend{name: "osv", vulns: []models.Vulnerability{osvFinding}}, stubBackend{name: "trivy", vulns: []models.Vulnerability{trivyFinding}}},
		"trivy first": {stubBackend{name: "trivy", vulns: []models.Vulnerability{trivyFinding}}, stubBackend{name: "osv", vulns: []models.Vulnerability{osvFinding}}},
	} {
		t.Run(name, func(t *testing.T) {
			out := Run(context.Background(), log, order, Package{Name: "pkg", Version: "1.0.0", Ecosystem: "npm"})

			if len(out.Vulnerabilities) != 1 {
				t.Fatalf("findings = %v, want one merged finding", vulnIDs(out.Vulnerabilities))
			}

			got := out.Vulnerabilities[0]
			if got.Severity != "HIGH" || got.CVSS != 8.1 {
				t.Errorf("severity = %s/%v, want the highest (HIGH/8.1)", got.Severity, got.CVSS)
			}

			ids := append([]string{got.ID}, got.Aliases...)
			slices.Sort(ids)

			if want := []string{"CVE-2024-0001", "GHSA-aaaa-bbbb-cccc"}; !slices.Equal(ids, want) {
				t.Errorf("id and aliases = %v, want %v (both names stay discoverable)", ids, want)
			}

			if got.FixedIn != "1.0.1" || got.Summary == "" {
				t.Errorf("merged finding lost data: fixed_in=%q summary=%q", got.FixedIn, got.Summary)
			}

			if len(got.References) != 2 || len(got.CWEs) != 2 {
				t.Errorf("references = %v, cwes = %v, want the union without duplicates", got.References, got.CWEs)
			}
		})
	}
}

// A third finding that names two earlier ones joins them into one.
func TestRun_MergesFindingsThroughAChain(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	backends := []ScannerBackend{
		stubBackend{name: "a", vulns: []models.Vulnerability{
			{ID: "GHSA-1", Aliases: []string{"CVE-1"}, Severity: "LOW"},
			{ID: "GHSA-2", Aliases: []string{"CVE-2"}, Severity: "CRITICAL"},
			{ID: "GHSA-3", Severity: "MEDIUM"},
		}},
		stubBackend{name: "b", vulns: []models.Vulnerability{{ID: "CVE-1", Aliases: []string{"CVE-2"}, Severity: "HIGH"}}},
	}

	out := Run(context.Background(), log, backends, Package{Name: "pkg", Version: "1.0.0", Ecosystem: "npm"})

	if len(out.Vulnerabilities) != 2 {
		t.Fatalf("findings = %+v, want GHSA-1/2 merged and GHSA-3 apart", out.Vulnerabilities)
	}

	for _, v := range out.Vulnerabilities {
		switch v.ID {
		case "GHSA-1":
			if v.Severity != "CRITICAL" || !slices.Contains(v.Aliases, "GHSA-2") || !slices.Contains(v.Aliases, "CVE-2") {
				t.Errorf("merged finding = %+v", v)
			}
		case "GHSA-3":
		default:
			t.Errorf("unexpected finding %q", v.ID)
		}
	}
}

// Findings that share nothing stay apart, and one without an id is never merged.
func TestRun_KeepsDistinctFindingsApart(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	backends := []ScannerBackend{
		stubBackend{name: "a", vulns: []models.Vulnerability{
			{ID: "CVE-1", Aliases: []string{"GHSA-1"}}, {Summary: "anonymous 1"}, {Summary: "anonymous 2"},
		}},
		stubBackend{name: "b", vulns: []models.Vulnerability{{ID: "CVE-2", Aliases: []string{"GHSA-2"}}}},
	}

	out := Run(context.Background(), log, backends, Package{Name: "pkg", Version: "1.0.0", Ecosystem: "npm"})

	if len(out.Vulnerabilities) != 4 {
		t.Fatalf("findings = %+v, want four", out.Vulnerabilities)
	}
}
