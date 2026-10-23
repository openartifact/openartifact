// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

type sbomDoc struct {
	BOMFormat  string `json:"bomFormat"` //nolint:tagliatelle // CycloneDX spelling
	Components []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Version string `json:"version"`
		PURL    string `json:"purl"`
	} `json:"components"`
}

func TestBuildTrivySBOM(t *testing.T) {
	tests := []struct {
		name              string
		pkg               Package
		osName, osVersion string
		libVersion, purl  string
	}{
		{
			"debian 12",
			Package{Name: "openssl", Version: "3.0.11-1~deb12u2", Ecosystem: "deb", Distro: "debian-12"},
			"debian", "12", "3.0.11-1~deb12u2", "pkg:deb/debian/openssl@3.0.11-1~deb12u2?distro=debian-12",
		},
		{
			"ubuntu 22.04",
			Package{Name: "openssl", Version: "3.0.2-0ubuntu1.10", Ecosystem: "deb", Distro: "ubuntu-22.04"},
			"ubuntu", "22.04", "3.0.2-0ubuntu1.10", "pkg:deb/ubuntu/openssl@3.0.2-0ubuntu1.10?distro=ubuntu-22.04",
		},
		{
			"rhel 9 with epoch",
			Package{Name: "openssl", Version: "3.0.7-24.el9", Ecosystem: "rpm", Distro: "rhel-9", Epoch: 1},
			"redhat", "9", "3.0.7-24.el9", "pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9&epoch=1",
		},
		{
			"rpm release stored apart",
			Package{Name: "openssl", Version: "3.0.7", Release: "24.el9", Ecosystem: "rpm", Distro: "rhel-9"},
			"redhat", "9", "3.0.7-24.el9", "pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9",
		},
		{
			"amazon linux 2023",
			Package{Name: "openssl", Version: "3.0.8-1.amzn2023.0.1", Ecosystem: "rpm", Distro: "amzn-2023"},
			"amazon", "2023", "3.0.8-1.amzn2023.0.1", "pkg:rpm/amzn/openssl@3.0.8-1.amzn2023.0.1?distro=amzn-2023",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := buildTrivySBOM(tt.pkg)
			if err != nil {
				t.Fatal(err)
			}

			var doc sbomDoc
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}

			if doc.BOMFormat != "CycloneDX" || len(doc.Components) != 2 {
				t.Fatalf("unexpected document: %s", data)
			}

			if c := doc.Components[0]; c.Type != "operating-system" || c.Name != tt.osName || c.Version != tt.osVersion {
				t.Errorf("operating system = %+v, want %s %s", c, tt.osName, tt.osVersion)
			}

			if c := doc.Components[1]; c.Type != "library" || c.Name != tt.pkg.Name || c.Version != tt.libVersion || c.PURL != tt.purl {
				t.Errorf("library = %+v, want version %s purl %s", c, tt.libVersion, tt.purl)
			}
		})
	}
}

func TestBuildTrivySBOM_Refused(t *testing.T) {
	for _, distro := range []string{"", "bookworm", "fedora-39", "sles-15"} {
		_, err := buildTrivySBOM(Package{Name: "openssl", Version: "1-1", Ecosystem: "deb", Distro: distro})
		if !errors.Is(err, errDistroRequired) {
			t.Errorf("distro %q: err = %v, want errDistroRequired", distro, err)
		}
	}
}

// A deb or rpm reaches `trivy sbom` with the SBOM as its last argument; one
// without a usable distro never starts the binary.
func TestTrivyScanner_DebRPM(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	const findings = `{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-2024-1","Severity":"HIGH"}]}]}`

	t.Run("sbom is scanned", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "args")
		sbom := filepath.Join(filepath.Dir(out), "sbom.json")
		installFakeScanner(t, "trivy", `echo "$1" > `+out+`; for a; do last=$a; done; cp "$last" `+sbom+`; echo '`+findings+`'`)

		pkg := Package{Name: "openssl", Version: "3.0.7-24.el9", Ecosystem: "rpm", Distro: "rhel-9", Epoch: 1}

		vulns, err := testRegistry(t, log).Backend("trivy").Scan(context.Background(), pkg)
		if err != nil {
			t.Fatal(err)
		}

		if ids := vulnIDs(vulns); len(ids) != 1 || ids[0] != "CVE-2024-1" {
			t.Errorf("findings = %v", ids)
		}

		if sub, _ := os.ReadFile(out); strings.TrimSpace(string(sub)) != "sbom" {
			t.Errorf("trivy subcommand = %q, want sbom", sub)
		}

		data, err := os.ReadFile(sbom)
		if err != nil || !strings.Contains(string(data), `"operating-system"`) || !strings.Contains(string(data), "epoch=1") {
			t.Errorf("sbom = %s (%v)", data, err)
		}
	})

	for _, distro := range []string{"", "fedora-39"} {
		t.Run("distro "+distro+" is refused", func(t *testing.T) {
			installFakeScanner(t, "trivy", "echo '"+findings+"'")

			_, err := testRegistry(t, log).Backend("trivy").Scan(context.Background(),
				Package{Name: "openssl", Version: "3.0.7-24.el9", Ecosystem: "rpm", Distro: distro})
			if se := asScanError(t, err); se.Kind != FailureUnsupported {
				t.Errorf("kind = %q, want %q", se.Kind, FailureUnsupported)
			}
		})
	}
}
