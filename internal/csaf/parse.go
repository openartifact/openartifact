// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package csaf

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
)

// docError is a document failure whose message is safe to show to an administrator.
type docError string

func (e docError) Error() string { return string(e) }

const (
	errNotJSON           docError = "not valid JSON"
	errCategory          docError = "unsupported document category (only csaf_vex and csaf_security_advisory)"
	errReleaseDate       docError = "missing or invalid document.tracking.current_release_date"
	categoryVEX                   = "csaf_vex"
	categoryAdvisory              = "csaf_security_advisory"
	threatImpact                  = "impact"
	remediationVendorFix          = "vendor_fix"
)

type csafDoc struct {
	Document struct {
		Category string `json:"category"`
		Tracking struct {
			ID                 string `json:"id"`
			CurrentReleaseDate string `json:"current_release_date"`
		} `json:"tracking"`
	} `json:"document"`
	ProductTree     productTree     `json:"product_tree"`
	Vulnerabilities []vulnerability `json:"vulnerabilities"`
}

type productTree struct {
	Branches         []branch          `json:"branches"`
	FullProductNames []fullProductName `json:"full_product_names"`
	Relationships    []relationship    `json:"relationships"`
	ProductGroups    []productGroup    `json:"product_groups"`
}

type branch struct {
	Branches []branch         `json:"branches"`
	Product  *fullProductName `json:"product"`
}

type fullProductName struct {
	ProductID string `json:"product_id"`
	Helper    *struct {
		PURL  string   `json:"purl"`
		PURLs []string `json:"purls"` // CSAF 2.1
		CPE   string   `json:"cpe"`
	} `json:"product_identification_helper"`
}

type relationship struct {
	ProductReference string          `json:"product_reference"`
	FullProductName  fullProductName `json:"full_product_name"`
}

type productGroup struct {
	GroupID    string   `json:"group_id"`
	ProductIDs []string `json:"product_ids"`
}

// productNote is a flag, threat or remediation: a text about some products.
type productNote struct {
	Label      string   `json:"label"`
	Category   string   `json:"category"`
	Details    string   `json:"details"`
	ProductIDs []string `json:"product_ids"`
	GroupIDs   []string `json:"group_ids"`
}

type vulnerability struct {
	CVE string `json:"cve"`
	IDs []struct {
		Text string `json:"text"`
	} `json:"ids"`
	Flags         []productNote `json:"flags"`
	Threats       []productNote `json:"threats"`
	Remediations  []productNote `json:"remediations"`
	ProductStatus struct {
		KnownNotAffected []string `json:"known_not_affected"`
		Fixed            []string `json:"fixed"`
		FirstFixed       []string `json:"first_fixed"`
	} `json:"product_status"`
}

// identity is how a product is identified for matching: its normalized
// versioned purls, else its CPE.
type identity struct {
	purls []string
	cpe   string
}

func (id identity) empty() bool { return len(id.purls) == 0 && id.cpe == "" }

// ParseDocument reads the not_affected and fixed statements of a CSAF 2.0
// csaf_vex or csaf_security_advisory document fetched from url. Products are
// identified by their purl (normalized with sbom.NormalizePURL; versionless
// or unparsable purls are dropped), else their CPE; a relationship product
// without identifiers takes those of its product_reference. known_not_affected
// becomes not_affected with the justification of the product's flag and the
// impact threat details, and is skipped when it has neither; fixed and
// first_fixed become fixed with the vendor_fix remediation details as action.
// The error's message is safe to show to an administrator.
func ParseDocument(body []byte, url string) (*Document, error) {
	var doc csafDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errNotJSON
	}

	if c := doc.Document.Category; c != categoryVEX && c != categoryAdvisory {
		return nil, errCategory
	}

	released, err := time.Parse(time.RFC3339, doc.Document.Tracking.CurrentReleaseDate)
	if err != nil {
		return nil, errReleaseDate
	}

	released = released.UTC()
	out := &Document{URL: url, TrackingID: doc.Document.Tracking.ID, Released: released}
	b := statementBuilder{
		products: doc.ProductTree.identities(),
		groups:   doc.ProductTree.groups(),
		base:     models.VEXFeedStatement{DocumentURL: url, TrackingID: out.TrackingID, ReleasedAt: &released},
		seen:     map[models.VEXFeedStatement]struct{}{},
	}

	for i := range doc.Vulnerabilities {
		out.Statements = b.add(out.Statements, &doc.Vulnerabilities[i])
	}

	return out, nil
}

// identities maps every product id of the tree to its identity.
func (t *productTree) identities() map[string]identity {
	ids := map[string]identity{}

	var walk func([]branch)
	walk = func(bs []branch) {
		for i := range bs {
			if p := bs[i].Product; p != nil {
				addIdentity(ids, p)
			}

			walk(bs[i].Branches)
		}
	}

	walk(t.Branches)

	for i := range t.FullProductNames {
		addIdentity(ids, &t.FullProductNames[i])
	}

	for i := range t.Relationships {
		r := &t.Relationships[i]
		if r.FullProductName.ProductID == "" {
			continue
		}

		id := productIdentity(&r.FullProductName)
		if id.empty() {
			id = ids[r.ProductReference]
		}

		ids[r.FullProductName.ProductID] = id
	}

	return ids
}

func addIdentity(ids map[string]identity, p *fullProductName) {
	if p.ProductID != "" {
		ids[p.ProductID] = productIdentity(p)
	}
}

func productIdentity(p *fullProductName) identity {
	var id identity

	if p.Helper == nil {
		return id
	}

	for _, raw := range append([]string{p.Helper.PURL}, p.Helper.PURLs...) {
		if purl, ok := sbom.NormalizePURL(raw); ok && !slices.Contains(id.purls, purl) {
			id.purls = append(id.purls, purl)
		}
	}

	if len(id.purls) == 0 {
		id.cpe = strings.TrimSpace(p.Helper.CPE)
	}

	return id
}

func (t *productTree) groups() map[string][]string {
	g := make(map[string][]string, len(t.ProductGroups))
	for _, pg := range t.ProductGroups {
		g[pg.GroupID] = append(g[pg.GroupID], pg.ProductIDs...)
	}

	return g
}

type statementBuilder struct {
	products map[string]identity
	groups   map[string][]string
	base     models.VEXFeedStatement
	seen     map[models.VEXFeedStatement]struct{}
}

// add appends the statements of one vulnerability to out.
func (b *statementBuilder) add(out []models.VEXFeedStatement, v *vulnerability) []models.VEXFeedStatement {
	vulnID := strings.TrimSpace(v.CVE)
	for i := 0; vulnID == "" && i < len(v.IDs); i++ {
		vulnID = strings.TrimSpace(v.IDs[i].Text)
	}

	if vulnID == "" {
		return out
	}

	justification := map[string]string{}

	for _, f := range v.Flags {
		if !sbom.ValidVEXJustification(f.Label) {
			continue
		}

		for _, pid := range b.expand(f) {
			if _, ok := justification[pid]; !ok {
				justification[pid] = f.Label
			}
		}
	}

	impact := b.details(v.Threats, threatImpact)

	for _, pid := range v.ProductStatus.KnownNotAffected {
		s := b.base
		s.VulnID, s.Status, s.Justification, s.ImpactStatement = vulnID, models.VEXNotAffected, justification[pid], impact[pid]

		if s.Justification != "" || s.ImpactStatement != "" {
			out = b.emit(out, pid, s)
		}
	}

	action := b.details(v.Remediations, remediationVendorFix)

	for _, pid := range slices.Concat(v.ProductStatus.Fixed, v.ProductStatus.FirstFixed) {
		s := b.base
		s.VulnID, s.Status, s.ActionStatement = vulnID, models.VEXFixed, action[pid]
		out = b.emit(out, pid, s)
	}

	return out
}

// details maps every product of the notes of category to their details,
// distinct texts joined.
func (b *statementBuilder) details(notes []productNote, category string) map[string]string {
	out := map[string]string{}

	for _, n := range notes {
		d := strings.TrimSpace(n.Details)
		if n.Category != category || d == "" {
			continue
		}

		for _, pid := range b.expand(n) {
			switch prev := out[pid]; {
			case prev == "":
				out[pid] = d
			case !strings.Contains(prev, d):
				out[pid] = prev + "\n" + d
			}
		}
	}

	return out
}

// expand lists the products a note is about: its product ids and the members of its groups.
func (b *statementBuilder) expand(n productNote) []string {
	out := slices.Clone(n.ProductIDs)
	for _, g := range n.GroupIDs {
		out = append(out, b.groups[g]...)
	}

	return out
}

// emit appends s once per identifier of product pid.
func (b *statementBuilder) emit(out []models.VEXFeedStatement, pid string, s models.VEXFeedStatement) []models.VEXFeedStatement {
	id := b.products[pid]

	push := func(s models.VEXFeedStatement) {
		key := s
		key.ReleasedAt = nil

		if _, dup := b.seen[key]; dup {
			return
		}

		b.seen[key] = struct{}{}
		out = append(out, s)
	}

	for _, purl := range id.purls {
		s.PURL = purl
		push(s)
	}

	if len(id.purls) == 0 && id.cpe != "" {
		s.CPE = id.cpe
		push(s)
	}

	return out
}
