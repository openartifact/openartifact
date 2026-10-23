// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sbom generates software bills of materials (CycloneDX 1.4-1.6, SPDX
// 2.3 JSON and tag-value) and VEX documents (OpenVEX 0.2.0, CycloneDX VEX)
// for artifacts from their stored properties, license and vulnerability scans
// and the admin VEX decisions.
package sbom

import (
	"errors"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ErrUnsupportedSpec is returned for a document format or spec version the generator cannot emit.
var ErrUnsupportedSpec = errors.New("unsupported SBOM spec version")

// Generator builds SBOM and VEX documents.
type Generator struct {
	db          database.Database
	log         *logger.Logger
	toolVersion string
	baseURL     string
	now         func() time.Time
}

// SBOMResult is a generated document. Body holds the exact bytes to store and
// serve; Data (JSON formats) and Text (tag-value) are the same content in the
// form the API returns.
type SBOMResult struct {
	Format      string
	SpecVersion string
	ContentType string
	Data        map[string]any
	Text        string
	Body        []byte
	Components  int
}

// NewGenerator creates a Generator; db may be nil (only the artifact's
// properties are then used, no scans, hashes or VEX decisions).
func NewGenerator(db database.Database, log *logger.Logger) *Generator {
	return &Generator{db: db, log: log, toolVersion: "dev", now: time.Now}
}

// WithToolVersion sets the OpenArtifact version recorded as the generating tool.
func (g *Generator) WithToolVersion(v string) *Generator {
	if v = strings.TrimSpace(spdxSafe(v)); v != "" {
		g.toolVersion = v
	}

	return g
}

// WithBaseURL sets the instance public URL used to mint document identifiers
// (SPDX documentNamespace, OpenVEX @id). Empty keeps the urn: fallback.
func (g *Generator) WithBaseURL(u string) *Generator {
	g.baseURL = strings.TrimRight(strings.TrimSpace(u), "/")

	return g
}

// docID returns a unique document identifier for kind ("spdxdocs", "vex"):
// <baseURL>/<kind>/<suffix>, or urn:openartifact:<kind>:<suffix> without a base URL.
func (g *Generator) docID(kind, suffix string) string {
	if g.baseURL == "" {
		return "urn:openartifact:" + kind + ":" + suffix
	}

	return g.baseURL + "/" + kind + "/" + suffix
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
