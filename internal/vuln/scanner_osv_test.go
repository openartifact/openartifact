// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

type osvSeverity = []struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

func osvSev(pairs ...string) osvSeverity {
	var out osvSeverity

	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, struct {
			Type  string `json:"type"`
			Score string `json:"score"`
		}{pairs[i], pairs[i+1]})
	}

	return out
}

// The score of a trivy finding must not depend on the order Go happens to
// iterate the CVSS sources of the report: the highest V3 score wins.
func TestParseTrivyOutput_CVSSIsTheHighestV3Score(t *testing.T) {
	data := []byte(`{"Results":[{"Vulnerabilities":[{
		"VulnerabilityID":"CVE-2024-1","Severity":"HIGH",
		"CVSS":{"nvd":{"V3Score":7.5},"ghsa":{"V3Score":9.8},"redhat":{"V3Score":8.1},"bitnami":{"V2Score":10},"amazon":{"V3Score":0}}
	}]}]}`)

	for range 200 {
		vulns, err := (&TrivyScanner{}).parseTrivyOutput(data)
		if err != nil || len(vulns) != 1 {
			t.Fatalf("parse: %v %v", vulns, err)
		}

		if vulns[0].CVSS != 9.8 {
			t.Fatalf("CVSS = %v, want 9.8 on every run", vulns[0].CVSS)
		}
	}
}

func TestCVSS2BaseScore(t *testing.T) {
	tests := []struct {
		vector string
		want   float64
	}{
		{"AV:N/AC:L/Au:N/C:C/I:C/A:C", 10},
		{"AV:N/AC:L/Au:N/C:P/I:P/A:P", 7.5},
		{"AV:N/AC:M/Au:N/C:N/I:N/A:P", 4.3},
		{"AV:L/AC:L/Au:N/C:C/I:C/A:C", 7.2},
		{"AV:N/AC:L/Au:S/C:P/I:P/A:P", 6.5},
		{"AV:A/AC:L/Au:N/C:N/I:N/A:C", 6.1},
		{"(AV:N/AC:L/Au:N/C:P/I:P/A:P)", 7.5},
		{"AV:N/AC:L/Au:N/C:N/I:N/A:N", 0},
	}

	for _, tt := range tests {
		got, ok := cvss2BaseScore(tt.vector)
		if !ok || got != tt.want {
			t.Errorf("cvss2BaseScore(%q) = %v, %v; want %v", tt.vector, got, ok, tt.want)
		}
	}

	for _, bad := range []string{"", "7.5", "AV:N/AC:L/Au:N/C:P/I:P", "AV:X/AC:L/Au:N/C:P/I:P/A:P", "AV:N/AC:L/Au:N/C:P/I:P/A:P/AV:N", "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"} {
		if got, ok := cvss2BaseScore(bad); ok {
			t.Errorf("cvss2BaseScore(%q) = %v, true; want it rejected", bad, got)
		}
	}
}

const (
	v3Critical = "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
	v4Vector   = "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N"
)

func TestParseSeverityFromOSV_Fallbacks(t *testing.T) {
	tests := []struct {
		name     string
		severity osvSeverity
		db       string
		wantSev  string
		wantCVSS float64
	}{
		{"v3 wins over v2", osvSev("CVSS_V2", "AV:N/AC:L/Au:N/C:P/I:P/A:P", "CVSS_V3", v3Critical), "", "CRITICAL", 9.8},
		{"v2 only", osvSev("CVSS_V2", "AV:N/AC:L/Au:N/C:P/I:P/A:P"), "", "HIGH", 7.5},
		{"unscorable v3 falls back to v2", osvSev("CVSS_V3", "CVSS:3.1/AV:X", "CVSS_V2", "AV:N/AC:M/Au:N/C:N/I:N/A:P"), "", "MEDIUM", 4.3},
		{"v3 after an unscorable v4", osvSev("CVSS_V4", v4Vector, "CVSS_V3", v3Critical), "", "CRITICAL", 9.8},
		{"v4 with a numeric score", osvSev("CVSS_V4", "8.7"), "", "HIGH", 8.7},
		{"v4 numeric beats v2", osvSev("CVSS_V2", "AV:N/AC:L/Au:N/C:P/I:P/A:P", "CVSS_V4", "9.3"), "", "CRITICAL", 9.3},
		{"unscorable v4 vector falls back to v2", osvSev("CVSS_V4", v4Vector, "CVSS_V2", "AV:N/AC:L/Au:N/C:P/I:P/A:P"), "", "HIGH", 7.5},
		{"database severity still comes first", osvSev("CVSS_V2", "AV:N/AC:L/Au:N/C:P/I:P/A:P"), "LOW", "LOW", 2},
		{"nothing scorable", osvSev("CVSS_V4", v4Vector), "", "UNKNOWN", 0},
		{"a type OSV does not define is ignored", osvSev("CVSS_V9", "9.9"), "", "UNKNOWN", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sev, score := parseSeverityFromOSV(tt.severity, tt.db)
			if sev != tt.wantSev || score != tt.wantCVSS {
				t.Errorf("parseSeverityFromOSV = %s/%v, want %s/%v", sev, score, tt.wantSev, tt.wantCVSS)
			}
		})
	}
}

// pagedOSV answers POST /query with one page per id list and a next_page_token
// while pages remain; it records the page tokens it was asked for.
func pagedOSV(t *testing.T, pages [][]string, tokens *[]string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PageToken string `json:"page_token"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}

		*tokens = append(*tokens, req.PageToken)

		page := 0
		if req.PageToken != "" {
			if _, err := fmt.Sscanf(req.PageToken, "page-%d", &page); err != nil {
				http.Error(w, "bad token", http.StatusBadRequest)

				return
			}
		}

		vulns := make([]map[string]string, 0, len(pages[page]))
		for _, id := range pages[page] {
			vulns = append(vulns, map[string]string{"id": id})
		}

		body := map[string]any{"vulns": vulns}
		if page+1 < len(pages) {
			body["next_page_token"] = fmt.Sprintf("page-%d", page+1)
		}

		_ = json.NewEncoder(w).Encode(body)
	}))
}

func scannerFor(t *testing.T, srv *httptest.Server) *OSVScanner {
	t.Helper()

	log, _ := logger.New("error", "json", "stderr")
	s := NewOSVScanner(log)
	s.osvBaseURL = srv.URL
	s.httpClient = srv.Client()

	return s
}

// OSV answers a query in pages: everything after the first page used to be
// dropped without a trace.
func TestOSVScanner_FollowsNextPageToken(t *testing.T) {
	var tokens []string

	srv := pagedOSV(t, [][]string{{"GHSA-1", "GHSA-2"}, {"GHSA-3"}, {"GHSA-4"}}, &tokens)
	defer srv.Close()

	vulns, err := scannerFor(t, srv).Scan(context.Background(), leftPad("npm"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if got, want := vulnIDs(vulns), []string{"GHSA-1", "GHSA-2", "GHSA-3", "GHSA-4"}; !slices.Equal(got, want) {
		t.Errorf("vulnerabilities = %v, want %v", got, want)
	}

	if want := []string{"", "page-1", "page-2"}; !slices.Equal(tokens, want) {
		t.Errorf("page tokens sent = %v, want %v", tokens, want)
	}
}

// A feed that never stops sending a token cannot hold a scan forever: the
// scan fails instead of returning a truncated list as complete.
func TestOSVScanner_PageCap(t *testing.T) {
	var requests atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)

		_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-x"}],"next_page_token":"again"}`))
	}))
	defer srv.Close()

	_, err := scannerFor(t, srv).Scan(context.Background(), leftPad("npm"))
	if err == nil {
		t.Fatal("an endless next_page_token must fail the scan")
	}

	if se := asScanError(t, err); se.Kind != FailureUpstream {
		t.Errorf("kind = %q, want %q", se.Kind, FailureUpstream)
	}

	if got := requests.Load(); got != maxOSVPages {
		t.Errorf("requests = %d, want the cap %d", got, maxOSVPages)
	}
}

// A page that fails after the first fails the scan: the findings of the pages
// before it are not a complete answer.
func TestOSVScanner_LaterPageFailureFailsTheScan(t *testing.T) {
	var calls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-1"}],"next_page_token":"t"}`))

			return
		}

		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := scannerFor(t, srv).Scan(context.Background(), leftPad("npm"))
	if err == nil {
		t.Fatal("a failing second page must fail the scan")
	}

	if se := asScanError(t, err); se.Kind != FailureUpstream {
		t.Errorf("kind = %q, want %q", se.Kind, FailureUpstream)
	}
}
