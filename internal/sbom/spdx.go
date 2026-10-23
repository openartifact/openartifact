// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SPDX23 is the only SPDX spec version generated.
const SPDX23 = "2.3"

const noAssertion = "NOASSERTION"

// maxSPDXAdvisories bounds the advisory external references of one package.
const maxSPDXAdvisories = 100

type spdxDoc struct {
	SPDXVersion       string          `json:"spdxVersion"`
	DataLicense       string          `json:"dataLicense"`
	SPDXID            string          `json:"SPDXID"`
	Name              string          `json:"name"`
	DocumentNamespace string          `json:"documentNamespace"`
	CreationInfo      spdxCreation    `json:"creationInfo"`
	Packages          []spdxPackage   `json:"packages"`
	Relationships     []spdxRel       `json:"relationships"`
	ExtractedLicenses []spdxExtracted `json:"hasExtractedLicensingInfos,omitempty"`
}

type spdxCreation struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name             string       `json:"name"`
	SPDXID           string       `json:"SPDXID"`
	VersionInfo      string       `json:"versionInfo,omitempty"`
	DownloadLocation string       `json:"downloadLocation"`
	FilesAnalyzed    bool         `json:"filesAnalyzed"`
	Checksums        []spdxHash   `json:"checksums,omitempty"`
	LicenseConcluded string       `json:"licenseConcluded"`
	LicenseDeclared  string       `json:"licenseDeclared"`
	LicenseComments  string       `json:"licenseComments,omitempty"`
	CopyrightText    string       `json:"copyrightText"`
	Purpose          string       `json:"primaryPackagePurpose,omitempty"`
	ExternalRefs     []spdxExtRef `json:"externalRefs,omitempty"`
}

type spdxHash struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"checksumValue"`
}

type spdxExtRef struct {
	Category string `json:"referenceCategory"`
	Type     string `json:"referenceType"`
	Locator  string `json:"referenceLocator"`
}

type spdxRel struct {
	Element string `json:"spdxElementId"`
	Type    string `json:"relationshipType"`
	Related string `json:"relatedSpdxElement"`
}

type spdxExtracted struct {
	LicenseID     string `json:"licenseId"`
	ExtractedText string `json:"extractedText"`
	Name          string `json:"name"`
}

// spdxLicenseExpr renders entries as an SPDX license expression. Names that
// are not SPDX identifiers become LicenseRef-* ids recorded in extracted;
// with no entry the expression is NOASSERTION.
func spdxLicenseExpr(entries []licenseEntry, extracted *[]spdxExtracted) string {
	if len(entries) == 0 {
		return noAssertion
	}

	exprs := make([]string, 0, len(entries))

	for _, e := range entries {
		if e.Expr != "" {
			exprs = append(exprs, e.Expr)
			continue
		}

		id := licenseRefID(e.Name)

		exprs = append(exprs, id)

		known := false

		for _, x := range *extracted {
			if x.LicenseID == id {
				known = true
				break
			}
		}

		if !known {
			*extracted = append(*extracted, spdxExtracted{LicenseID: id, ExtractedText: spdxSafe(e.Name), Name: spdxSafe(e.Name)})
		}
	}

	return joinExpressions(exprs)
}

// licenseRefID derives a valid SPDX LicenseRef id (letters, digits, '.', '-')
// from a free-form license name.
func licenseRefID(name string) string {
	var b strings.Builder

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		default:
			if s := b.String(); s != "" && !strings.HasSuffix(s, "-") {
				b.WriteByte('-')
			}
		}

		if b.Len() >= 64 {
			break
		}
	}

	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "unnamed"
	}

	return "LicenseRef-" + id
}

func spdxPurpose(format string) string {
	switch rootType(format) {
	case "container":
		return "CONTAINER"
	case "file":
		return "FILE"
	default:
		return "LIBRARY"
	}
}

func (in *Input) spdxPackages() ([]spdxPackage, []spdxExtracted, []spdxRel) {
	var extracted []spdxExtracted

	const rootID = "SPDXRef-RootPackage"

	root := spdxPackage{
		Name:             spdxSafe(in.Name),
		SPDXID:           rootID,
		DownloadLocation: noAssertion,
		LicenseConcluded: spdxLicenseExpr(in.Licenses, &extracted),
		CopyrightText:    noAssertion,
		Purpose:          spdxPurpose(in.Format),
		ExternalRefs:     []spdxExtRef{{Category: "PACKAGE-MANAGER", Type: "purl", Locator: in.PURL}},
	}
	root.LicenseDeclared = root.LicenseConcluded

	if in.Version != "unknown" {
		root.VersionInfo = spdxSafe(in.Version)
	}

	if in.SHA256 != "" {
		root.Checksums = []spdxHash{{Algorithm: "SHA256", Value: in.SHA256}}
	}

	for i, f := range in.Findings() {
		if i >= maxSPDXAdvisories {
			break
		}

		root.ExternalRefs = append(root.ExternalRefs, spdxExtRef{
			Category: "SECURITY", Type: "advisory", Locator: vulnSource(spdxSafe(f.Vuln.ID)).URL,
		})
	}

	pkgs := []spdxPackage{root}
	rels := []spdxRel{{Element: "SPDXRef-DOCUMENT", Type: "DESCRIBES", Related: rootID}}

	for i, d := range in.Deps {
		id := fmt.Sprintf("SPDXRef-Package-%d", i+1)
		p := spdxPackage{
			Name:             spdxSafe(d.Name),
			SPDXID:           id,
			VersionInfo:      spdxSafe(d.Version),
			DownloadLocation: noAssertion,
			LicenseConcluded: spdxLicenseExpr(d.Licenses, &extracted),
			CopyrightText:    noAssertion,
			Purpose:          "LIBRARY",
			ExternalRefs:     []spdxExtRef{{Category: "PACKAGE-MANAGER", Type: "purl", Locator: d.PURL}},
		}
		p.LicenseDeclared = p.LicenseConcluded

		if d.SHA256 != "" {
			p.Checksums = []spdxHash{{Algorithm: "SHA256", Value: d.SHA256}}
		}

		pkgs = append(pkgs, p)
		rels = append(rels, spdxRel{Element: rootID, Type: "DEPENDS_ON", Related: id})
	}

	return pkgs, extracted, rels
}

func (g *Generator) spdxDocument(in *Input) *spdxDoc {
	pkgs, extracted, rels := in.spdxPackages()

	name := spdxSafe(in.Name)
	if in.Version != "unknown" {
		name += "-" + spdxSafe(in.Version)
	}

	return &spdxDoc{
		SPDXVersion:       "SPDX-" + SPDX23,
		DataLicense:       "CC0-1.0",
		SPDXID:            "SPDXRef-DOCUMENT",
		Name:              name,
		DocumentNamespace: g.docID("spdxdocs", url.PathEscape(name)+"-"+uuid.NewString()),
		CreationInfo: spdxCreation{
			Created:  g.now().UTC().Format(time.RFC3339),
			Creators: []string{"Tool: OpenArtifact-" + g.toolVersion},
		},
		Packages:          pkgs,
		Relationships:     rels,
		ExtractedLicenses: extracted,
	}
}

func (g *Generator) loadSPDX(ctx context.Context, repoID, artifactID, format string, metadata map[string]any) (*Input, error) {
	in := newInput(format, artifactID, metadata)
	if err := g.load(ctx, repoID, in); err != nil {
		return nil, err
	}

	return in, nil
}

// GenerateSPDXJSON generates the SPDX 2.3 JSON document of an artifact: the
// artifact and its declared dependencies as packages with sha256 checksums,
// licenses (SPDX expressions, LicenseRef-* for unrecognized names) and purl
// external references, linked by DESCRIBES / DEPENDS_ON relationships.
func (g *Generator) GenerateSPDXJSON(ctx context.Context, repoID, artifactID, format string, metadata map[string]any) (*SBOMResult, error) {
	in, err := g.loadSPDX(ctx, repoID, artifactID, format, metadata)
	if err != nil {
		return nil, err
	}

	return finishJSON("spdx-json", SPDX23, "application/spdx+json", g.spdxDocument(in), len(in.Deps))
}

// GenerateSPDX generates the SPDX 2.3 tag-value document of an artifact (same
// content as GenerateSPDXJSON).
func (g *Generator) GenerateSPDX(ctx context.Context, repoID, artifactID, format string, metadata map[string]any) (*SBOMResult, error) {
	in, err := g.loadSPDX(ctx, repoID, artifactID, format, metadata)
	if err != nil {
		return nil, err
	}

	text := renderTagValue(g.spdxDocument(in))

	return &SBOMResult{
		Format:      "spdx",
		SpecVersion: SPDX23,
		ContentType: "text/spdx",
		Text:        text,
		Body:        []byte(text),
		Components:  len(in.Deps),
	}, nil
}

func renderTagValue(d *spdxDoc) string {
	var b strings.Builder

	w := func(tag, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\n", tag, spdxSafe(value))
		}
	}

	w("SPDXVersion", d.SPDXVersion)
	w("DataLicense", d.DataLicense)
	w("SPDXID", d.SPDXID)
	w("DocumentName", d.Name)
	w("DocumentNamespace", d.DocumentNamespace)

	for _, c := range d.CreationInfo.Creators {
		w("Creator", c)
	}

	w("Created", d.CreationInfo.Created)

	for _, p := range d.Packages {
		b.WriteString("\n")
		w("PackageName", p.Name)
		w("SPDXID", p.SPDXID)
		w("PackageVersion", p.VersionInfo)
		w("PackageDownloadLocation", p.DownloadLocation)
		w("FilesAnalyzed", "false")

		for _, h := range p.Checksums {
			w("PackageChecksum", h.Algorithm+": "+h.Value)
		}

		w("PackageLicenseConcluded", p.LicenseConcluded)
		w("PackageLicenseDeclared", p.LicenseDeclared)
		w("PackageCopyrightText", p.CopyrightText)
		w("PrimaryPackagePurpose", p.Purpose)

		for _, r := range p.ExternalRefs {
			w("ExternalRef", r.Category+" "+r.Type+" "+r.Locator)
		}
	}

	b.WriteString("\n")

	for _, r := range d.Relationships {
		w("Relationship", r.Element+" "+r.Type+" "+r.Related)
	}

	for _, x := range d.ExtractedLicenses {
		b.WriteString("\n")
		w("LicenseID", x.LicenseID)
		fmt.Fprintf(&b, "ExtractedText: <text>%s</text>\n", strings.ReplaceAll(spdxSafe(x.ExtractedText), "</text>", ""))
		w("LicenseName", x.Name)
	}

	return b.String()
}
