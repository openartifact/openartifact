// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vexsync

import (
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
)

// scanned is one artifact with the latest scan of it, ready for matching.
type scanned struct {
	repoID     string
	artifactID string
	version    string
	// vulns maps every id and alias the scan reports (foldID) to the scan's
	// own id of the vulnerability.
	vulns map[string]string
}

// scanID returns the scan's id of the vulnerability a statement names by its
// id or an alias; ok is false when the scan does not report it.
func (t *scanned) scanID(vulnID string) (string, bool) {
	id, ok := t.vulns[foldID(vulnID)]

	return id, ok
}

// targetIndex finds the scanned artifacts a feed statement speaks about: by
// normalized purl, or by package name for a CPE.
type targetIndex struct {
	byPURL map[string][]*scanned
	byName map[string][]*scanned
}

func newTargetIndex(targets []models.VEXTarget) *targetIndex {
	idx := &targetIndex{byPURL: make(map[string][]*scanned), byName: make(map[string][]*scanned)}

	for i := range targets {
		tg := &targets[i]
		name, version := splitArtifactID(tg.ArtifactID)
		t := &scanned{repoID: tg.RepositoryID, artifactID: tg.ArtifactID, version: version, vulns: scanVulns(tg.Vulns)}

		if p, ok := sbom.NormalizePURL(sbom.ArtifactPURL(tg.RepositoryFormat, tg.ArtifactID)); ok {
			idx.byPURL[p] = append(idx.byPURL[p], t)
		}

		for _, key := range nameKeys(name) {
			idx.byName[key] = append(idx.byName[key], t)
		}
	}

	return idx
}

func scanVulns(vulns []models.Vulnerability) map[string]string {
	m := make(map[string]string, len(vulns))

	for _, v := range vulns {
		if v.ID == "" {
			continue
		}

		m[foldID(v.ID)] = v.ID

		for _, alias := range v.Aliases {
			if _, taken := m[foldID(alias)]; !taken && alias != "" {
				m[foldID(alias)] = v.ID
			}
		}
	}

	return m
}

// materialize returns the statements of vex_statements the feed's statements
// yield: one per scanned artifact and reported vulnerability they match, named
// by the scan's id, the first statement winning.
func (idx *targetIndex) materialize(feed *models.VEXFeed, stmts []models.VEXFeedStatement) []*models.VEXStatement {
	type key struct{ repo, artifact, vuln string }

	seen := make(map[key]struct{})

	var out []*models.VEXStatement

	for i := range stmts {
		st := &stmts[i]

		for _, t := range idx.candidates(st) {
			vulnID, ok := t.scanID(st.VulnID)
			if !ok {
				continue
			}

			k := key{t.repoID, t.artifactID, vulnID}
			if _, dup := seen[k]; dup {
				continue
			}

			seen[k] = struct{}{}

			out = append(out, &models.VEXStatement{
				RepositoryID:    t.repoID,
				ArtifactID:      t.artifactID,
				VulnID:          vulnID,
				Status:          st.Status,
				Justification:   st.Justification,
				ImpactStatement: st.ImpactStatement,
				ActionStatement: st.ActionStatement,
				SourceKind:      models.VEXSourceCSAF,
				SourceName:      feed.Name,
				SourceRef:       st.DocumentURL,
				FeedID:          feed.ID,
			})
		}
	}

	return out
}

// candidates are the scanned artifacts a statement's product is: the artifacts
// with its purl, else those whose package name and version are the CPE's
// product and version.
func (idx *targetIndex) candidates(st *models.VEXFeedStatement) []*scanned {
	if st.PURL != "" {
		return idx.byPURL[st.PURL]
	}

	product, version, ok := parseCPE(st.CPE)
	if !ok {
		return nil
	}

	var out []*scanned

	for _, t := range idx.byName[foldName(product)] {
		if t.version == version {
			out = append(out, t)
		}
	}

	return out
}

// splitArtifactID splits "name@version" on the last '@' after the first
// character, like the SBOM generator, so "@babel/core@7.0.0" keeps its scope.
// An id without a version has version "".
func splitArtifactID(artifactID string) (string, string) {
	if i := strings.LastIndex(artifactID, "@"); i > 0 {
		return artifactID[:i], artifactID[i+1:]
	}

	return artifactID, ""
}

// nameKeys are the names a CPE product may give a package: its whole name and
// its last path or coordinate segment ("core" of "@babel/core", "commons-text"
// of "org.apache.commons:commons-text").
func nameKeys(name string) []string {
	keys := []string{foldName(name)}

	if i := strings.LastIndexAny(name, "/:"); i >= 0 && i < len(name)-1 {
		if last := foldName(name[i+1:]); last != keys[0] {
			keys = append(keys, last)
		}
	}

	return keys
}

// foldName is the form package names and CPE products are compared in:
// lower case, '_' and '-' equivalent.
func foldName(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", "-")
}

// parseCPE returns the product and version of an application CPE, in the 2.3
// formatted string binding ("cpe:2.3:a:vendor:product:version:...") or the 2.2
// URI binding ("cpe:/a:vendor:product:version"). ok is false for another part
// (o, h), or a CPE without a product or a specific version.
func parseCPE(cpe string) (string, string, bool) {
	var fields []string

	switch {
	case strings.HasPrefix(cpe, "cpe:2.3:"):
		fields = splitCPE23(strings.TrimPrefix(cpe, "cpe:2.3:"))
	case strings.HasPrefix(cpe, "cpe:/"):
		for f := range strings.SplitSeq(strings.TrimPrefix(cpe, "cpe:/"), ":") {
			v, err := url.PathUnescape(f)
			if err != nil {
				return "", "", false
			}

			fields = append(fields, v)
		}
	default:
		return "", "", false
	}

	if len(fields) < 4 || fields[0] != "a" {
		return "", "", false
	}

	product, version := fields[2], fields[3]
	if !specific(product) || !specific(version) {
		return "", "", false
	}

	return product, version, true
}

// splitCPE23 splits the fields of a 2.3 formatted string on the ':' not
// escaped with '\', and unescapes them.
func splitCPE23(s string) []string {
	var (
		fields []string
		cur    strings.Builder
	)

	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}

	return append(fields, cur.String())
}

// specific reports whether a CPE field names one value: not empty, ANY (*) or
// NA (-).
func specific(v string) bool {
	return v != "" && v != "*" && v != "-"
}
