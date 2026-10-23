// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

// secretMarker is printed on stderr by the fake scanners and sent in upstream
// error bodies; it must never reach PublicMessage or Outcome.PublicError.
const secretMarker = "TOPSECRET-/var/lib/oa/token"

// fakeScannerDirEnv names the variable that remembers the directory holding
// a test's fake scanners, so that several can share one PATH.
const fakeScannerDirEnv = "OA_TEST_FAKE_SCANNER_DIR"

// installFakeScanner puts an executable shell script called name on a PATH that
// holds only the test's fake scanners: a real trivy or grype installed on the
// host is never found, so a test never depends on (or runs) one. The script
// sets PATH=/usr/bin:/bin itself before calling sleep, cat, ...
func installFakeScanner(t *testing.T, name, script string) {
	t.Helper()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := os.Getenv(fakeScannerDirEnv)
	if dir == "" {
		dir = t.TempDir()

		t.Setenv(fakeScannerDirEnv, dir)
		t.Setenv("PATH", dir)
	}

	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nPATH=/usr/bin:/bin\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// testRegistry returns a registry whose trivy and grype caches already hold a
// database, so scans reach the (fake) binaries on PATH.
func testRegistry(t *testing.T, log *logger.Logger) *Registry {
	t.Helper()

	return NewRegistry(log, Options{CacheDir: seededCache(t)})
}

// seededCache creates a scanner cache base directory holding a trivy and a
// grype database file.
func seededCache(t *testing.T) string {
	t.Helper()

	base := t.TempDir()

	for _, f := range []string{"trivy/db/trivy.db", "trivy/db/metadata.json", "grype/6/vulnerability.db"} {
		p := filepath.Join(base, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return base
}

// withoutScannerBinaries makes trivy and grype unfindable.
func withoutScannerBinaries(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func asScanError(t *testing.T, err error) *ScanError {
	t.Helper()

	var se *ScanError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T) is not a *ScanError", err, err)
	}

	return se
}

func TestCLIScanners_Failures(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	tests := []struct {
		name      string
		scanner   string
		ecosystem string
		script    string // "" means the binary is not installed
		timeout   time.Duration
		want      FailureKind
	}{
		{name: "trivy binary missing", scanner: "trivy", ecosystem: "npm", want: FailureUnavailable},
		{name: "grype binary missing", scanner: "grype", ecosystem: "npm", want: FailureUnavailable},
		{
			name: "trivy exits non-zero", scanner: "trivy", ecosystem: "npm",
			script: "echo " + secretMarker + " >&2; exit 3", want: FailureExecution,
		},
		{
			name: "grype exits non-zero", scanner: "grype", ecosystem: "npm",
			script: "echo " + secretMarker + " >&2; exit 1", want: FailureExecution,
		},
		{
			name: "trivy prints something that is not JSON", scanner: "trivy", ecosystem: "npm",
			script: "echo '<html>" + secretMarker + "</html>'", want: FailureInvalidOutput,
		},
		{
			name: "grype prints something that is not JSON", scanner: "grype", ecosystem: "npm",
			script: "echo 'not json'", want: FailureInvalidOutput,
		},
		{
			name: "trivy exceeds its deadline", scanner: "trivy", ecosystem: "npm",
			script: "exec sleep 30", timeout: 200 * time.Millisecond, want: FailureTimeout,
		},
		{
			name: "grype exceeds its deadline", scanner: "grype", ecosystem: "npm",
			script: "exec sleep 30", timeout: 200 * time.Millisecond, want: FailureTimeout,
		},
		{name: "trivy ecosystem not supported", scanner: "trivy", ecosystem: "docker", script: "echo '{}'", want: FailureUnsupported},
		{name: "grype ecosystem not supported", scanner: "grype", ecosystem: "docker", script: "echo '{}'", want: FailureUnsupported},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.script == "" {
				withoutScannerBinaries(t)
			} else {
				installFakeScanner(t, tt.scanner, tt.script)
			}

			ctx := context.Background()

			if tt.timeout > 0 {
				var cancel context.CancelFunc

				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}

			backend := testRegistry(t, log).Backend(tt.scanner)

			vulns, err := backend.Scan(ctx, leftPad(tt.ecosystem))
			if err == nil {
				t.Fatalf("scan must fail, got %d vulnerabilities and no error", len(vulns))
			}

			se := asScanError(t, err)
			if se.Kind != tt.want || se.Scanner != tt.scanner {
				t.Fatalf("got scanner=%q kind=%q (%v), want scanner=%q kind=%q", se.Scanner, se.Kind, err, tt.scanner, tt.want)
			}

			if strings.Contains(se.PublicMessage(), "TOPSECRET") {
				t.Errorf("PublicMessage leaks scanner output: %q", se.PublicMessage())
			}

			// End to end through Run: the failure is a failed scan, never a clean one.
			out := Run(ctx, log, []ScannerBackend{backend}, leftPad(tt.ecosystem))
			if out.Status != StatusFailed || len(out.Vulnerabilities) != 0 {
				t.Errorf("Run status=%q vulns=%d, want %q with no findings", out.Status, len(out.Vulnerabilities), StatusFailed)
			}

			if strings.Contains(out.PublicError(), "TOPSECRET") || strings.Contains(out.PublicError(), "/var/lib") {
				t.Errorf("PublicError leaks scanner output: %q", out.PublicError())
			}
		})
	}
}

func TestCLIScanners_Success(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	tests := []struct {
		name    string
		scanner string
		script  string
		want    []string
	}{
		{
			name: "trivy findings", scanner: "trivy",
			script: `echo '{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-2024-1","Severity":"HIGH"},{"VulnerabilityID":"CVE-2024-1","Severity":"HIGH"},{"VulnerabilityID":"CVE-2024-2","Severity":"LOW"}]}]}'`,
			want:   []string{"CVE-2024-1", "CVE-2024-2"},
		},
		{
			name: "grype findings", scanner: "grype",
			script: `echo '{"matches":[{"vulnerability":{"id":"GHSA-x","severity":"Critical"}}]}'`,
			want:   []string{"GHSA-x"},
		},
		{name: "trivy clean", scanner: "trivy", script: `echo '{"Results":[]}'`, want: []string{}},
		{name: "grype clean", scanner: "grype", script: `echo '{"matches":[]}'`, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeScanner(t, tt.scanner, tt.script)

			out := Run(context.Background(), log, []ScannerBackend{testRegistry(t, log).Backend(tt.scanner)}, leftPad("npm"))

			if out.Status != StatusCompleted {
				t.Fatalf("status = %q (%s), want %q", out.Status, out.PublicError(), StatusCompleted)
			}

			if got := vulnIDs(out.Vulnerabilities); !slices.Equal(got, tt.want) {
				t.Errorf("vulnerabilities = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOSVScanner_Failures(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	tests := []struct {
		name      string
		ecosystem string
		handler   http.HandlerFunc
		timeout   time.Duration
		want      FailureKind
	}{
		{
			name: "non-200 answer", ecosystem: "npm", want: FailureUpstream,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, secretMarker, http.StatusBadGateway)
			},
		},
		{
			name: "rate limited", ecosystem: "npm", want: FailureUpstream,
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) },
		},
		{
			name: "200 with a body that is not JSON", ecosystem: "npm", want: FailureInvalidOutput,
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>" + secretMarker)) },
		},
		{
			name: "answer slower than the deadline", ecosystem: "npm", want: FailureTimeout, timeout: 150 * time.Millisecond,
			handler: func(_ http.ResponseWriter, r *http.Request) {
				// Reading the body to EOF lets the server notice the client hanging up.
				_, _ = io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
			},
		},
		{
			name: "ecosystem OSV does not know", ecosystem: "docker", want: FailureUnsupported,
			handler: func(http.ResponseWriter, *http.Request) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			scanner := NewOSVScanner(log)
			scanner.osvBaseURL = srv.URL
			scanner.httpClient = srv.Client()

			ctx := context.Background()

			if tt.timeout > 0 {
				var cancel context.CancelFunc

				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}

			_, err := scanner.Scan(ctx, leftPad(tt.ecosystem))
			if err == nil {
				t.Fatal("scan must fail, got no error")
			}

			if se := asScanError(t, err); se.Kind != tt.want {
				t.Fatalf("kind = %q (%v), want %q", se.Kind, err, tt.want)
			}

			out := Run(ctx, log, []ScannerBackend{scanner}, leftPad(tt.ecosystem))
			if out.Status != StatusFailed {
				t.Errorf("Run status = %q, want %q", out.Status, StatusFailed)
			}

			if strings.Contains(out.PublicError(), "TOPSECRET") || strings.Contains(out.PublicError(), srv.URL) {
				t.Errorf("PublicError leaks upstream detail: %q", out.PublicError())
			}
		})
	}
}

func TestOSVScanner_Unreachable(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more

	scanner := NewOSVScanner(log)
	scanner.osvBaseURL = url

	_, err := scanner.Scan(context.Background(), leftPad("npm"))
	if err == nil {
		t.Fatal("scan must fail when OSV is unreachable")
	}

	if se := asScanError(t, err); se.Kind != FailureUpstream {
		t.Errorf("kind = %q, want %q", se.Kind, FailureUpstream)
	}
}

func TestOSVScanner_Success(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-aaaa","summary":"bad"}]}`))
	}))
	defer srv.Close()

	scanner := NewOSVScanner(log)
	scanner.osvBaseURL = srv.URL

	out := Run(context.Background(), log, []ScannerBackend{scanner}, leftPad("npm"))
	if out.Status != StatusCompleted || !slices.Equal(vulnIDs(out.Vulnerabilities), []string{"GHSA-aaaa"}) {
		t.Fatalf("status=%q vulns=%v", out.Status, vulnIDs(out.Vulnerabilities))
	}
}

// A scanner that cannot run next to one that can is a partial scan: the
// working scanner's findings are kept and the broken one is named.
func TestRun_PartialWithRealScanners(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-bbbb"}]}`))
	}))
	defer srv.Close()

	osv := NewOSVScanner(log)
	osv.osvBaseURL = srv.URL

	withoutScannerBinaries(t)

	out := Run(context.Background(), log, []ScannerBackend{osv, testRegistry(t, log).Backend("trivy")}, leftPad("npm"))

	if out.Status != StatusPartial {
		t.Fatalf("status = %q, want %q", out.Status, StatusPartial)
	}

	if !slices.Equal(out.FailedScanners(), []string{"trivy"}) {
		t.Errorf("FailedScanners = %v", out.FailedScanners())
	}

	if !slices.Equal(vulnIDs(out.Vulnerabilities), []string{"GHSA-bbbb"}) {
		t.Errorf("findings of the scanner that worked were dropped: %v", vulnIDs(out.Vulnerabilities))
	}

	if want := "trivy: " + (&ScanError{Kind: FailureUnavailable}).PublicMessage(); out.PublicError() != want {
		t.Errorf("PublicError = %q, want %q", out.PublicError(), want)
	}
}
