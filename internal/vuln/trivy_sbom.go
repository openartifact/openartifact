// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"encoding/json"
	"strings"
)

// trivyOSFamily maps the id of a distro ("debian-12" is id "debian") to the
// operating system name Trivy knows it by. Trivy reads a deb or rpm from an
// SBOM only when the SBOM names the operating system, and only for the ones it
// has advisories for: it logs "Unsupported os" and finds nothing for fedora
// (measured with Trivy 0.76.1, openssl 3.0.8-1.fc38: 0 findings).
//
//nolint:gochecknoglobals // immutable lookup table
var trivyOSFamily = map[string]string{
	"debian": "debian",
	"ubuntu": "ubuntu",
	"rhel":   "redhat",
	"amzn":   "amazon",
}

// trivyOS returns the operating system name and version of a valid distro, and
// false when Trivy has no matching family.
func trivyOS(distro string) (string, string, bool) {
	id, version, found := strings.CutLast(distro, "-")
	if !found {
		return "", "", false
	}

	family, ok := trivyOSFamily[id]

	return family, version, ok
}

// trivySBOMSupports reports whether a deb or rpm can be scanned by Trivy: its
// distribution release must be known and one Trivy has a vulnerability source
// for.
func trivySBOMSupports(distro string) bool {
	if !validDistro(distro) {
		return false
	}

	_, _, ok := trivyOS(distro)

	return ok
}

// buildTrivySBOM returns a CycloneDX JSON document with the operating system of
// the package's distro and the package itself, which is what `trivy sbom` needs
// to match a deb or rpm. The package URL is the one grype takes, so both
// scanners see the same package.
func buildTrivySBOM(pkg Package) ([]byte, error) {
	if !trivySBOMSupports(pkg.Distro) {
		return nil, errDistroRequired
	}

	purl, err := buildPURL(pkg)
	if err != nil {
		return nil, err
	}

	osName, osVersion, _ := trivyOS(pkg.Distro)

	version := pkg.Version
	if strings.EqualFold(pkg.Ecosystem, "rpm") {
		_, version = rpmEVR(pkg)
	}

	type component struct {
		BOMRef  string `json:"bom-ref"` //nolint:tagliatelle // CycloneDX spelling
		Type    string `json:"type"`
		Name    string `json:"name"`
		Version string `json:"version"`
		PURL    string `json:"purl,omitempty"`
	}

	return json.Marshal(map[string]any{
		"bomFormat":   "CycloneDX",
		"specVersion": "1.5",
		"version":     1,
		"components": []component{
			{BOMRef: "os", Type: "operating-system", Name: osName, Version: osVersion},
			{BOMRef: "pkg", Type: "library", Name: pkg.Name, Version: version, PURL: purl},
		},
		"dependencies": []map[string]any{
			{"ref": "os", "dependsOn": []string{"pkg"}},
			{"ref": "pkg", "dependsOn": []string{}},
		},
	})
}
