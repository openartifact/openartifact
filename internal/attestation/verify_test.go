// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attestation

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/attest"
)

const (
	testSubject = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSAN     = "https://kubernetes.io/namespaces/ci/serviceaccounts/chains"
	testIssuer  = "https://kubernetes.default.svc"
)

func compile(t *testing.T, cfg TrustConfig) *Trust {
	t.Helper()

	tr, err := cfg.Compile()
	if err != nil {
		t.Fatalf("compile trust: %v", err)
	}

	return tr
}

func decodeEnv(t *testing.T, raw []byte) *attest.Envelope {
	t.Helper()

	var env attest.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}

	return &env
}

func parseCert(t *testing.T, p string) *x509.Certificate {
	t.Helper()

	blk, _ := pem.Decode([]byte(p))
	if blk == nil {
		t.Fatal("no PEM block")
	}

	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestVerifyEnvelopeStaticKey(t *testing.T) {
	good, _ := newSigner(t)
	other, _ := newSigner(t)

	tampered := decodeEnv(t, provenanceEnvelope(t, good, testSubject))
	tampered.Payload = base64.StdEncoding.EncodeToString(
		[]byte(`{"_type":"https://in-toto.io/Statement/v0.1","subject":[],"predicateType":"x","predicate":{}}`),
	)

	tests := []struct {
		name     string
		trust    TrustConfig
		env      *attest.Envelope
		verified bool
		reason   string
	}{
		{"trusted key", TrustConfig{Keys: []string{pemPublic(t, good)}}, decodeEnv(t, provenanceEnvelope(t, good, testSubject)), true, ""},
		{
			"one of several keys",
			TrustConfig{Keys: []string{pemPublic(t, other), pemPublic(t, good)}},
			decodeEnv(t, provenanceEnvelope(t, good, testSubject)),
			true,
			"",
		},
		{
			"untrusted key",
			TrustConfig{Keys: []string{pemPublic(t, other)}},
			decodeEnv(t, provenanceEnvelope(t, good, testSubject)),
			false,
			"no trusted key",
		},
		{"tampered payload", TrustConfig{Keys: []string{pemPublic(t, good)}}, tampered, false, "no trusted key"},
		{"no trust configured", TrustConfig{}, decodeEnv(t, provenanceEnvelope(t, good, testSubject)), false, "trusts no key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var v Verifier

			out := v.VerifyEnvelope(compile(t, tc.trust), tc.env, Material{}, time.Now())
			if out.Verified != tc.verified {
				t.Fatalf("verified = %v (%s), want %v", out.Verified, out.Reason, tc.verified)
			}

			if !strings.Contains(out.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to contain %q", out.Reason, tc.reason)
			}

			if tc.verified && !strings.HasPrefix(out.Signer, "key:") {
				t.Fatalf("signer = %q, want a key id", out.Signer)
			}
		})
	}
}

func TestVerifyEnvelopeKeyless(t *testing.T) {
	ca := newTestCA(t)
	otherCA := newTestCA(t)
	now := time.Now()

	signer, certPEM := ca.issue(t, testSAN, testIssuer, now.Add(-time.Minute))
	rogue, roguePEM := otherCA.issue(t, testSAN, testIssuer, now.Add(-time.Minute))
	// A certificate of the right identity whose key did not make the signature.
	_, wrongKeyPEM := ca.issue(t, testSAN, testIssuer, now.Add(-time.Minute))

	trust := TrustConfig{Fulcio: &Fulcio{
		Roots:      []string{ca.pem},
		Identities: []Identity{{Issuer: testIssuer, SubjectRegexp: `https://kubernetes\.io/namespaces/ci/serviceaccounts/.*`}},
	}}

	envOf := func(s *attest.Signer) *attest.Envelope { return decodeEnv(t, provenanceEnvelope(t, s, testSubject)) }

	tests := []struct {
		name     string
		trust    TrustConfig
		env      *attest.Envelope
		cert     string
		at       time.Time
		verified bool
		reason   string
	}{
		{"trusted identity", trust, envOf(signer), certPEM, now, true, ""},
		{
			"subject mismatch",
			TrustConfig{
				Fulcio: &Fulcio{
					Roots:      []string{ca.pem},
					Identities: []Identity{{Issuer: testIssuer, Subject: "https://example.com/other"}},
				},
			},
			envOf(signer),
			certPEM,
			now,
			false,
			"identity is not trusted",
		},
		{
			"issuer mismatch",
			TrustConfig{
				Fulcio: &Fulcio{
					Roots:      []string{ca.pem},
					Identities: []Identity{{Issuer: "https://accounts.google.com", SubjectRegexp: ".*"}},
				},
			},
			envOf(signer),
			certPEM,
			now,
			false,
			"identity is not trusted",
		},
		{"foreign root", trust, envOf(rogue), roguePEM, now, false, "does not chain"},
		{"expired at signing time", trust, envOf(signer), certPEM, now.Add(time.Hour), false, "does not chain"},
		{"not yet valid", trust, envOf(signer), certPEM, now.Add(-time.Hour), false, "does not chain"},
		{"certificate of another key", trust, envOf(signer), wrongKeyPEM, now, false, "does not match the certificate"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var v Verifier

			mat := Material{Leaf: parseCert(t, tc.cert)}
			out := v.VerifyEnvelope(compile(t, tc.trust), tc.env, mat, tc.at)

			if out.Verified != tc.verified {
				t.Fatalf("verified = %v (%s), want %v", out.Verified, out.Reason, tc.verified)
			}

			if !strings.Contains(out.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to contain %q", out.Reason, tc.reason)
			}

			if out.Signer != testSAN || out.Issuer != testIssuer {
				t.Fatalf("identity = %q / %q, want the certificate's", out.Signer, out.Issuer)
			}
		})
	}
}

func TestTrustCompileRejects(t *testing.T) {
	signer, _ := newSigner(t)
	ca := newTestCA(t)
	id := Identity{Issuer: testIssuer, Subject: "x"}

	tests := []struct {
		name string
		cfg  TrustConfig
	}{
		{"key is not PEM", TrustConfig{Keys: []string{"nope"}}},
		{"key is a certificate", TrustConfig{Keys: []string{ca.pem}}},
		{"roots without identities", TrustConfig{Fulcio: &Fulcio{Roots: []string{ca.pem}}}},
		{"identities without roots", TrustConfig{Fulcio: &Fulcio{Identities: []Identity{id}}}},
		{"root is a key", TrustConfig{Fulcio: &Fulcio{Roots: []string{pemPublic(t, signer)}, Identities: []Identity{id}}}},
		{"identity without issuer", TrustConfig{Fulcio: &Fulcio{Roots: []string{ca.pem}, Identities: []Identity{{Subject: "x"}}}}},
		{"identity without subject", TrustConfig{Fulcio: &Fulcio{Roots: []string{ca.pem}, Identities: []Identity{{Issuer: testIssuer}}}}},
		{
			"identity with both",
			TrustConfig{
				Fulcio: &Fulcio{Roots: []string{ca.pem}, Identities: []Identity{{Issuer: testIssuer, Subject: "x", SubjectRegexp: "y"}}},
			},
		},
		{
			"bad regexp",
			TrustConfig{Fulcio: &Fulcio{Roots: []string{ca.pem}, Identities: []Identity{{Issuer: testIssuer, SubjectRegexp: "("}}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.Compile(); err == nil {
				t.Fatal("Compile accepted the configuration")
			}
		})
	}
}

func TestTrustSubjectRegexpIsAnchored(t *testing.T) {
	ca := newTestCA(t)
	signer, certPEM := ca.issue(t, "https://example.com/evil/https://kubernetes.io/ns/ci", testIssuer, time.Now().Add(-time.Minute))
	trust := compile(t, TrustConfig{Fulcio: &Fulcio{
		Roots: []string{ca.pem}, Identities: []Identity{{Issuer: testIssuer, SubjectRegexp: `https://kubernetes\.io/ns/ci`}},
	}})

	var v Verifier

	out := v.VerifyEnvelope(
		trust,
		decodeEnv(t, provenanceEnvelope(t, signer, testSubject)),
		Material{Leaf: parseCert(t, certPEM)},
		time.Now(),
	)
	if out.Verified {
		t.Fatal("an unanchored match was accepted")
	}
}
