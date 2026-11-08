// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attestation

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"

	"github.com/openartifact/openartifact/internal/attest"
)

// Media types and annotations of the attachments this package reads.
const (
	mediaTypeDSSE         = attest.EnvelopeContentType
	mediaTypeInToto       = attest.PayloadType
	mediaTypeBundle       = attest.BundleMediaType
	mediaTypeSimpleSign   = "application/vnd.dev.cosign.simplesigning.v1+json"
	annotationSignature   = "dev.cosignproject.cosign/signature"
	annotationCertificate = "dev.sigstore.cosign/certificate"
	annotationChain       = "dev.sigstore.cosign/chain"
	annotationPredicate   = "predicateType"

	// PredicateSimpleSigning is the predicate type reported for cosign signatures.
	PredicateSimpleSigning = "https://sigstore.dev/cosign/sign/v1"

	// PredicateSLSAv02 is the SLSA provenance v0.2 predicate type that require_verified_provenance accepts.
	PredicateSLSAv02 = "https://slsa.dev/provenance/v0.2"
	// PredicateSLSAv1 is the SLSA provenance v1 predicate type that require_verified_provenance accepts.
	PredicateSLSAv1 = "https://slsa.dev/provenance/v1"
)

// descriptor is an OCI content descriptor.
type descriptor struct {
	MediaType   string            `json:"mediaType"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

// manifestDoc is the part of an OCI image manifest this package reads.
type manifestDoc struct {
	MediaType    string       `json:"mediaType"`    //nolint:tagliatelle // OCI image-spec wire format is camelCase
	ArtifactType string       `json:"artifactType"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
	Subject      *descriptor  `json:"subject"`
	Config       descriptor   `json:"config"`
	Layers       []descriptor `json:"layers"`
}

// parseManifest reads a manifest body; ok is false for anything that is not a
// JSON object.
func parseManifest(raw []byte) (manifestDoc, bool) {
	var m manifestDoc

	return m, json.Unmarshal(raw, &m) == nil
}

// artifactType is the artifact type of the manifest: the artifactType field, or
// the config media type of an artifact pushed the pre-1.1 way.
func (m *manifestDoc) artifactType() string {
	if m.ArtifactType != "" {
		return m.ArtifactType
	}

	return m.Config.MediaType
}

// bundleDoc is a Sigstore bundle (v0.3) as far as verification needs it.
type bundleDoc struct {
	MediaType            string `json:"mediaType"` //nolint:tagliatelle // Sigstore bundle wire format is camelCase
	VerificationMaterial struct {
		Certificate *struct {
			RawBytes []byte `json:"rawBytes"` //nolint:tagliatelle // Sigstore bundle wire format is camelCase
		} `json:"certificate"`
		X509CertificateChain *struct {
			Certificates []struct {
				RawBytes []byte `json:"rawBytes"` //nolint:tagliatelle // Sigstore bundle wire format is camelCase
			} `json:"certificates"`
		} `json:"x509CertificateChain"` //nolint:tagliatelle // Sigstore bundle wire format is camelCase
	} `json:"verificationMaterial"` //nolint:tagliatelle // Sigstore bundle wire format is camelCase
	DSSEEnvelope     *attest.Envelope `json:"dsseEnvelope"`     //nolint:tagliatelle // Sigstore bundle wire format is camelCase
	MessageSignature json.RawMessage  `json:"messageSignature"` //nolint:tagliatelle // Sigstore bundle wire format is camelCase
}

// material returns the certificates the bundle carries, leaf first.
func (b *bundleDoc) material() Material {
	var certs []*x509.Certificate

	vm := b.VerificationMaterial
	if vm.Certificate != nil {
		certs = appendDER(certs, vm.Certificate.RawBytes)
	}

	if vm.X509CertificateChain != nil {
		for _, c := range vm.X509CertificateChain.Certificates {
			certs = appendDER(certs, c.RawBytes)
		}
	}

	return materialOf(certs)
}

func appendDER(certs []*x509.Certificate, der []byte) []*x509.Certificate {
	if c, err := x509.ParseCertificate(der); err == nil {
		return append(certs, c)
	}

	return certs
}

func materialOf(certs []*x509.Certificate) Material {
	if len(certs) == 0 {
		return Material{}
	}

	return Material{Leaf: certs[0], Chain: certs[1:]}
}

// annotationMaterial reads the certificate and chain cosign (and Tekton
// Chains) put in the annotations of a layer.
func annotationMaterial(ann map[string]string) Material {
	certs := parsePEMCerts(ann[annotationCertificate])
	if len(certs) == 0 {
		return Material{}
	}

	return Material{Leaf: certs[0], Chain: append(certs[1:], parsePEMCerts(ann[annotationChain])...)}
}

func parsePEMCerts(doc string) []*x509.Certificate {
	var out []*x509.Certificate

	rest := []byte(strings.TrimSpace(doc))

	for {
		var blk *pem.Block

		blk, rest = pem.Decode(rest)
		if blk == nil {
			return out
		}

		if blk.Type != "CERTIFICATE" {
			continue
		}

		if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
			out = append(out, c)
		}
	}
}

// simpleSigning is the payload of a cosign signature layer.
type simpleSigning struct {
	Critical struct {
		Image struct {
			Digest string `json:"docker-manifest-digest"` //nolint:tagliatelle // cosign wire format
		} `json:"image"`
	} `json:"critical"`
}
