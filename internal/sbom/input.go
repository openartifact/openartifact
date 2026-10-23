// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/license"
)

// Dep is one dependency listed in the artifact's "dependencies" property.
type Dep struct {
	Name     string
	Version  string
	PURL     string
	SHA256   string
	Licenses []licenseEntry
}

// Input is everything the SBOM and VEX documents of one artifact are built
// from: the artifact identity, its stored properties, the latest license and
// vulnerability scans and the admin VEX decisions.
type Input struct {
	Format     string // repository format ("npm", "maven", ...)
	ArtifactID string
	Name       string
	Version    string
	PURL       string
	SHA256     string // "" when the artifact has no unambiguous stored blob
	Deps       []Dep
	Licenses   []licenseEntry

	// Scan is the latest completed (or partial) vulnerability scan, nil when
	// the artifact was never scanned successfully.
	Scan *models.VulnScanResult
	// Quarantined is set when the vulnerability scan quarantined the artifact.
	Quarantined bool
	// LicenseQuarantined is set when the license scan quarantined the artifact.
	LicenseQuarantined bool
	// Decisions are the admin VEX decisions keyed by upper-cased vulnerability id.
	Decisions map[string]*models.VEXStatement
}

// maxHashLookup bounds the package-listing rows read to resolve an artifact's hash.
const maxHashLookup = 50

// newInput builds the Input of an artifact from its stored properties
// (metadata, see api.storedArtifactMetadata); the scans are added by load.
func newInput(format, artifactID string, metadata map[string]any) *Input {
	name, version := splitArtifactID(artifactID)

	in := &Input{
		Format:     format,
		ArtifactID: artifactID,
		Name:       name,
		Version:    version,
		PURL:       generatePURL(format, name, version),
		Decisions:  map[string]*models.VEXStatement{},
	}

	in.Licenses = classifyLicenses(license.NewDetector(nil).DetectFromMetadata(metadata))

	deps, _ := metadata["dependencies"].([]any)
	seen := map[string]struct{}{}

	for _, d := range deps {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}

		dep := parseDep(format, m)
		if dep.Name == "" {
			continue
		}

		if _, dup := seen[dep.PURL]; dup {
			continue
		}

		seen[dep.PURL] = struct{}{}

		in.Deps = append(in.Deps, dep)
	}

	return in
}

func parseDep(format string, m map[string]any) Dep {
	str := func(k string) string {
		s, _ := m[k].(string)
		return strings.TrimSpace(spdxSafe(s))
	}

	dep := Dep{Name: str("name"), Version: str("version"), PURL: str("purl"), SHA256: strings.ToLower(str("sha256"))}
	if !isHexSHA256(dep.SHA256) {
		dep.SHA256 = ""
	}

	if dep.PURL == "" && dep.Name != "" {
		dep.PURL = generatePURL(format, dep.Name, dep.Version)
	}

	var raw []string

	for _, k := range []string{"license", "licenses"} {
		switch v := m[k].(type) {
		case string:
			raw = append(raw, v)
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok {
					raw = append(raw, s)
				}
			}
		}
	}

	dep.Licenses = classifyLicenses(raw)

	return dep
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}

	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// load adds the stored hash, license scan, vulnerability scan and VEX
// decisions of the artifact. With no database only the properties are used.
func (g *Generator) load(ctx context.Context, repoID string, in *Input) error {
	if g.db == nil {
		return nil
	}

	if err := g.loadHash(ctx, repoID, in); err != nil {
		return err
	}

	ls, err := g.db.GetLicenseScanResult(ctx, repoID, in.ArtifactID)
	if err != nil {
		return fmt.Errorf("load license scan: %w", err)
	}

	if ls != nil && ls.ScanStatus == "completed" {
		if scanned := classifyLicenses(ls.SPDXExpressions, ls.Licenses); len(scanned) > 0 {
			in.Licenses = scanned
		}

		in.LicenseQuarantined = ls.Quarantined
	}

	vs, err := g.db.GetVulnScanResult(ctx, repoID, in.ArtifactID)
	if err != nil {
		return fmt.Errorf("load vuln scan: %w", err)
	}

	if vs != nil && (vs.ScanStatus == "completed" || vs.ScanStatus == "partial") {
		in.Scan = vs
		in.Quarantined = vs.Quarantined
	}

	stmts, err := g.db.ListVEXStatements(ctx, repoID, in.ArtifactID)
	if err != nil {
		return fmt.Errorf("load vex statements: %w", err)
	}

	for _, s := range stmts {
		in.Decisions[strings.ToUpper(s.VulnID)] = s
	}

	return nil
}

// loadHash resolves the sha256 of the artifact's stored blob from the package
// listing. A name+version that maps to several distinct blobs (a deb or rpm
// built for several architectures) has no single hash and leaves it empty.
func (g *Generator) loadHash(ctx context.Context, repoID string, in *Input) error {
	if in.Version == "unknown" {
		return nil
	}

	searcher, ok := g.db.(database.ComponentSearcher)
	if !ok {
		return nil
	}

	comps, _, err := searcher.SearchComponents(ctx, repoID, database.ComponentListOptions{
		Name: in.Name, Query: in.Version, Page: 1, Limit: maxHashLookup,
	})
	if err != nil {
		return fmt.Errorf("resolve artifact hash: %w", err)
	}

	sum := ""

	for _, c := range comps {
		if c.Version != in.Version || c.SHA256 == "" {
			continue
		}

		if sum != "" && sum != c.SHA256 {
			return nil
		}

		sum = c.SHA256
	}

	in.SHA256 = strings.ToLower(sum)

	return nil
}
