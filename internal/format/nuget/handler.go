// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/format/base"
)

const (
	maxNuGetPackageBytes = 100 << 20
	maxNuGetNuspecBytes  = 4 << 20
)

type NuGetHandler struct{}

func NewNuGetHandler() *NuGetHandler {
	return &NuGetHandler{}
}

func (h *NuGetHandler) Parse(ctx context.Context, r io.Reader) (map[string]any, error) {
	nuspec, err := parseNuspec(r)
	if err != nil {
		return nil, fmt.Errorf("failed to parse nuget package: %w", err)
	}

	metadata := nuspec.Metadata
	result := map[string]any{
		"id":                       metadata.ID,
		"version":                  metadata.Version,
		"title":                    metadata.Title,
		"description":              metadata.Description,
		"summary":                  metadata.Summary,
		"authors":                  metadata.Authors,
		"owners":                   metadata.Owners,
		"tags":                     metadata.Tags,
		"projectUrl":               metadata.ProjectURL,
		"licenseUrl":               metadata.LicenseURL,
		"licenseExpression":        metadata.License.Expression(),
		"iconUrl":                  metadata.IconURL,
		"requireLicenseAcceptance": metadata.RequireLicenseAcceptance,
		"minClientVersion":         metadata.MinClientVersion,
	}

	return result, nil
}

// Nuspec represents the .nuspec XML structure.
type Nuspec struct {
	XMLName  xml.Name `xml:"package"`
	Metadata NuspecMetadata
}

type NuspecMetadata struct {
	XMLName                  xml.Name           `xml:"metadata"`
	ID                       string             `xml:"id"`
	Version                  string             `xml:"version"`
	Title                    string             `xml:"title"`
	Description              string             `xml:"description"`
	Summary                  string             `xml:"summary"`
	Authors                  string             `xml:"authors"`
	Owners                   string             `xml:"owners"`
	Tags                     string             `xml:"tags"`
	ProjectURL               string             `xml:"projectUrl"`
	LicenseURL               string             `xml:"licenseUrl"`
	License                  NuspecLicense      `xml:"license"`
	IconURL                  string             `xml:"iconUrl"`
	RequireLicenseAcceptance string             `xml:"requireLicenseAcceptance"`
	MinClientVersion         string             `xml:"minClientVersion"`
	Dependencies             NuspecDependencies `xml:"dependencies"`
}

// NuspecLicense is the <license type="expression|file"> element. Only the
// "expression" type carries an SPDX expression; "file" names a file in the
// package.
type NuspecLicense struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// Expression returns the SPDX license expression, or "" when the nuspec
// declares no expression-type license.
func (l NuspecLicense) Expression() string {
	if !strings.EqualFold(strings.TrimSpace(l.Type), "expression") {
		return ""
	}

	return strings.TrimSpace(l.Text)
}

// NuspecDependency is a single <dependency id="..." version="..."/> entry.
type NuspecDependency struct {
	ID      string `xml:"id,attr"`
	Version string `xml:"version,attr"`
}

// NuspecDependencyGroup is a <dependencies><group> element, scoping a set
// of dependencies to one target framework moniker (TFM).
type NuspecDependencyGroup struct {
	TargetFramework string             `xml:"targetFramework,attr"`
	Dependencies    []NuspecDependency `xml:"dependency"`
}

// NuspecDependencies models <package><metadata><dependencies>, which may mix
// framework-scoped <group> elements with legacy ungrouped <dependency>
// elements (no target framework).
type NuspecDependencies struct {
	Groups       []NuspecDependencyGroup `xml:"group"`
	Dependencies []NuspecDependency      `xml:"dependency"`
}

// DependencyGroups converts the parsed <dependencies> element into the
// NuGet V3 registration catalogEntry "dependencyGroups" shape:
// https://learn.microsoft.com/en-us/nuget/api/registration-base-url-resource#package-dependency-group
func (d NuspecDependencies) DependencyGroups() []map[string]any {
	groups := make([]map[string]any, 0, len(d.Groups)+1)

	if len(d.Dependencies) > 0 {
		groups = append(groups, nuspecDependencyGroupEntry("", d.Dependencies))
	}

	for _, g := range d.Groups {
		groups = append(groups, nuspecDependencyGroupEntry(g.TargetFramework, g.Dependencies))
	}

	return groups
}

func nuspecDependencyGroupEntry(targetFramework string, deps []NuspecDependency) map[string]any {
	items := make([]map[string]any, len(deps))
	for i, dep := range deps {
		items[i] = map[string]any{"id": dep.ID, "range": dep.Version}
	}

	entry := map[string]any{"dependencies": items}
	if targetFramework != "" {
		entry["targetFramework"] = targetFramework
	}

	return entry
}

// parseNuspec extracts and parses the .nuspec XML from a .nupkg (zip) file.
func parseNuspec(r io.Reader) (*Nuspec, error) {
	nuspecData, err := readNuspec(r)
	if err != nil {
		return nil, err
	}

	var nuspec Nuspec
	if err := xml.Unmarshal(nuspecData, &nuspec); err != nil {
		return nil, fmt.Errorf("failed to parse .nuspec XML: %w", err)
	}

	return &nuspec, nil
}

// isRootNuspec reports whether a zip entry name is a .nuspec at the package root.
func isRootNuspec(name string) bool {
	return len(name) > len(".nuspec") &&
		!strings.ContainsAny(name, `/\`) &&
		strings.EqualFold(name[len(name)-len(".nuspec"):], ".nuspec")
}

// readNuspec returns the raw .nuspec XML of a .nupkg (zip) file.
func readNuspec(r io.Reader) ([]byte, error) {
	// Zip reading needs random access: a reader that has it is used in place.
	ra, size, err := base.ReaderAtLimited(r, maxNuGetPackageBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read package: %w", err)
	}

	// Open as zip
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, fmt.Errorf("failed to open as zip: %w", err)
	}

	// Find the root-level .nuspec, the one NuGet clients read; a nested one is
	// package content and must not decide the package's identity.
	var nuspecFile *zip.File

	for _, f := range zr.File {
		if !isRootNuspec(f.Name) {
			continue
		}

		if nuspecFile != nil {
			// NuGet clients refuse a package with several root nuspecs.
			return nil, errors.New("multiple .nuspec files found in package") //nolint:err113 // static error
		}

		nuspecFile = f
	}

	if nuspecFile == nil {
		return nil, errors.New("no .nuspec file found in package") //nolint:err113 // static error
	}

	// Read and parse .nuspec
	rc, err := nuspecFile.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open .nuspec: %w", err)
	}
	defer func() { _ = rc.Close() }()

	nuspecData, err := base.ReadAllLimited(rc, maxNuGetNuspecBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read .nuspec: %w", err)
	}

	return nuspecData, nil
}
