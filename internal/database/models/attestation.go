// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Attestation kinds of OCIAttestation.Kind.
const (
	AttestationKindSignature   = "signature"
	AttestationKindAttestation = "attestation"
	AttestationKindSBOM        = "sbom"
)

// OCIAttestation is one signature, in-toto attestation or SBOM attached to a
// docker image, with the outcome of verifying it against the repository's
// attestation trust configuration.
type OCIAttestation struct {
	ID           string
	RepositoryID string
	// Image is the image inside the repository ("" for the default image).
	Image string
	// SubjectDigest is the manifest digest the envelope is about.
	SubjectDigest string
	// AttestationDigest is the digest ("sha256:<hex>") of the signed envelope.
	AttestationDigest string
	// ManifestID is the docker manifest that carries the envelope.
	ManifestID     string
	PredicateType  string
	Kind           string
	SignerIdentity string
	Issuer         string
	Verified       bool
	VerifyError    string
	// PayloadBlob is the blob store key of the envelope.
	PayloadBlob string
	// CreatedAt is when the envelope was first seen; on a write it is that
	// time too (an existing row keeps its own).
	CreatedAt time.Time
	UpdatedAt time.Time
}
