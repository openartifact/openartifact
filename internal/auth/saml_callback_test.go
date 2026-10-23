// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"

	"github.com/openartifact/openartifact/internal/database/models"
)

const samlProtocolNS = "urn:oasis:names:tc:SAML:2.0:protocol"

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestPeekSAMLInResponseTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"unsolicited response", b64(`<samlp:Response xmlns:samlp="` + samlProtocolNS + `" ID="r1"/>`), "", true},
		{"solicited response", b64(`<samlp:Response xmlns:samlp="` + samlProtocolNS + `" InResponseTo="id-1"/>`), "id-1", true},
		{"default namespace", b64(`<Response xmlns="` + samlProtocolNS + `" InResponseTo="id-2"/>`), "id-2", true},
		{"leading xml declaration and comment", b64(`<?xml version="1.0"?><!-- c --><samlp:Response xmlns:samlp="` + samlProtocolNS + `" InResponseTo="id-3"/>`), "id-3", true},
		{"only the root counts, not an InResponseTo deeper down", b64(`<samlp:Response xmlns:samlp="` + samlProtocolNS + `"><x InResponseTo="id-4"/></samlp:Response>`), "", true},
		{"a namespaced lookalike attribute is not InResponseTo", b64(`<samlp:Response xmlns:samlp="` + samlProtocolNS + `" xmlns:evil="urn:evil" evil:InResponseTo="id-5"/>`), "", true},
		{"wrong namespace is not a Response", b64(`<Response xmlns="urn:evil"/>`), "", false},
		{"not a Response root", b64(`<samlp:AuthnRequest xmlns:samlp="` + samlProtocolNS + `"/>`), "", false},
		{"not base64", "%%%", "", false},
		{"not xml", b64("hello"), "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := peekSAMLInResponseTo(tt.in)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("peek = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSAMLProvider_ResponseMode(t *testing.T) {
	t.Parallel()

	unsolicited := b64(`<samlp:Response xmlns:samlp="` + samlProtocolNS + `"/>`)
	solicited := b64(`<samlp:Response xmlns:samlp="` + samlProtocolNS + `" InResponseTo="id-1"/>`)

	tests := []struct {
		name     string
		enabled  bool
		form     url.Values
		wantMode SAMLResponseMode
	}{
		{"disabled provider never routes to the IdP-initiated path", false, url.Values{"SAMLResponse": {unsolicited}}, SAMLSPInitiated},
		{"enabled, no InResponseTo", true, url.Values{"SAMLResponse": {unsolicited}}, SAMLIDPInitiated},
		{"enabled, InResponseTo present stays strict", true, url.Values{"SAMLResponse": {solicited}}, SAMLSPInitiated},
		{"enabled, undecodable payload stays strict", true, url.Values{"SAMLResponse": {"%%%"}}, SAMLSPInitiated},
		{"enabled, missing payload stays strict", true, url.Values{}, SAMLSPInitiated},
		{"enabled, artifact binding stays strict", true, url.Values{"SAMLResponse": {unsolicited}, "SAMLart": {"AAQAAA"}}, SAMLSPInitiated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &SAMLProvider{}
			if tt.enabled {
				p.idpInitiatedSP = &saml.ServiceProvider{}
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/acs", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			if got := p.ResponseMode(req); got != tt.wantMode {
				t.Errorf("ResponseMode = %v, want %v", got, tt.wantMode)
			}
		})
	}
}

func TestClassifySAMLParseError(t *testing.T) {
	t.Parallel()

	wrap := func(err error) error { return &saml.InvalidResponseError{PrivateErr: err} }
	unknown := errors.New("something else")

	tests := []struct {
		name string
		in   error
		want []error
		not  []error
	}{
		{"signature missing", wrap(fmt.Errorf("x: %w", saml.ErrSignatureMissing)), []error{ErrSAMLSignatureMissing, ErrSAMLSignatureInvalid}, nil},
		{"signature invalid", wrap(fmt.Errorf("x: %w", saml.ErrSignatureInvalid)), []error{ErrSAMLSignatureInvalid}, []error{ErrSAMLSignatureMissing}},
		{"audience", wrap(saml.ErrAudienceMismatch), []error{ErrSAMLAudienceMismatch}, []error{ErrSAMLSignatureInvalid}},
		{"expired", wrap(saml.ErrExpired), []error{ErrSAMLAssertionExpired}, []error{ErrSAMLAssertionNotYetValid}},
		{"not yet valid", wrap(saml.ErrNotYetValid), []error{ErrSAMLAssertionNotYetValid}, []error{ErrSAMLAssertionExpired}},
		{"InResponseTo", wrap(saml.ErrInResponseToMismatch), []error{ErrSAMLRequestMismatch}, nil},
		{"destination", wrap(saml.ErrDestinationMismatch), []error{ErrSAMLDestinationMismatch}, nil},
		{"recipient", wrap(saml.ErrRecipientMismatch), []error{ErrSAMLDestinationMismatch}, nil},
		{"issuer", wrap(saml.ErrIssuerMismatch), []error{ErrSAMLIssuerMismatch}, nil},
		{"bad status", wrap(saml.ErrBadStatus{Status: "urn:status:Requester"}), []error{ErrSAMLBadStatus}, nil},
		{"unrecognized stays unrecognized", wrap(unknown), []error{unknown}, []error{ErrSAMLSignatureInvalid, ErrSAMLAudienceMismatch, ErrSAMLAssertionExpired}},
		{"not an InvalidResponseError", unknown, []error{unknown}, []error{ErrSAMLSignatureInvalid}},
		{
			"message wording is irrelevant",
			wrap(errors.New("the signature is fine, audience and expired are just words")),
			nil, []error{ErrSAMLSignatureInvalid, ErrSAMLAudienceMismatch, ErrSAMLAssertionExpired},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classifySAMLParseError(tt.in)

			for _, w := range tt.want {
				if !errors.Is(got, w) {
					t.Errorf("errors.Is(%v, %v) = false", got, w)
				}
			}

			for _, n := range tt.not {
				if errors.Is(got, n) {
					t.Errorf("errors.Is(%v, %v) = true, want false", got, n)
				}
			}
		})
	}
}

func TestSAMLFingerprint_IgnoresValidityHintsOnly(t *testing.T) {
	t.Parallel()

	row := &models.SSOProvider{Slug: "s", SAMLConfig: []byte(`{}`), UpdatedAt: time.Unix(1000, 0)}
	cert := newTestCert(t, "a")
	other := newTestCert(t, "b")

	parse := func(doc string) *saml.EntityDescriptor {
		var md saml.EntityDescriptor
		if err := xml.Unmarshal([]byte(doc), &md); err != nil {
			t.Fatal(err)
		}

		return &md
	}

	base, err := samlFingerprint(row, parse(idpDoc("", cert)), "e")
	if err != nil {
		t.Fatal(err)
	}

	restamped, err := samlFingerprint(row, parse(idpDoc(`validUntil="2031-01-01T00:00:00Z" cacheDuration="PT10M"`, cert)), "e")
	if err != nil {
		t.Fatal(err)
	}

	if base != restamped {
		t.Error("a restamped validUntil/cacheDuration must not rebuild the provider")
	}

	rotated, err := samlFingerprint(row, parse(idpDoc("", other)), "e")
	if err != nil {
		t.Fatal(err)
	}

	if base == rotated {
		t.Error("a different signing certificate must change the fingerprint")
	}
}
