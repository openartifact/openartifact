// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attest

import "encoding/json"

// BundleMediaType is the Sigstore bundle (v0.3) media type.
const BundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"

// BundleContentType is the HTTP content type of a serialized bundle.
const BundleContentType = "application/vnd.dev.sigstore.bundle.v0.3+json"

type bundlePublicKey struct {
	Hint string `json:"hint"`
}

type bundleMaterial struct {
	PublicKey   bundlePublicKey `json:"publicKey"`
	TlogEntries []struct{}      `json:"tlogEntries"`
}

type bundle struct {
	MediaType            string         `json:"mediaType"`
	VerificationMaterial bundleMaterial `json:"verificationMaterial"`
	DSSEEnvelope         *Envelope      `json:"dsseEnvelope"`
}

// Bundle wraps a DSSE envelope signed with a bare public key into a Sigstore
// bundle, the only input `cosign verify-blob-attestation` (v3) reads. It holds
// no transparency log entry: verify it with --key and --insecure-ignore-tlog.
func (s *Signer) Bundle(env *Envelope) ([]byte, error) {
	return json.Marshal(bundle{
		MediaType: BundleMediaType,
		VerificationMaterial: bundleMaterial{
			PublicKey:   bundlePublicKey{Hint: s.keyID},
			TlogEntries: []struct{}{},
		},
		DSSEEnvelope: env,
	})
}
