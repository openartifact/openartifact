// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

func TestPackageFromPURL(t *testing.T) {
	tests := []struct {
		purl string
		want Package
		ok   bool
	}{
		{"pkg:npm/lodash@4.17.20", Package{Name: "lodash", Version: "4.17.20", Ecosystem: "npm"}, true},
		{"pkg:npm/%40angular/core@16.0.0", Package{Name: "@angular/core", Version: "16.0.0", Ecosystem: "npm"}, true},
		{"pkg:pypi/Django_Rest@3.0", Package{Name: "django-rest", Version: "3.0", Ecosystem: "pypi"}, true},
		{"pkg:golang/github.com/gin-gonic/gin@v1.9.0", Package{Name: "github.com/gin-gonic/gin", Version: "v1.9.0", Ecosystem: "go"}, true},
		{"pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1", Package{Name: "org.apache.logging.log4j:log4j-core", Version: "2.14.1",
			Ecosystem: "maven"}, true},
		{"pkg:cargo/serde@1.0.0", Package{Name: "serde", Version: "1.0.0", Ecosystem: "cargo"}, true},
		{"pkg:gem/rails@6.0.0", Package{Name: "rails", Version: "6.0.0", Ecosystem: "rubygems"}, true},
		{"pkg:nuget/Newtonsoft.Json@12.0.1", Package{Name: "Newtonsoft.Json", Version: "12.0.1", Ecosystem: "nuget"}, true},
		{"pkg:composer/symfony/console@5.0.0", Package{Name: "symfony/console", Version: "5.0.0", Ecosystem: "composer"}, true},
		{"pkg:deb/debian/openssl@3.0.7-1?arch=amd64&distro=debian-12", Package{Name: "openssl", Version: "3.0.7-1", Ecosystem: "deb",
			Distro: "debian-12"}, true},
		{"pkg:deb/ubuntu/curl@7.81.0-1ubuntu1.15~esm1", Package{Name: "curl", Version: "7.81.0-1ubuntu1.15~esm1", Ecosystem: "deb"}, true},
		{
			"pkg:rpm/redhat/openssl@3.0.7-27.el9?epoch=1&distro=redhat-9.2",
			Package{Name: "openssl", Version: "3.0.7-27.el9", Ecosystem: "rpm",
				Distro: "rhel-9.2", Epoch: 1},
			true,
		},
		{"pkg:rpm/centos/bash@5.1.8-6.el9", Package{Name: "bash", Version: "5.1.8-6.el9", Ecosystem: "rpm", Distro: "rhel-9"}, true},
		{"pkg:apk/alpine/musl@1.2.3-r0?distro=3.18", Package{}, false},
		{"pkg:generic/thing@1.0", Package{}, false},
		{"pkg:npm/lodash", Package{}, false},
		{"pkg:maven/log4j@1.2", Package{}, false},
		{"npm/lodash@1", Package{}, false},
		{"", Package{}, false},
		{"pkg:npm/%zz@1", Package{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.purl, func(t *testing.T) {
			got, ok := PackageFromPURL(tt.purl)
			if ok != tt.ok || got != tt.want {
				t.Errorf("PackageFromPURL(%q) = %+v, %v; want %+v, %v", tt.purl, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// byPackage answers a lookup by the package name.
type byPackage struct {
	vulns map[string][]models.Vulnerability
	fail  map[string]bool
}

func (byPackage) Name() string { return "osv" }

func (b byPackage) Scan(_ context.Context, pkg Package) ([]models.Vulnerability, error) {
	if b.fail[pkg.Name] {
		return nil, errors.New("upstream down")
	}

	return b.vulns[pkg.Name], nil
}

func TestRunComponents(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	backend := byPackage{
		vulns: map[string][]models.Vulnerability{
			"lodash": {{ID: "CVE-1", Severity: "HIGH"}},
			"django": {{ID: "CVE-1", Severity: "HIGH"}},
		},
		fail: map[string]bool{"broken": true},
	}

	tests := []struct {
		name        string
		purls       []string
		wantStatus  string
		wantFinding []string
		wantScanned int
		wantNote    string
	}{
		{
			name:        "findings are attributed to their package",
			purls:       []string{"pkg:npm/lodash@4.17.20", "pkg:pypi/django@2.0", "pkg:npm/clean@1.0.0"},
			wantStatus:  StatusCompleted,
			wantFinding: []string{"lodash@4.17.20", "django@2.0"},
			wantScanned: 3,
		},
		{
			name:        "a duplicate package is looked up once",
			purls:       []string{"pkg:npm/lodash@4.17.20", "pkg:npm/lodash@4.17.20"},
			wantStatus:  StatusCompleted,
			wantFinding: []string{"lodash@4.17.20"},
			wantScanned: 1,
		},
		{
			name:        "uncovered components are counted, not reported clean",
			purls:       []string{"pkg:npm/lodash@4.17.20", "pkg:apk/alpine/musl@1.2.3", ""},
			wantStatus:  StatusCompleted,
			wantFinding: []string{"lodash@4.17.20"},
			wantScanned: 1,
			wantNote:    "1 apk, 1 none",
		},
		{
			name:        "a failed lookup makes the scan partial",
			purls:       []string{"pkg:npm/lodash@4.17.20", "pkg:npm/broken@1.0.0"},
			wantStatus:  StatusPartial,
			wantFinding: []string{"lodash@4.17.20"},
			wantScanned: 2,
		},
		{
			name:        "all lookups failing fails the scan",
			purls:       []string{"pkg:npm/broken@1.0.0"},
			wantStatus:  StatusFailed,
			wantScanned: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := RunComponents(t.Context(), log, func(string) ScannerBackend { return backend }, []string{"osv"}, tt.purls)

			if out.Status != tt.wantStatus || out.Scanned != tt.wantScanned {
				t.Fatalf("status/scanned = %s/%d, want %s/%d", out.Status, out.Scanned, tt.wantStatus, tt.wantScanned)
			}

			var got []string
			for _, v := range out.Vulnerabilities {
				got = append(got, v.Package)
			}

			if strings.Join(got, ",") != strings.Join(tt.wantFinding, ",") {
				t.Errorf("finding packages = %v, want %v", got, tt.wantFinding)
			}

			if note := out.SkippedNote(); !strings.Contains(note, tt.wantNote) {
				t.Errorf("note = %q, want it to contain %q", note, tt.wantNote)
			}
		})
	}
}

func TestParseExternal(t *testing.T) {
	sarif := `{"version":"2.1.0","runs":[{"tool":{"driver":{"rules":[
		{"id":"CVE-2021-1","shortDescription":{"text":"bad bug"},"help":{"text":"Fixed Version: 1.2.4"},"properties":{"security-severity":"9.8"}},
		{"id":"CVE-2021-2","properties":{"tags":["vulnerability","MEDIUM"]}}]}},
		"results":[{"ruleId":"CVE-2021-1","level":"error","message":{"text":"x"},"locations":[{"logicalLocations":[{"name":"lodash@1.2.3"}]}]},
		{"ruleId":"CVE-2021-2","level":"warning","message":{"text":"y"}},
		{"ruleId":"CVE-2021-3","level":"note","message":{"text":"z"}}]}]}`

	osvScanner := `{"results":[{"packages":[{"package":{"name":"lodash","version":"4.17.20","ecosystem":"npm"},
		"vulnerabilities":[{"id":"GHSA-1","aliases":["CVE-9"],"summary":"s","database_specific":{"severity":"HIGH"}}]}]}]}`

	oa := `{"vulnerabilities":[{"id":"CVE-5","severity":"critical","cvss":99,"package":"a@1"},{"id":"","severity":"low"}]}`

	tests := []struct {
		name    string
		format  string
		in      string
		want    [][2]string // id, severity
		wantErr bool
	}{
		{
			"SARIF scores, tags and levels", ExternalSARIF, sarif,
			[][2]string{{"CVE-2021-1", "CRITICAL"}, {"CVE-2021-2", "MEDIUM"}, {"CVE-2021-3", "LOW"}}, false,
		},
		{"osv-scanner", ExternalOSVScanner, osvScanner, [][2]string{{"GHSA-1", "HIGH"}}, false},
		{"OA schema is normalized and drops id-less findings", ExternalOA, oa, [][2]string{{"CVE-5", "CRITICAL"}}, false},
		{"SARIF 1.x is refused", ExternalSARIF, `{"version":"1.0.0","runs":[]}`, nil, true},
		{"not JSON", ExternalOA, `nope`, nil, true},
		{"unknown format", "xml", `{}`, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExternal(tt.format, []byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if err != nil && !errors.Is(err, ErrExternalInvalid) {
				t.Errorf("err = %v, want ErrExternalInvalid", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("findings = %+v, want %v", got, tt.want)
			}

			for i, w := range tt.want {
				if got[i].ID != w[0] || got[i].Severity != w[1] {
					t.Errorf("finding %d = %s/%s, want %s/%s", i, got[i].ID, got[i].Severity, w[0], w[1])
				}
			}
		})
	}

	got, _ := ParseExternal(ExternalSARIF, []byte(sarif))
	if got[0].FixedIn != "1.2.4" || got[0].Package != "lodash@1.2.3" || got[0].Summary != "bad bug" {
		t.Errorf("SARIF finding = %+v", got[0])
	}

	if got[0].CVSS != 9.8 {
		t.Errorf("SARIF cvss = %v", got[0].CVSS)
	}

	cvss, _ := ParseExternal(ExternalOA, []byte(oa))
	if cvss[0].CVSS != 0 {
		t.Errorf("out-of-range cvss kept: %v", cvss[0].CVSS)
	}
}
