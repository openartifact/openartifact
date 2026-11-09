// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// errNotOpenVEX is returned by ParseOpenVEX for a JSON document that is not OpenVEX.
var errNotOpenVEX = errors.New("not an OpenVEX document")

// PeerOpenVEX builds the OpenVEX 0.2.0 document a repository serves to its
// federated peers: one statement per VEX statement, nothing derived from the
// scans. Each product is identified by ArtifactPURL of the repository format,
// the identity the pulling side computes for its copy of the artifact.
func (g *Generator) PeerOpenVEX(format string, stmts []*models.VEXStatement) (*SBOMResult, error) {
	now := g.now().UTC()
	doc := g.newOpenVEXDoc(now)
	in := &Input{Format: format}

	for _, s := range stmts {
		if s.VulnID == "" || !ValidVEXStatus(s.Status) {
			continue
		}

		purl := ArtifactPURL(format, s.ArtifactID)
		f := Finding{
			Vuln:          models.Vulnerability{ID: s.VulnID},
			Status:        s.Status,
			Justification: s.Justification,
			Impact:        s.ImpactStatement,
			Action:        s.ActionStatement,
			Decided:       true,
			SourceKind:    s.SourceKind,
			SourceName:    s.SourceName,
			SourceRef:     s.SourceRef,
			UpdatedAt:     s.UpdatedAt,
		}

		doc.Statements = append(doc.Statements, f.openVEX(in, openVEXProduct{ID: purl, Identifiers: map[string]string{"purl": purl}}, now))
	}

	return finishJSON(VEXOpenVEX, "0.2.0", "application/json", doc, len(doc.Statements))
}

// OpenVEXStatement is one usable statement of an OpenVEX document.
type OpenVEXStatement struct {
	VulnID          string
	PURLs           []string
	Status          string
	Justification   string
	ImpactStatement string
	ActionStatement string
	// Timestamp is the statement's timestamp, else the document's; nil when neither parses.
	Timestamp *time.Time
}

// ParseOpenVEX reads the statements of an OpenVEX document. The vulnerability
// id is vulnerability.name, else its @id; the products are their purl
// identifiers (or a purl @id). Statements without a vulnerability or product,
// with an unknown status or justification, or not_affected with neither a
// justification nor an impact statement are skipped.
func ParseOpenVEX(body []byte) ([]OpenVEXStatement, error) {
	var doc openVEXDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode OpenVEX document: %w", err)
	}

	if !strings.HasPrefix(doc.Context, "https://openvex.dev/ns") {
		return nil, errNotOpenVEX
	}

	docTime := parseRFC3339(doc.Timestamp)

	out := make([]OpenVEXStatement, 0, len(doc.Statements))

	for _, st := range doc.Statements {
		s, ok := openVEXStatement(st)
		if !ok {
			continue
		}

		if s.Timestamp = parseRFC3339(st.Timestamp); s.Timestamp == nil {
			s.Timestamp = docTime
		}

		out = append(out, s)
	}

	return out, nil
}

// openVEXStatement validates one statement; ok is false when it is unusable.
func openVEXStatement(st openVEXStmt) (OpenVEXStatement, bool) {
	s := OpenVEXStatement{
		VulnID:          strings.TrimSpace(st.Vulnerability.Name),
		Status:          st.Status,
		Justification:   st.Justification,
		ImpactStatement: st.ImpactStatement,
		ActionStatement: st.ActionStatement,
	}

	if s.VulnID == "" {
		s.VulnID = strings.TrimSpace(st.Vulnerability.ID)
	}

	switch {
	case s.VulnID == "", !ValidVEXStatus(s.Status):
		return s, false
	case s.Justification != "" && !ValidVEXJustification(s.Justification):
		return s, false
	case s.Status == models.VEXNotAffected && s.Justification == "" && s.ImpactStatement == "":
		return s, false
	}

	for _, p := range st.Products {
		purl := strings.TrimSpace(p.Identifiers["purl"])
		if purl == "" && strings.HasPrefix(p.ID, "pkg:") {
			purl = p.ID
		}

		if purl != "" && !slices.Contains(s.PURLs, purl) {
			s.PURLs = append(s.PURLs, purl)
		}
	}

	return s, len(s.PURLs) > 0
}

// parseRFC3339 parses an RFC 3339 timestamp; nil when s is not one.
func parseRFC3339(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}

	return &t
}
