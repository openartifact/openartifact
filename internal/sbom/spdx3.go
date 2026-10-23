// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

// SPDX30 is the SPDX 3 spec version generated (JSON-LD serialization).
const SPDX30 = "3.0.1"

const (
	spdx3Context     = "https://spdx.org/rdf/3.0.1/spdx-context.jsonld"
	spdx3ContentType = "application/ld+json"
	// maxSPDX3Vulns bounds the vulnerabilities of one SPDX 3 document.
	maxSPDX3Vulns = 1000
)

// spdx3Builder assembles the @graph of one SPDX 3 document.
type spdx3Builder struct {
	ns      string // urn prefix shared by the document's elements
	graph   []map[string]any
	members []string // spdxIds listed in the SpdxDocument / Sbom "element"
	n       int
}

func (b *spdx3Builder) id(kind string) string {
	b.n++
	return b.ns + kind + "-" + strconv.Itoa(b.n)
}

// add appends an element carrying the document's CreationInfo and returns its spdxId.
func (b *spdx3Builder) add(el map[string]any, kind string) string {
	id := b.id(kind)
	el["spdxId"] = id
	el["creationInfo"] = "_:creationinfo"
	b.graph = append(b.graph, el)
	b.members = append(b.members, id)

	return id
}

func (b *spdx3Builder) relationship(from, relType string, to []string) {
	b.add(map[string]any{"type": "Relationship", "from": from, "relationshipType": relType, "to": to}, "rel")
}

// licenseElement returns the spdxId of the license element for entries,
// creating it on first use. Names that are not SPDX ids become a CustomLicense
// referenced from the expression through customIdToUri.
func (b *spdx3Builder) licenseElement(entries []licenseEntry, cache map[string]string) string {
	var (
		extracted []spdxExtracted
		key       = spdxLicenseExpr(entries, &extracted)
	)

	if id, ok := cache[key]; ok {
		return id
	}

	el := map[string]any{"type": "simplelicensing_LicenseExpression", "simplelicensing_licenseExpression": key}

	var custom []map[string]any

	for _, x := range extracted {
		lic := b.add(map[string]any{
			"type":                        "expandedlicensing_CustomLicense",
			"name":                        x.Name,
			"simplelicensing_licenseText": x.ExtractedText,
		}, "license")

		custom = append(custom, map[string]any{"type": "DictionaryEntry", "key": x.LicenseID, "value": lic})
	}

	if len(custom) > 0 {
		el["simplelicensing_customIdToUri"] = custom
	}

	id := b.add(el, "licexpr")
	cache[key] = id

	return id
}

func spdx3Purpose(format string) string {
	switch rootType(format) {
	case "container":
		return "container"
	case "file":
		return "file"
	default:
		return "library"
	}
}

func (b *spdx3Builder) pkg(name, version, purl, sha string, purpose string) string {
	el := map[string]any{
		"type":                    "software_Package",
		"name":                    spdxSafe(name),
		"software_primaryPurpose": purpose,
	}

	if version != "" && version != "unknown" {
		el["software_packageVersion"] = spdxSafe(version)
	}

	if purl != "" {
		el["software_packageUrl"] = purl
	}

	if sha != "" {
		el["verifiedUsing"] = []map[string]any{{"type": "Hash", "algorithm": "sha256", "hashValue": sha}}
	}

	return b.add(el, "pkg")
}

func spdx3VexRel(status string) (string, string) {
	switch status {
	case models.VEXNotAffected:
		return "security_VexNotAffectedVulnAssessmentRelationship", "doesNotAffect"
	case models.VEXFixed:
		return "security_VexFixedVulnAssessmentRelationship", "fixedIn"
	case models.VEXAffected:
		return "security_VexAffectedVulnAssessmentRelationship", "affects"
	default:
		return "security_VexUnderInvestigationVulnAssessmentRelationship", "underInvestigationFor"
	}
}

// spdx3Justification maps an OpenVEX justification onto the SPDX 3 enum.
func spdx3Justification(j string) string {
	switch j {
	case models.VEXComponentNotPresent:
		return "componentNotPresent"
	case models.VEXVulnerableCodeNotPresent:
		return "vulnerableCodeNotPresent"
	case models.VEXVulnerableCodeNotInExec:
		return "vulnerableCodeNotInExecutePath"
	case models.VEXVulnerableCodeNotControl:
		return "vulnerableCodeCannotBeControlledByAdversary"
	case models.VEXInlineMitigationsExisting:
		return "inlineMitigationsAlreadyExist"
	default:
		return ""
	}
}

func spdx3Time(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// vulnerabilities adds a security_Vulnerability per finding and the VEX
// assessment relationship (affects, doesNotAffect, fixedIn,
// underInvestigationFor) tying it to the root package.
func (b *spdx3Builder) vulnerabilities(in *Input, root string, now time.Time) {
	for i, f := range in.Findings() {
		if i >= maxSPDX3Vulns {
			break
		}

		src := vulnSource(spdxSafe(f.Vuln.ID))
		vuln := map[string]any{
			"type": "security_Vulnerability",
			"name": spdxSafe(f.Vuln.ID),
			"externalRef": []map[string]any{{
				"type": "ExternalRef", "externalRefType": "securityAdvisory", "locator": []string{src.URL},
			}},
		}

		if f.Vuln.Summary != "" {
			vuln["summary"] = spdxSafe(f.Vuln.Summary)
		}

		if t, err := time.Parse(time.RFC3339, f.Vuln.PublishedAt); err == nil {
			vuln["security_publishedTime"] = spdx3Time(t)
		}

		if idType := spdx3VulnIDType(f.Vuln.ID); idType != "" {
			vuln["externalIdentifier"] = []map[string]any{{
				"type": "ExternalIdentifier", "externalIdentifierType": idType, "identifier": spdxSafe(f.Vuln.ID),
			}}
		}

		vid := b.add(vuln, "vuln")

		typ, rel := spdx3VexRel(f.Status)
		assess := map[string]any{
			"type": typ, "from": vid, "relationshipType": rel, "to": []string{root},
			"security_assessedElement": root,
		}

		ts := f.UpdatedAt
		if ts.IsZero() {
			ts = now
		}

		assess["security_publishedTime"] = spdx3Time(ts)
		spdx3VexDetails(assess, f, ts)

		b.add(assess, "vex")
	}
}

func spdx3VexDetails(assess map[string]any, f Finding, ts time.Time) {
	switch f.Status {
	case models.VEXNotAffected:
		assess["security_justificationType"] = spdx3Justification(f.Justification)

		if f.Impact != "" {
			assess["security_impactStatement"] = spdxSafe(f.Impact)
			assess["security_impactStatementTime"] = spdx3Time(ts)
		}
	case models.VEXAffected:
		if f.Action != "" {
			assess["security_actionStatement"] = spdxSafe(f.Action)
			assess["security_actionStatementTime"] = spdx3Time(ts)
		}
	}
}

func spdx3VulnIDType(id string) string {
	switch vulnSource(id).Name {
	case "NVD":
		return "cve"
	default:
		return "securityOther"
	}
}

func (g *Generator) spdx3Document(in *Input) *spdx3Builder {
	now := g.now()
	b := &spdx3Builder{ns: "urn:spdx.dev:openartifact-" + uuid.NewString() + "-"}

	agent := b.id("agent")
	tool := b.id("tool")
	// The CreationInfo is a blank node shared by every element; the agent and
	// tool are elements of the graph but not "members" of the document.
	b.graph = append(b.graph,
		map[string]any{
			"type":         "CreationInfo",
			"@id":          "_:creationinfo",
			"specVersion":  SPDX30,
			"created":      spdx3Time(now),
			"createdBy":    []string{agent},
			"createdUsing": []string{tool},
		},
		map[string]any{"type": "SoftwareAgent", "spdxId": agent, "creationInfo": "_:creationinfo", "name": "OpenArtifact"},
		map[string]any{"type": "Tool", "spdxId": tool, "creationInfo": "_:creationinfo", "name": "OpenArtifact-" + g.toolVersion},
	)

	rootPkg := b.pkg(in.Name, in.Version, in.PURL, in.SHA256, spdx3Purpose(in.Format))
	rootRefs := []string{rootPkg}
	licCache := map[string]string{}

	if len(in.Licenses) > 0 {
		lic := b.licenseElement(in.Licenses, licCache)
		b.relationship(rootPkg, "hasConcludedLicense", []string{lic})
		b.relationship(rootPkg, "hasDeclaredLicense", []string{lic})
	}

	if len(in.Deps) > 0 {
		deps := make([]string, 0, len(in.Deps))

		for _, d := range in.Deps {
			id := b.pkg(d.Name, d.Version, d.PURL, d.SHA256, "library")
			deps = append(deps, id)

			if len(d.Licenses) > 0 {
				lic := b.licenseElement(d.Licenses, licCache)
				b.relationship(id, "hasConcludedLicense", []string{lic})
				b.relationship(id, "hasDeclaredLicense", []string{lic})
			}
		}

		b.relationship(rootPkg, "dependsOn", deps)
	}

	b.vulnerabilities(in, rootPkg, now)

	name := spdxSafe(in.Name)
	if in.Version != "unknown" {
		name += "-" + spdxSafe(in.Version)
	}

	sbomEl := b.id("sbom")
	b.graph = append(b.graph, map[string]any{
		"type": "software_Sbom", "spdxId": sbomEl, "creationInfo": "_:creationinfo", "name": name,
		"software_sbomType":  []string{"analyzed"},
		"rootElement":        rootRefs,
		"element":            append([]string(nil), b.members...),
		"profileConformance": []string{"core", "software", "simpleLicensing", "security"},
	})

	doc := b.id("document")
	b.graph = append(b.graph, map[string]any{
		"type": "SpdxDocument", "spdxId": doc, "creationInfo": "_:creationinfo", "name": name,
		"rootElement":        []string{sbomEl},
		"element":            append(append([]string(nil), b.members...), sbomEl),
		"profileConformance": []string{"core", "software", "simpleLicensing", "security"},
	})

	return b
}

// GenerateSPDX3JSON generates the SPDX 3.0.1 JSON-LD document of an artifact:
// an SpdxDocument holding a software Sbom whose root is the artifact, with its
// declared dependencies as Packages (purl, sha256, licenses as simplelicensing
// expressions) linked by dependsOn relationships, and the scan findings as
// security Vulnerabilities with their VEX assessment.
func (g *Generator) GenerateSPDX3JSON(ctx context.Context, repoID, artifactID, format string, metadata map[string]any) (*SBOMResult, error) {
	in, err := g.loadSPDX(ctx, repoID, artifactID, format, metadata)
	if err != nil {
		return nil, err
	}

	b := g.spdx3Document(in)

	return finishJSON("spdx3-json", SPDX30, spdx3ContentType, map[string]any{"@context": spdx3Context, "@graph": b.graph}, len(in.Deps))
}
