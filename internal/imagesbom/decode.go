// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package imagesbom ingests the SBOMs a container build publishes beside its
// image (cosign ".sbom" tags, cosign attestations, OCI referrers) into the
// SBOM reports of the docker repository, so the vulnerability and licence
// checks can run on the packages of an image.
package imagesbom

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/sbom"
)

const (
	// MaxDocumentBytes bounds one SBOM document (or the envelope around it).
	MaxDocumentBytes = 32 << 20

	// MaxComponents bounds the components of one SBOM. A document that names
	// more is refused instead of being scanned in part.
	MaxComponents = 100_000

	// maxUnwrap bounds the nesting of bundle, envelope, statement and
	// predicate wrappers.
	maxUnwrap = 4
)

// SBOM formats, the values of models.SBOMReport.Format.
const (
	FormatSPDX          = "spdx"
	FormatSPDXJSON      = "spdx-json"
	FormatSPDX3JSON     = "spdx3-json"
	FormatCycloneDX     = "cyclonedx"
	contentTypeSPDX     = "application/spdx+json"
	contentTypeSPDXText = "text/spdx"
	contentTypeCDX      = "application/vnd.cyclonedx+json"
	spdxPredicate       = "https://spdx.dev/Document"
	cdxPredicate        = "https://cyclonedx.org/bom"
	inTotoPayloadTyp    = "application/vnd.in-toto+json"
)

var (
	// ErrNotSBOM is returned for a document that is not an SBOM this package
	// reads.
	ErrNotSBOM = errors.New("imagesbom: not an SPDX or CycloneDX JSON document")
	// ErrTooLarge is returned for a document or a component list over the
	// bounds.
	ErrTooLarge = errors.New("imagesbom: SBOM is too large")
)

// Document is a decoded SBOM.
type Document struct {
	// Format is FormatSPDX (tag-value), FormatSPDXJSON, FormatSPDX3JSON or
	// FormatCycloneDX.
	Format string
	// SpecVersion is the version the document declares ("2.3", "1.5"), or "".
	SpecVersion string
	// ContentType is the media type of Body.
	ContentType string
	// PredicateType is the in-toto predicate type the document was wrapped in,
	// "" for a bare document.
	PredicateType string
	// Body is the SBOM document itself, without any envelope.
	Body []byte
	// Components are the packages the document names.
	Components []sbom.Component
}

// IsSBOMPredicate reports whether an in-toto predicate type carries an SPDX or
// CycloneDX document (the bare type or one with a version suffix).
func IsSBOMPredicate(t string) bool {
	return hasTypePrefix(t, spdxPredicate) || hasTypePrefix(t, cdxPredicate)
}

func hasTypePrefix(t, base string) bool {
	rest, ok := strings.CutPrefix(t, base)

	return ok && (rest == "" || rest[0] == '/' || rest[0] == '@' || rest[0] == '#')
}

// IsSBOMArtifactType reports whether an OCI artifactType or layer media type
// names an SBOM document ("application/spdx+json",
// "application/vnd.cyclonedx+json", "text/spdx").
func IsSBOMArtifactType(t string) bool {
	switch base, _, _ := strings.Cut(strings.ToLower(t), ";"); base {
	case contentTypeSPDX, contentTypeCDX, "text/spdx", "text/spdx+json", "application/vnd.cyclonedx", "application/vnd.cyclonedx+xml":
		return true
	}

	return false
}

// envelope is the union of the wrappers an SBOM travels in: a Sigstore bundle,
// a DSSE envelope, an in-toto statement and the cosign SPDX predicate wrapper.
//
//nolint:tagliatelle // the wire formats name their properties in camelCase and underscores
type envelope struct {
	DSSE          json.RawMessage `json:"dsseEnvelope"`
	Payload       string          `json:"payload"`
	PayloadType   string          `json:"payloadType"`
	Type          string          `json:"_type"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
	// Data holds the document of the cosign SPDX predicate: a JSON string or
	// an object.
	Data json.RawMessage `json:"Data"`

	SPDXVersion string          `json:"spdxVersion"`
	BOMFormat   string          `json:"bomFormat"`
	SpecVersion string          `json:"specVersion"`
	Context     json.RawMessage `json:"@context"`
}

// Decode reads an SBOM from data: a bare SPDX 2.x or 3.0 JSON or CycloneDX
// JSON document, or one inside an in-toto statement, a DSSE envelope or a
// Sigstore bundle. It never verifies a signature. It returns ErrNotSBOM for any
// other content, ErrTooLarge for a document over the bounds and a wrapped
// sbom.ErrUnreadable for a malformed one.
func Decode(data []byte) (*Document, error) {
	if len(data) > MaxDocumentBytes {
		return nil, ErrTooLarge
	}

	return decode(bytes.TrimSpace(data), "", 0)
}

func decode(data []byte, predicateType string, depth int) (*Document, error) {
	if depth > maxUnwrap {
		return nil, ErrNotSBOM
	}

	if depth == 0 && bytes.HasPrefix(data, []byte("SPDXVersion:")) {
		return build(data, FormatSPDX, strings.TrimPrefix(firstLine(data), "SPDXVersion: SPDX-"), contentTypeSPDXText, predicateType)
	}

	if len(data) == 0 || data[0] != '{' {
		return nil, ErrNotSBOM
	}

	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: %w", sbom.ErrUnreadable, err)
	}

	switch {
	case env.SPDXVersion != "":
		return build(data, FormatSPDXJSON, strings.TrimPrefix(env.SPDXVersion, "SPDX-"), contentTypeSPDX, predicateType)
	case strings.EqualFold(env.BOMFormat, "CycloneDX"):
		return build(data, FormatCycloneDX, env.SpecVersion, contentTypeCDX, predicateType)
	case isSPDX3Context(env.Context):
		return build(data, FormatSPDX3JSON, "3.0", contentTypeSPDX, predicateType)
	}

	next, nextType, err := env.inner(predicateType)
	if err != nil {
		return nil, err
	}

	if next == nil {
		return nil, ErrNotSBOM
	}

	return decode(bytes.TrimSpace(next), nextType, depth+1)
}

// inner returns the document nested in a wrapper, nil when the object is none.
func (e *envelope) inner(predicateType string) ([]byte, string, error) {
	switch {
	case len(e.DSSE) > 0:
		return e.DSSE, predicateType, nil
	case e.Payload != "":
		if e.PayloadType != "" && e.PayloadType != inTotoPayloadTyp {
			return nil, "", ErrNotSBOM
		}

		raw, err := base64.StdEncoding.DecodeString(e.Payload)
		if err != nil {
			if raw, err = base64.URLEncoding.DecodeString(e.Payload); err != nil {
				return nil, "", fmt.Errorf("%w: payload is not base64", sbom.ErrUnreadable)
			}
		}

		return raw, predicateType, nil
	case e.PredicateType != "" || len(e.Predicate) > 0:
		if !IsSBOMPredicate(e.PredicateType) {
			return nil, "", ErrNotSBOM
		}

		return e.Predicate, e.PredicateType, nil
	case len(e.Data) > 0:
		return unquote(e.Data), predicateType, nil
	}

	return nil, "", nil
}

// unquote returns the JSON text of a value that is either a JSON string
// holding a document or the document itself.
func unquote(raw json.RawMessage) []byte {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []byte(s)
	}

	return raw
}

func firstLine(data []byte) string {
	line, _, _ := bytes.Cut(data, []byte("\n"))

	return strings.TrimSpace(string(line))
}

func isSPDX3Context(raw json.RawMessage) bool {
	return len(raw) > 0 && bytes.Contains(raw, []byte("spdx.org/rdf/3."))
}

func build(body []byte, format, spec, contentType, predicateType string) (*Document, error) {
	comps, truncated, err := sbom.ReadComponents(bytes.NewReader(body), format, MaxComponents)
	if err != nil {
		return nil, fmt.Errorf("read components: %w", err)
	}

	if truncated {
		return nil, ErrTooLarge
	}

	return &Document{
		Format: format, SpecVersion: spec, ContentType: contentType, PredicateType: predicateType,
		Body: body, Components: comps,
	}, nil
}
