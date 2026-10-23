// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
)

var whitespaceRE = regexp.MustCompile(`\s+`)

// idpSigningCerts returns every certificate the IdP metadata publishes for
// signing (KeyDescriptor use="signing" or unspecified), in document order. An
// IdP in the middle of a certificate rollover lists the old and the new one.
func idpSigningCerts(md *saml.EntityDescriptor) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate

	for _, desc := range md.IDPSSODescriptors {
		for _, kd := range desc.KeyDescriptors {
			if kd.Use != "" && kd.Use != "signing" {
				continue
			}

			for _, c := range kd.KeyInfo.X509Data.X509Certificates {
				der, err := base64.StdEncoding.DecodeString(whitespaceRE.ReplaceAllString(c.Data, ""))
				if err != nil {
					return nil, fmt.Errorf("decode idp signing certificate: %w", err)
				}

				cert, err := x509.ParseCertificate(der)
				if err != nil {
					return nil, fmt.Errorf("parse idp signing certificate: %w", err)
				}

				certs = append(certs, cert)
			}
		}
	}

	if len(certs) == 0 {
		return nil, errors.New("idp metadata has no signing certificate") //nolint:err113 // static error
	}

	return certs, nil
}

// idpCertVerifier verifies enveloped signatures against every signing
// certificate in the IdP metadata.
//
// The default verifier handles a signature that embeds its certificate
// (KeyInfo/X509Certificate): the certificate must equal one of the trusted
// ones. A signature without an embedded certificate, however, is only
// verifiable by the default verifier when exactly one certificate is trusted,
// which fails during a rollover. This verifier tries each trusted certificate
// in that case, so a signature by any certificate in the metadata verifies and
// one by a certificate that is not in the metadata does not.
type idpCertVerifier struct {
	certs []*x509.Certificate
}

// VerifySignature implements saml.SignatureVerifier.
func (v *idpCertVerifier) VerifySignature(vc *dsig.ValidationContext, el *etree.Element) error {
	if el.FindElement("./Signature/KeyInfo/X509Data/X509Certificate") != nil {
		if _, err := vc.Validate(el); err != nil {
			return fmt.Errorf("cannot validate signature on %s: %w", el.Tag, err)
		}

		return nil
	}

	var firstErr error

	for _, cert := range v.certs {
		single := dsig.NewDefaultValidationContext(&dsig.MemoryX509CertificateStore{Roots: []*x509.Certificate{cert}})
		single.IdAttribute = vc.IdAttribute
		single.Clock = vc.Clock

		_, err := single.Validate(el)
		if err == nil {
			return nil
		}

		if firstErr == nil {
			firstErr = err
		}
	}

	if firstErr == nil {
		firstErr = errors.New("no trusted signing certificate") //nolint:err113 // static error
	}

	return fmt.Errorf("cannot validate signature on %s: %w", el.Tag, firstErr)
}
