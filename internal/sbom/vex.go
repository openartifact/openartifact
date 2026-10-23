// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

// VEX document formats.
const (
	VEXOpenVEX   = "openvex"
	VEXCycloneDX = "cyclonedx"

	// OpenVEXContext is the JSON-LD context of the OpenVEX version generated.
	OpenVEXContext = "https://openvex.dev/ns/v0.2.0"
)

// ValidVEXJustification reports whether j is an OpenVEX justification label.
func ValidVEXJustification(j string) bool {
	switch j {
	case models.VEXComponentNotPresent, models.VEXVulnerableCodeNotPresent, models.VEXVulnerableCodeNotInExec,
		models.VEXVulnerableCodeNotControl, models.VEXInlineMitigationsExisting:
		return true
	}

	return false
}

// ValidVEXStatus reports whether s is a VEX status label.
func ValidVEXStatus(s string) bool {
	switch s {
	case models.VEXAffected, models.VEXNotAffected, models.VEXFixed, models.VEXUnderInvestigation:
		return true
	}

	return false
}

// VEXInputs returns the inputs of every artifact of a repository that has a
// completed (or partial) vulnerability scan or a VEX decision, using the
// latest scan of each. Artifacts with neither are left out: a VEX document
// only speaks about vulnerabilities.
func (g *Generator) VEXInputs(ctx context.Context, repoID, format string) ([]*Input, error) {
	scans, err := g.db.ListVulnScanResults(ctx, repoID, false)
	if err != nil {
		return nil, fmt.Errorf("list vulnerability scans: %w", err)
	}

	stmts, err := g.db.ListVEXStatements(ctx, repoID, "")
	if err != nil {
		return nil, fmt.Errorf("list vex statements: %w", err)
	}

	byID := map[string]*Input{}

	var order []string

	get := func(artifactID string) *Input {
		in, ok := byID[artifactID]
		if !ok {
			in = newInput(format, artifactID, nil)
			byID[artifactID] = in
			order = append(order, artifactID)
		}

		return in
	}

	// ListVulnScanResults is newest first: the first scan of an artifact is its latest.
	scanned := map[string]struct{}{}

	for _, vs := range scans {
		if vs.ScanStatus != "completed" && vs.ScanStatus != "partial" {
			continue
		}

		if _, ok := scanned[vs.ArtifactID]; ok {
			continue
		}

		scanned[vs.ArtifactID] = struct{}{}

		in := get(vs.ArtifactID)
		in.Scan = vs
		in.Quarantined = vs.Quarantined
	}

	for _, s := range stmts {
		get(s.ArtifactID).Decisions[strings.ToUpper(s.VulnID)] = s
	}

	out := make([]*Input, 0, len(order))

	for _, id := range order {
		in := byID[id]
		if len(in.Findings()) == 0 {
			continue
		}

		if err := g.loadHash(ctx, repoID, in); err != nil {
			return nil, err
		}

		out = append(out, in)
	}

	return out, nil
}

// VEXInput returns the input of one artifact for a VEX document: its
// properties, latest scans and decisions.
func (g *Generator) VEXInput(ctx context.Context, repoID, format, artifactID string, metadata map[string]any) (*Input, error) {
	in := newInput(format, artifactID, metadata)
	if err := g.load(ctx, repoID, in); err != nil {
		return nil, err
	}

	return in, nil
}

// ---- OpenVEX 0.2.0 --------------------------------------------------------

type openVEXDoc struct {
	Context    string        `json:"@context"`
	ID         string        `json:"@id"`
	Author     string        `json:"author"`
	Role       string        `json:"role,omitempty"`
	Timestamp  string        `json:"timestamp"`
	Version    int           `json:"version"`
	Tooling    string        `json:"tooling,omitempty"`
	Statements []openVEXStmt `json:"statements"`
}

type openVEXStmt struct {
	Vulnerability   openVEXVuln      `json:"vulnerability"`
	Timestamp       string           `json:"timestamp,omitempty"`
	Products        []openVEXProduct `json:"products"`
	Status          string           `json:"status"`
	StatusNotes     string           `json:"status_notes,omitempty"`
	Justification   string           `json:"justification,omitempty"`
	ImpactStatement string           `json:"impact_statement,omitempty"`
	ActionStatement string           `json:"action_statement,omitempty"`
}

type openVEXVuln struct {
	ID          string `json:"@id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type openVEXProduct struct {
	ID          string            `json:"@id"`
	Identifiers map[string]string `json:"identifiers,omitempty"`
	Hashes      map[string]string `json:"hashes,omitempty"`
}

// GenerateOpenVEX builds an OpenVEX 0.2.0 document with one statement per
// (artifact, vulnerability) of the inputs. Statuses follow Input.Findings.
func (g *Generator) GenerateOpenVEX(inputs []*Input) (*SBOMResult, error) {
	now := g.now().UTC()
	doc := &openVEXDoc{
		Context:    OpenVEXContext,
		ID:         "https://openartifact.io/vex/" + uuid.NewString(),
		Author:     "OpenArtifact",
		Role:       "Document Creator",
		Timestamp:  now.Format(time.RFC3339),
		Version:    1,
		Tooling:    "OpenArtifact " + g.toolVersion,
		Statements: []openVEXStmt{},
	}

	count := 0

	for _, in := range inputs {
		prod := openVEXProduct{ID: in.PURL, Identifiers: map[string]string{"purl": in.PURL}}
		if in.SHA256 != "" {
			prod.Hashes = map[string]string{"sha-256": in.SHA256}
		}

		for _, f := range in.Findings() {
			doc.Statements = append(doc.Statements, f.openVEX(in, prod, now))
			count++
		}
	}

	return finishJSON(VEXOpenVEX, "0.2.0", "application/json", doc, count)
}

func (f Finding) openVEX(in *Input, prod openVEXProduct, now time.Time) openVEXStmt {
	src := vulnSource(f.Vuln.ID)
	st := openVEXStmt{
		Vulnerability: openVEXVuln{ID: src.URL, Name: f.Vuln.ID, Description: f.Vuln.Summary},
		Products:      []openVEXProduct{prod},
		Status:        f.Status,
		StatusNotes:   f.statusNotes(in),
	}

	ts := f.UpdatedAt
	if ts.IsZero() {
		ts = now
	}

	st.Timestamp = ts.UTC().Format(time.RFC3339)

	switch f.Status {
	case models.VEXNotAffected:
		st.Justification = f.Justification
		st.ImpactStatement = f.Impact
	case models.VEXAffected:
		st.ActionStatement = f.Action
	}

	return st
}

// statusNotes explains where the status comes from.
func (f Finding) statusNotes(in *Input) string {
	var notes []string

	switch {
	case f.Decided:
		notes = append(notes, "Recorded by an OpenArtifact administrator.")
		if f.Status != models.VEXNotAffected && f.Impact != "" {
			notes = append(notes, f.Impact)
		}
	case f.Status == models.VEXFixed:
		notes = append(notes, "The scan reports a fix in "+f.Vuln.FixedIn+"; this artifact is version "+in.Version+".")
	case f.Status == models.VEXUnderInvestigation:
		notes = append(notes, "Reported by the "+in.scanner()+" scan; no VEX decision has been recorded.")
	default:
		notes = append(notes, "Reported by the "+in.scanner()+" scan.")
	}

	if in.Quarantined {
		notes = append(notes, "The artifact is quarantined in OpenArtifact.")
	}

	return strings.Join(notes, " ")
}

func (in *Input) scanner() string {
	if in.Scan != nil && in.Scan.Scanner != "" {
		return strings.ReplaceAll(in.Scan.Scanner, ",", "+")
	}

	return "vulnerability"
}

// ---- CycloneDX VEX --------------------------------------------------------

// GenerateCycloneDXVEX builds a CycloneDX VEX document: the artifacts as
// components and one vulnerability per (artifact, vulnerability) with the VEX
// status as its analysis. A single input is described in metadata.component
// (a VEX for one package); several are listed as components (a repository VEX).
func (g *Generator) GenerateCycloneDXVEX(inputs []*Input, specVersion string) (*SBOMResult, error) {
	if specVersion == "" {
		specVersion = DefaultCycloneDX
	}

	if !CycloneDXVersionSupported(specVersion) {
		return nil, fmt.Errorf("%w: CycloneDX %q", ErrUnsupportedSpec, specVersion)
	}

	bom := &cdxBOM{
		BOMFormat:    "CycloneDX",
		SpecVersion:  specVersion,
		SerialNumber: "urn:uuid:" + uuid.NewString(),
		Version:      1,
		Metadata:     cdxMetadata{Timestamp: g.now().UTC().Format(time.RFC3339), Tools: g.cdxTools(specVersion)},
		Components:   []cdxComponent{},
	}

	count := 0

	for _, in := range inputs {
		c := in.cdxComponent()

		if len(inputs) == 1 {
			bom.Metadata.Component = &c
		} else {
			bom.Components = append(bom.Components, c)
		}

		vulns := in.cdxVulns(c.BOMRef, specVersion)
		bom.Vulnerabilities = append(bom.Vulnerabilities, vulns...)
		count += len(vulns)
	}

	return finishJSON(VEXCycloneDX, specVersion, "application/json", bom, count)
}
