// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

type Generator struct {
	db  database.Database
	log *logger.Logger
}

type SBOMResult struct {
	Format      string
	SpecVersion string
	ContentType string
	Data        map[string]any
	Text        string
	Components  int
}

func NewGenerator(db database.Database, log *logger.Logger) *Generator {
	return &Generator{db: db, log: log}
}

// GenerateCycloneDX generates a CycloneDX 1.4 SBOM for an artifact.
func (g *Generator) GenerateCycloneDX(ctx context.Context, repoID, artifactID, format string, metadata map[string]any) (*SBOMResult, error) {
	// Generate a UUID for serialNumber
	serialNumber := generateUUID()

	// Extract artifact name and version from artifactID (format: "name@version")
	name, version := splitArtifactID(artifactID)

	// Generate PURL
	purl := generatePURL(format, name, version)

	// Build the CycloneDX structure
	sbom := map[string]any{
		"bomFormat":    "CycloneDX",
		"specVersion":  "1.4",
		"serialNumber": "urn:uuid:" + serialNumber,
		"version":      1,
		"metadata": map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"tools": []map[string]any{
				{
					"vendor":  "OpenArtifact",
					"name":    "openartifact",
					"version": "1.0.0",
				},
			},
			"component": map[string]any{
				"type":    "library",
				"name":    name,
				"version": version,
				"purl":    purl,
			},
		},
		"components": []map[string]any{},
	}

	// Extract components from metadata if available
	componentCount := 0

	if deps, ok := metadata["dependencies"].([]any); ok {
		components := make([]map[string]any, 0)

		for _, dep := range deps {
			if depMap, ok := dep.(map[string]any); ok {
				depName, _ := depMap["name"].(string)
				depVersion, _ := depMap["version"].(string)
				depName, depVersion = spdxSafe(depName), spdxSafe(depVersion)
				depName, depVersion = spdxSafe(depName), spdxSafe(depVersion)
				if depName != "" {
					depPURL := generatePURL(format, depName, depVersion)
					components = append(components, map[string]any{
						"type":    "library",
						"name":    depName,
						"version": depVersion,
						"purl":    depPURL,
					})
					componentCount++
				}
			}
		}

		sbom["components"] = components
	}

	return &SBOMResult{
		Format:      "cyclonedx",
		SpecVersion: "1.4",
		ContentType: "application/json",
		Data:        sbom,
		Components:  componentCount,
	}, nil
}

// GenerateSPDX generates an SPDX 2.3 SBOM for an artifact.
func (g *Generator) GenerateSPDX(ctx context.Context, repoID, artifactID, format string, metadata map[string]any) (*SBOMResult, error) {
	// Generate a UUID for DocumentNamespace
	uuid := generateUUID()

	// Extract artifact name and version from artifactID (format: "name@version")
	name, version := splitArtifactID(artifactID)

	name, version, format = spdxSafe(name), spdxSafe(version), spdxSafe(format)

	// Build the SPDX text format
	var spdxText strings.Builder
	fmt.Fprintf(&spdxText, `SPDXVersion: SPDX-2.3
DataLicense: CC0-1.0
SPDXID: SPDXRef-DOCUMENT
DocumentName: %s-%s
DocumentNamespace: https://openartifact.io/sbom/%s
Creator: Tool: OpenArtifact
Created: %s

PackageName: %s
SPDXID: SPDXRef-Package
PackageVersion: %s
PackageDownloadLocation: NOASSERTION
FilesAnalyzed: false
PackageLicenseConcluded: NOASSERTION
PackageLicenseDeclared: NOASSERTION
PackageCopyrightText: NOASSERTION
`,
		name,
		version,
		uuid,
		time.Now().UTC().Format(time.RFC3339),
		name,
		version,
	)

	// Extract components from metadata if available
	componentCount := 0

	if deps, ok := metadata["dependencies"].([]any); ok {
		for _, dep := range deps {
			if depMap, ok := dep.(map[string]any); ok {
				depName, _ := depMap["name"].(string)
				depVersion, _ := depMap["version"].(string)
				depName, depVersion = spdxSafe(depName), spdxSafe(depVersion)
				if depName != "" {
					fmt.Fprintf(&spdxText, `
ExternalRef: SPDXRef-%s-%s
ExternalRefComment: Dependency
ExternalRefType: PACKAGE-MANAGER
ExternalRefLocator: pkg:%s/%s@%s
`,
						strings.ReplaceAll(depName, "/", "-"),
						strings.ReplaceAll(depVersion, ".", "-"),
						format,
						depName,
						depVersion,
					)

					componentCount++
				}
			}
		}
	}

	return &SBOMResult{
		Format:      "spdx",
		SpecVersion: "2.3",
		ContentType: "text/spdx",
		Text:        spdxText.String(),
		Components:  componentCount,
	}, nil
}

// generatePURL generates a Package URL (PURL) for an artifact.
func generatePURL(format, name, version string) string {
	// Normalize format to lowercase
	format = strings.ToLower(format)

	// Map format names to PURL types
	purlType := "generic"

	switch format {
	case "npm":
		purlType = "npm"
	case "maven":
		purlType = "maven"
	case "pypi":
		purlType = "pypi"
	case "cargo":
		purlType = "cargo"
	case "rubygems":
		purlType = "gem"
	case "helm":
		purlType = "helm"
	case "nuget":
		purlType = "nuget"
	case "docker":
		purlType = "docker"
	case "deb":
		purlType = "deb"
	case "rpm":
		purlType = "rpm"
	case "go":
		purlType = "golang"
	case "raw":
		purlType = "generic"
	}

	if version == "" {
		return fmt.Sprintf("pkg:%s/%s", purlType, name)
	}

	return fmt.Sprintf("pkg:%s/%s@%s", purlType, name, version)
}

// generateUUID generates a random UUID string.
func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to a timestamp-based ID if rand fails
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}

	// Format as UUID
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4],
		b[4:6],
		b[6:8],
		b[8:10],
		b[10:16],
	)
}

// splitArtifactID splits "name@version" on the LAST '@' after position 0 so
// scoped names such as "@babel/core@7.0.0" keep their scope.
func splitArtifactID(artifactID string) (string, string) {
	if i := strings.LastIndex(artifactID, "@"); i > 0 {
		return artifactID[:i], artifactID[i+1:]
	}

	return artifactID, "unknown"
}

// spdxSafe removes line breaks and control characters so untrusted values
// cannot inject additional tag lines into SPDX tag-value output.
func spdxSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029' {
			return -1
		}

		return r
	}, s)
}
