// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

// CycloneDX spec versions the generator can emit.
const (
	CycloneDX14 = "1.4"
	CycloneDX15 = "1.5"
	CycloneDX16 = "1.6"

	// DefaultCycloneDX is the spec version used when a caller names none.
	DefaultCycloneDX = CycloneDX15
)

// CycloneDXVersionSupported reports whether v is a CycloneDX spec version the
// generator can emit.
func CycloneDXVersionSupported(v string) bool {
	return v == CycloneDX14 || v == CycloneDX15 || v == CycloneDX16
}

type cdxBOM struct {
	BOMFormat       string          `json:"bomFormat"`
	SpecVersion     string          `json:"specVersion"`
	SerialNumber    string          `json:"serialNumber"`
	Version         int             `json:"version"`
	Metadata        cdxMetadata     `json:"metadata"`
	Components      []cdxComponent  `json:"components"`
	Dependencies    []cdxDependency `json:"dependencies,omitempty"`
	Vulnerabilities []cdxVuln       `json:"vulnerabilities,omitempty"`
}

type cdxMetadata struct {
	Timestamp string        `json:"timestamp"`
	Tools     any           `json:"tools"`
	Component *cdxComponent `json:"component,omitempty"`
}

type cdxComponent struct {
	Type       string       `json:"type"`
	BOMRef     string       `json:"bom-ref,omitempty"`
	Name       string       `json:"name"`
	Version    string       `json:"version,omitempty"`
	PURL       string       `json:"purl,omitempty"`
	Hashes     []cdxHash    `json:"hashes,omitempty"`
	Licenses   []cdxLicense `json:"licenses,omitempty"`
	Properties []cdxProp    `json:"properties,omitempty"`
}

type cdxHash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type cdxProp struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// cdxLicense is one entry of a licenses array: a license (id or name) or an
// expression; exactly one of the two fields is set.
type cdxLicense struct {
	License    *cdxLicenseID `json:"license,omitempty"`
	Expression string        `json:"expression,omitempty"`
}

type cdxLicenseID struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type cdxDependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

type cdxSource struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type cdxRating struct {
	Source   *cdxSource `json:"source,omitempty"`
	Score    *float64   `json:"score,omitempty"`
	Severity string     `json:"severity"`
	Method   string     `json:"method,omitempty"`
}

type cdxAdvisory struct {
	URL string `json:"url"`
}

type cdxAffects struct {
	Ref string `json:"ref"`
}

type cdxAnalysis struct {
	State         string   `json:"state"`
	Justification string   `json:"justification,omitempty"`
	Response      []string `json:"response,omitempty"`
	Detail        string   `json:"detail,omitempty"`
}

type cdxVuln struct {
	BOMRef         string        `json:"bom-ref,omitempty"`
	ID             string        `json:"id"`
	Source         *cdxSource    `json:"source,omitempty"`
	Ratings        []cdxRating   `json:"ratings,omitempty"`
	CWEs           []int         `json:"cwes,omitempty"`
	Description    string        `json:"description,omitempty"`
	Recommendation string        `json:"recommendation,omitempty"`
	Advisories     []cdxAdvisory `json:"advisories,omitempty"`
	Published      string        `json:"published,omitempty"`
	Analysis       *cdxAnalysis  `json:"analysis,omitempty"`
	Affects        []cdxAffects  `json:"affects"`
}

var cweRe = regexp.MustCompile(`(?i)^(?:CWE-)?(\d{1,6})$`)

func (g *Generator) cdxTools(spec string) any {
	if spec == CycloneDX14 {
		return []map[string]string{{"vendor": "OpenArtifact", "name": "openartifact", "version": g.toolVersion}}
	}

	return map[string]any{"components": []map[string]string{
		{"type": "application", "author": "OpenArtifact", "name": "openartifact", "version": g.toolVersion},
	}}
}

func rootType(format string) string {
	switch strings.ToLower(format) {
	case "docker":
		return "container"
	case "raw":
		return "file"
	default:
		return "library"
	}
}

func (in *Input) cdxComponent() cdxComponent {
	c := cdxComponent{
		Type:     rootType(in.Format),
		BOMRef:   in.PURL,
		Name:     in.Name,
		PURL:     in.PURL,
		Licenses: cdxLicenses(in.Licenses),
		Properties: []cdxProp{
			{Name: "openartifact:format", Value: in.Format},
			{Name: "openartifact:artifact_id", Value: in.ArtifactID},
		},
	}

	if in.Version != "unknown" {
		c.Version = in.Version
	}

	if in.SHA256 != "" {
		c.Hashes = []cdxHash{{Alg: "SHA-256", Content: in.SHA256}}
	}

	if in.Quarantined || in.LicenseQuarantined {
		c.Properties = append(c.Properties, cdxProp{Name: "openartifact:quarantined", Value: "true"})
	}

	return c
}

// cdxLicenses renders license entries as a CycloneDX licenses array. The
// schema allows either license objects or one expression, so a lone compound
// expression (or an all-SPDX list with a compound one) becomes an expression
// and everything else license objects with an id or a name.
func cdxLicenses(entries []licenseEntry) []cdxLicense {
	if len(entries) == 0 {
		return nil
	}

	allValid, anyCompound := true, false

	for _, e := range entries {
		switch {
		case e.Expr == "":
			allValid = false
		case !e.simple():
			anyCompound = true
		}
	}

	if allValid && anyCompound {
		exprs := make([]string, len(entries))
		for i, e := range entries {
			exprs[i] = e.Expr
		}

		return []cdxLicense{{Expression: joinExpressions(exprs)}}
	}

	out := make([]cdxLicense, 0, len(entries))

	for _, e := range entries {
		if e.simple() && !strings.HasSuffix(e.Expr, "+") {
			out = append(out, cdxLicense{License: &cdxLicenseID{ID: e.Expr}})
			continue
		}

		name := e.Expr
		if name == "" {
			name = e.Name
		}

		out = append(out, cdxLicense{License: &cdxLicenseID{Name: name}})
	}

	return out
}

// GenerateCycloneDX builds the CycloneDX SBOM of an artifact: the artifact as
// the described component (purl, sha256 hash, licenses), its declared
// dependencies as components with a dependency graph, and the vulnerabilities
// of its latest scan with ratings, CWEs and the VEX analysis state.
// specVersion is "1.4", "1.5" or "1.6"; "" selects DefaultCycloneDX.
func (g *Generator) GenerateCycloneDX(ctx context.Context, repoID, artifactID, format string, metadata map[string]any, specVersion ...string) (*SBOMResult, error) {
	spec := DefaultCycloneDX
	if len(specVersion) > 0 && specVersion[0] != "" {
		spec = specVersion[0]
	}

	if !CycloneDXVersionSupported(spec) {
		return nil, fmt.Errorf("%w: CycloneDX %q", ErrUnsupportedSpec, spec)
	}

	in := newInput(format, artifactID, metadata)
	if err := g.load(ctx, repoID, in); err != nil {
		return nil, err
	}

	root := in.cdxComponent()
	bom := &cdxBOM{
		BOMFormat:    "CycloneDX",
		SpecVersion:  spec,
		SerialNumber: "urn:uuid:" + uuid.NewString(),
		Version:      1,
		Metadata: cdxMetadata{
			Timestamp: g.now().UTC().Format(time.RFC3339),
			Tools:     g.cdxTools(spec),
			Component: &root,
		},
		Components: []cdxComponent{},
	}

	refs := make([]string, 0, len(in.Deps))

	for _, d := range in.Deps {
		c := cdxComponent{Type: "library", BOMRef: d.PURL, Name: d.Name, PURL: d.PURL, Licenses: cdxLicenses(d.Licenses)}
		if d.Version != "" {
			c.Version = d.Version
		}

		if d.SHA256 != "" {
			c.Hashes = []cdxHash{{Alg: "SHA-256", Content: d.SHA256}}
		}

		bom.Components = append(bom.Components, c)
		refs = append(refs, d.PURL)
	}

	bom.Dependencies = append(bom.Dependencies, cdxDependency{Ref: root.BOMRef, DependsOn: refs})
	for _, r := range refs {
		bom.Dependencies = append(bom.Dependencies, cdxDependency{Ref: r})
	}

	bom.Vulnerabilities = in.cdxVulns(root.BOMRef, spec)

	return finishJSON("cyclonedx", spec, "application/json", bom, len(in.Deps))
}

// cdxVulns renders the findings of the artifact as CycloneDX vulnerabilities
// affecting ref, with the VEX status as the analysis.
func (in *Input) cdxVulns(ref, spec string) []cdxVuln {
	findings := in.Findings()
	if len(findings) == 0 {
		return nil
	}

	out := make([]cdxVuln, 0, len(findings))

	for _, f := range findings {
		out = append(out, f.cdxVuln(ref, spec))
	}

	return out
}

func vulnSource(id string) cdxSource {
	switch u := strings.ToUpper(id); {
	case strings.HasPrefix(u, "CVE-"):
		return cdxSource{Name: "NVD", URL: "https://nvd.nist.gov/vuln/detail/" + id}
	case strings.HasPrefix(u, "GHSA-"):
		return cdxSource{Name: "GITHUB", URL: "https://github.com/advisories/" + id}
	default:
		return cdxSource{Name: "OSV", URL: "https://osv.dev/vulnerability/" + id}
	}
}

// cdxSeverity maps a scanner severity onto the CycloneDX severity enum.
func cdxSeverity(s string) string {
	switch l := strings.ToLower(strings.TrimSpace(s)); l {
	case "critical", "high", "medium", "low", "info", "none":
		return l
	case "moderate":
		return "medium"
	default:
		return "unknown"
	}
}

// cdxAnalysisState maps a VEX status onto the CycloneDX impact analysis state.
func cdxAnalysisState(status string) string {
	switch status {
	case models.VEXAffected:
		return "exploitable"
	case models.VEXNotAffected:
		return "not_affected"
	case models.VEXFixed:
		return "resolved"
	default:
		return "in_triage"
	}
}

// cdxJustification maps the OpenVEX justification enum onto CycloneDX's.
func cdxJustification(j string) string {
	switch j {
	case models.VEXComponentNotPresent, models.VEXVulnerableCodeNotPresent:
		return "code_not_present"
	case models.VEXVulnerableCodeNotInExec:
		return "code_not_reachable"
	case models.VEXVulnerableCodeNotControl:
		return "requires_environment"
	case models.VEXInlineMitigationsExisting:
		return "protected_by_mitigating_control"
	default:
		return ""
	}
}

func (f Finding) cdxVuln(ref, spec string) cdxVuln {
	src := vulnSource(f.Vuln.ID)
	v := cdxVuln{
		BOMRef:      "vuln:" + f.Vuln.ID + ":" + ref,
		ID:          f.Vuln.ID,
		Source:      &src,
		Description: f.Vuln.Summary,
		Affects:     []cdxAffects{{Ref: ref}},
	}

	if f.Vuln.Severity != "" || f.Vuln.CVSS > 0 {
		r := cdxRating{Source: &src, Severity: cdxSeverity(f.Vuln.Severity)}
		if f.Vuln.CVSS > 0 {
			score := f.Vuln.CVSS
			r.Score = &score
			r.Method = "other"
		}

		v.Ratings = []cdxRating{r}
	}

	for _, c := range f.Vuln.CWEs {
		if m := cweRe.FindStringSubmatch(strings.TrimSpace(c)); m != nil {
			n, _ := strconv.Atoi(m[1])
			v.CWEs = append(v.CWEs, n)
		}
	}

	if f.Vuln.FixedIn != "" {
		v.Recommendation = "Upgrade to " + f.Vuln.FixedIn + " or later."
	}

	for _, u := range f.Vuln.References {
		if len(v.Advisories) >= maxAdvisories {
			break
		}

		if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
			v.Advisories = append(v.Advisories, cdxAdvisory{URL: u})
		}
	}

	if t, err := time.Parse(time.RFC3339, f.Vuln.PublishedAt); err == nil {
		v.Published = t.UTC().Format(time.RFC3339)
	}

	v.Analysis = f.cdxAnalysis(spec)

	return v
}

// maxAdvisories bounds the reference URLs copied into one vulnerability.
const maxAdvisories = 20

func (f Finding) cdxAnalysis(_ string) *cdxAnalysis {
	a := &cdxAnalysis{State: cdxAnalysisState(f.Status)}

	switch f.Status {
	case models.VEXNotAffected:
		a.Justification = cdxJustification(f.Justification)
		a.Detail = f.Impact
	case models.VEXAffected:
		a.Detail = f.Action
		if f.Vuln.FixedIn != "" {
			a.Response = []string{"update"}
		}
	case models.VEXFixed:
		a.Detail = f.Impact
	default:
		a.Detail = f.Impact
	}

	return a
}

// finishJSON marshals doc and wraps it into an SBOMResult carrying both the
// serialized bytes and the generic map form (the API's response body).
func finishJSON(format, spec, contentType string, doc any, components int) (*SBOMResult, error) {
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal %s document: %w", format, err)
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode %s document: %w", format, err)
	}

	return &SBOMResult{
		Format:      format,
		SpecVersion: spec,
		ContentType: contentType,
		Data:        data,
		Body:        body,
		Components:  components,
	}, nil
}
