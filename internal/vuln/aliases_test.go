// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

func TestCleanAliases(t *testing.T) {
	t.Parallel()

	many := make([]string, 0, 40)
	for i := range 40 {
		many = append(many, "CVE-2024-"+strings.Repeat("1", i+1))
	}

	tests := []struct {
		name    string
		id      string
		aliases []string
		want    []string
	}{
		{"none", "GHSA-a", nil, nil},
		{"own id, blanks and duplicates dropped", "GHSA-a", []string{"GHSA-a", " ", " CVE-2024-1 ", "CVE-2024-1", "CVE-2024-2"},
			[]string{"CVE-2024-1", "CVE-2024-2"}},
		{"capped", "GHSA-a", many, many[:maxAliases]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := cleanAliases(tt.id, tt.aliases); !slices.Equal(got, tt.want) {
				t.Errorf("cleanAliases = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOSVScanner_KeepsAliases(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-aaaa","aliases":["CVE-2021-44228","GHSA-aaaa"],"summary":"bad"}]}`))
	}))
	defer srv.Close()

	scanner := NewOSVScanner(log)
	scanner.osvBaseURL = srv.URL

	vulns, err := scanner.Scan(context.Background(), leftPad("npm"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(vulns) != 1 || !slices.Equal(vulns[0].Aliases, []string{"CVE-2021-44228"}) {
		t.Fatalf("vulns = %+v, want one finding aliased to its CVE", vulns)
	}
}

func TestGrypeParse_KeepsRelatedVulnerabilitiesAsAliases(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	scanner := &GrypeScanner{log: log}

	out := `{"matches":[{"vulnerability":{"id":"GHSA-xxxx-yyyy","severity":"High"},
		"relatedVulnerabilities":[{"id":"CVE-2022-0001"},{"id":"GHSA-xxxx-yyyy"}],
		"artifact":{"name":"left-pad","version":"1.0.0"}}]}`

	vulns, err := scanner.parseGrypeOutput([]byte(out))
	if err != nil {
		t.Fatalf("parseGrypeOutput: %v", err)
	}

	if len(vulns) != 1 || !slices.Equal(vulns[0].Aliases, []string{"CVE-2022-0001"}) {
		t.Fatalf("vulns = %+v, want one finding aliased to its CVE", vulns)
	}
}
