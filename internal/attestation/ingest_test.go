// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attestation

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ociref"
)

func attTag() string { return ociref.CosignTag(testSubject, ociref.KindAttestation) }

func dsseLayer(raw []byte, ann map[string]string) layerSpec {
	return layerSpec{mediaType: attest.EnvelopeContentType, data: raw, annotations: ann}
}

// ingestAtt pushes a ".att" manifest with the layers and ingests it.
func ingestAtt(t *testing.T, s *Service, db *fakeDB, layers ...layerSpec) error {
	t.Helper()

	digest, body := db.putManifest(t, "", "", layers...)
	db.tags[attTag()] = digest

	return s.Ingest(context.Background(), Event{RepositoryID: testRepo, Tag: attTag(), Digest: digest, Raw: body})
}

func onlyRow(t *testing.T, db *fakeDB) *models.OCIAttestation {
	t.Helper()

	rows := db.sorted()
	if len(rows) != 1 {
		t.Fatalf("indexed %d attestations, want 1", len(rows))
	}

	return rows[0]
}

func setTrust(t *testing.T, s *Service, cfg TrustConfig) {
	t.Helper()

	if err := s.SetTrustConfig(context.Background(), testRepo, &cfg); err != nil {
		t.Fatal(err)
	}
}

func TestIngestCosignAttestationTag(t *testing.T) {
	good, _ := newSigner(t)
	other, _ := newSigner(t)

	tamperedEnv := decodeEnv(t, provenanceEnvelope(t, good, testSubject))
	tamperedEnv.Payload = base64.StdEncoding.EncodeToString(
		[]byte(`{"_type":"https://in-toto.io/Statement/v0.1","subject":[{"name":"x","digest":{"sha256":"` +
			strings.TrimPrefix(
				testSubject,
				"sha256:",
			) + `"}}],"predicateType":"` + PredicateSLSAv02 + `","predicate":{"builder":{"id":"evil"}}}`),
	)

	tampered, err := json.Marshal(tamperedEnv)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		trusted    *attest.Signer
		envelope   []byte
		verified   bool
		errContain string
	}{
		{"signed by the trusted key", good, provenanceEnvelope(t, good, testSubject), true, ""},
		{"signed by another key", other, provenanceEnvelope(t, good, testSubject), false, "no trusted key"},
		{"payload tampered after signing", good, tampered, false, "no trusted key"},
		{
			"valid attestation about another image", good,
			provenanceEnvelope(t, good, "sha256:"+strings.Repeat("b", 64)), false, "does not cover the image digest",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newFakeDB()
			s := newService(t, db)
			setTrust(t, s, TrustConfig{Keys: []string{pemPublic(t, tc.trusted)}})

			if err := ingestAtt(t, s, db, dsseLayer(tc.envelope, map[string]string{"predicateType": PredicateSLSAv02})); err != nil {
				t.Fatalf("Ingest: %v", err)
			}

			row := onlyRow(t, db)
			if row.Verified != tc.verified {
				t.Fatalf("verified = %v (%s), want %v", row.Verified, row.VerifyError, tc.verified)
			}

			if !strings.Contains(row.VerifyError, tc.errContain) {
				t.Fatalf("verify_error = %q, want it to contain %q", row.VerifyError, tc.errContain)
			}

			if row.Kind != models.AttestationKindAttestation || row.PredicateType != PredicateSLSAv02 || row.SubjectDigest != testSubject {
				t.Fatalf("row = %+v", row)
			}

			if row.AttestationDigest != "sha256:"+row.PayloadBlob {
				t.Fatalf("payload blob %q does not match digest %q", row.PayloadBlob, row.AttestationDigest)
			}
		})
	}
}

func TestIngestKeyless(t *testing.T) {
	ca := newTestCA(t)
	now := time.Now()
	signer, certPEM := ca.issue(t, testSAN, testIssuer, now.Add(-time.Minute))
	trust := TrustConfig{Fulcio: &Fulcio{Roots: []string{ca.pem}, Identities: []Identity{{Issuer: testIssuer, Subject: testSAN}}}}

	db := newFakeDB()
	s := newService(t, db)
	setTrust(t, s, trust)

	ann := map[string]string{"predicateType": PredicateSLSAv1, annotationCertificate: certPEM, annotationChain: ca.pem}
	if err := ingestAtt(t, s, db, dsseLayer(provenanceEnvelope(t, signer, testSubject), ann)); err != nil {
		t.Fatal(err)
	}

	row := onlyRow(t, db)
	if !row.Verified || row.SignerIdentity != testSAN || row.Issuer != testIssuer {
		t.Fatalf("row = verified %v signer %q issuer %q (%s)", row.Verified, row.SignerIdentity, row.Issuer, row.VerifyError)
	}

	// Judged at first-seen time: the certificate is long expired when a later
	// reverification runs, the verdict stays.
	s.now = func() time.Time { return now.Add(2 * time.Hour) }

	if n, err := s.Reverify(context.Background(), testRepo, testSubject, ""); err != nil || n != 1 {
		t.Fatalf("Reverify = %d, %v", n, err)
	}

	if row := onlyRow(t, db); !row.Verified {
		t.Fatalf("reverification judged the signature at the wrong time: %s", row.VerifyError)
	}
}

func TestIngestKeylessIdentityMismatch(t *testing.T) {
	ca := newTestCA(t)
	signer, certPEM := ca.issue(t, testSAN, testIssuer, time.Now().Add(-time.Minute))

	db := newFakeDB()
	s := newService(t, db)
	setTrust(
		t,
		s,
		TrustConfig{
			Fulcio: &Fulcio{
				Roots:      []string{ca.pem},
				Identities: []Identity{{Issuer: testIssuer, Subject: "https://example.com/someone-else"}},
			},
		},
	)

	ann := map[string]string{annotationCertificate: certPEM, annotationChain: ca.pem}
	if err := ingestAtt(t, s, db, dsseLayer(provenanceEnvelope(t, signer, testSubject), ann)); err != nil {
		t.Fatal(err)
	}

	row := onlyRow(t, db)
	if row.Verified || !strings.Contains(row.VerifyError, "identity is not trusted") {
		t.Fatalf("row = verified %v (%s)", row.Verified, row.VerifyError)
	}

	if row.SignerIdentity != testSAN {
		t.Fatalf("signer = %q, want the claimed identity", row.SignerIdentity)
	}

	if row.PredicateType != PredicateSLSAv02 {
		t.Fatalf("predicate = %q, want it read from the statement", row.PredicateType)
	}
}

func TestIngestCosignSignatureTag(t *testing.T) {
	s0, key := newSigner(t)
	other, _ := newSigner(t)

	sign := func(digest string) layerSpec {
		payload := []byte(`{"critical":{"identity":{"docker-reference":"x"},"image":{"docker-manifest-digest":"` + digest +
			`"},"type":"cosign container image signature"},"optional":null}`)
		sum := sha256.Sum256(payload)

		sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
		if err != nil {
			t.Fatal(err)
		}

		return layerSpec{
			mediaType:   mediaTypeSimpleSign,
			data:        payload,
			annotations: map[string]string{annotationSignature: base64.StdEncoding.EncodeToString(sig)},
		}
	}

	tests := []struct {
		name     string
		trusted  *attest.Signer
		layer    layerSpec
		verified bool
	}{
		{"valid signature", s0, sign(testSubject), true},
		{"untrusted key", other, sign(testSubject), false},
		{"signature of another image", s0, sign("sha256:" + strings.Repeat("c", 64)), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newFakeDB()
			s := newService(t, db)
			setTrust(t, s, TrustConfig{Keys: []string{pemPublic(t, tc.trusted)}})

			digest, body := db.putManifest(t, "", "", tc.layer)

			tag := ociref.CosignTag(testSubject, ociref.KindSignature)
			if err := s.Ingest(context.Background(), Event{RepositoryID: testRepo, Tag: tag, Digest: digest, Raw: body}); err != nil {
				t.Fatal(err)
			}

			row := onlyRow(t, db)
			if row.Kind != models.AttestationKindSignature || row.Verified != tc.verified || row.PredicateType != PredicateSimpleSigning {
				t.Fatalf("row = %+v", row)
			}
		})
	}
}

func TestIngestSBOMTagIsIndexedUnverified(t *testing.T) {
	db := newFakeDB()
	s := newService(t, db)

	digest, body := db.putManifest(t, "", "", layerSpec{mediaType: "application/spdx+json", data: []byte(`{"spdxVersion":"SPDX-2.3"}`)})

	tag := ociref.CosignTag(testSubject, ociref.KindSBOM)
	if err := s.Ingest(context.Background(), Event{RepositoryID: testRepo, Tag: tag, Digest: digest, Raw: body}); err != nil {
		t.Fatal(err)
	}

	row := onlyRow(t, db)
	if row.Kind != models.AttestationKindSBOM || row.Verified || row.PredicateType != attest.PredicateSPDX {
		t.Fatalf("row = %+v", row)
	}

	st, err := s.Statement(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}

	if st.PredicateType != attest.PredicateSPDX || !strings.Contains(string(st.Predicate), "SPDX-2.3") {
		t.Fatalf("statement = %+v", st)
	}
}

func TestIngestReferrerBundle(t *testing.T) {
	signer, _ := newSigner(t)
	db := newFakeDB()
	s := newService(t, db)
	setTrust(t, s, TrustConfig{Keys: []string{pemPublic(t, signer)}})

	bundle, err := json.Marshal(map[string]any{
		"mediaType":            attest.BundleMediaType,
		"verificationMaterial": map[string]any{"publicKey": map[string]any{"hint": "k"}},
		"dsseEnvelope":         decodeEnv(t, provenanceEnvelope(t, signer, testSubject)),
	})
	if err != nil {
		t.Fatal(err)
	}

	digest, body := db.putManifest(t, attest.BundleMediaType, testSubject, layerSpec{mediaType: attest.BundleMediaType, data: bundle})

	if err := s.Ingest(context.Background(), Event{RepositoryID: testRepo, Digest: digest, Raw: body}); err != nil {
		t.Fatal(err)
	}

	row := onlyRow(t, db)
	if !row.Verified || row.PredicateType != PredicateSLSAv02 || row.Kind != models.AttestationKindAttestation {
		t.Fatalf("row = %+v", row)
	}

	st, err := s.Statement(context.Background(), row)
	if err != nil || st.PredicateType != PredicateSLSAv02 {
		t.Fatalf("Statement = %+v, %v", st, err)
	}
}

func TestIngestIgnoresOtherManifests(t *testing.T) {
	db := newFakeDB()
	s := newService(t, db)

	image, body := db.putManifest(t, "", "", layerSpec{mediaType: "application/vnd.oci.image.layer.v1.tar+gzip", data: []byte("layer")})
	// A referrer of another artifact type, and a manifest pushed under a plain tag.
	ref, refBody := db.putManifest(t, "application/vnd.example.thing", testSubject, layerSpec{mediaType: "text/plain", data: []byte("x")})

	events := []Event{
		{RepositoryID: testRepo, Tag: "latest", Digest: image, Raw: body},
		{RepositoryID: testRepo, Digest: ref, Raw: refBody},
		{RepositoryID: testRepo, Tag: attTag(), Digest: image, Raw: []byte("not json")},
	}

	for _, ev := range events {
		if err := s.Ingest(context.Background(), ev); err != nil {
			t.Fatalf("Ingest(%q): %v", ev.Tag, err)
		}
	}

	if n := len(db.rows); n != 0 {
		t.Fatalf("indexed %d rows for manifests that are no attachments", n)
	}
}

func TestIngestOversizedAndMissingLayers(t *testing.T) {
	signer, _ := newSigner(t)
	db := newFakeDB()
	s := newService(t, db)
	setTrust(t, s, TrustConfig{Keys: []string{pemPublic(t, signer)}})

	huge := make([]byte, maxEnvelopeBytes+1)
	if err := ingestAtt(t, s, db, dsseLayer(huge, nil)); err != nil {
		t.Fatalf("an oversized layer failed the ingest: %v", err)
	}

	if row := onlyRow(t, db); row.Verified || !strings.Contains(row.VerifyError, "4 MiB") {
		t.Fatalf("row = %+v", row)
	}

	// A layer whose blob is not in the repository is reported, the others are still indexed.
	good := dsseLayer(provenanceEnvelope(t, signer, testSubject), nil)
	digest, body := db.putManifest(t, "", "", good)

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}

	layers, ok := m["layers"].([]any)
	if !ok {
		t.Fatal("manifest has no layers")
	}

	missing := map[string]any{"mediaType": attest.EnvelopeContentType, "digest": "sha256:" + strings.Repeat("d", 64), "size": 10}
	m["layers"] = append([]any{missing}, layers...)

	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	err = s.Ingest(context.Background(), Event{RepositoryID: testRepo, Tag: attTag(), Digest: digest, Raw: body})
	if err == nil || !errors.Is(err, errBlobMissing) {
		t.Fatalf("Ingest error = %v, want errBlobMissing", err)
	}

	if len(db.rows) != 2 {
		t.Fatalf("indexed %d rows, want the oversized one and the good one", len(db.rows))
	}
}

func TestReverifyAppliesChangedTrust(t *testing.T) {
	signer, _ := newSigner(t)
	db := newFakeDB()
	s := newService(t, db)

	// Pushed before any trust is configured.
	digest, body := db.putManifest(t, "", "", dsseLayer(provenanceEnvelope(t, signer, testSubject), nil))
	db.tags[attTag()] = digest

	if err := s.Ingest(context.Background(), Event{RepositoryID: testRepo, Tag: attTag(), Digest: digest, Raw: body}); err != nil {
		t.Fatal(err)
	}

	before := onlyRow(t, db)
	if before.Verified {
		t.Fatal("verified without trust")
	}

	setTrust(t, s, TrustConfig{Keys: []string{pemPublic(t, signer)}})

	for _, subject := range []string{testSubject, ""} {
		if n, err := s.Reverify(context.Background(), testRepo, subject, ""); err != nil || n != 1 {
			t.Fatalf("Reverify(%q) = %d, %v", subject, n, err)
		}
	}

	after := onlyRow(t, db)
	if !after.Verified || after.ID != before.ID || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("after = %+v, before = %+v", after, before)
	}
}

func TestCheckProvenance(t *testing.T) {
	signer, _ := newSigner(t)
	const dst = "22222222-2222-2222-2222-222222222222"

	tests := []struct {
		name       string
		srcTrust   *TrustConfig
		dstTrust   *TrustConfig
		predicate  string
		signedByTK bool
		want       error
	}{
		{"nothing required", &TrustConfig{}, nil, "", false, nil},
		{"required by the source, verified provenance", &TrustConfig{RequireVerifiedProvenance: true}, nil, PredicateSLSAv02, true, nil},
		{"required, SLSA v1 provenance", &TrustConfig{RequireVerifiedProvenance: true}, nil, PredicateSLSAv1, true, nil},
		{"required, no attestation", &TrustConfig{RequireVerifiedProvenance: true}, nil, "", false, ErrProvenanceRequired},
		{
			"required, provenance not verified",
			&TrustConfig{RequireVerifiedProvenance: true},
			nil,
			PredicateSLSAv02,
			false,
			ErrProvenanceRequired,
		},
		{
			"required, only a verified other predicate",
			&TrustConfig{RequireVerifiedProvenance: true},
			nil,
			"https://example.com/other",
			true,
			ErrProvenanceRequired,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newFakeDB()
			s := newService(t, db)

			cfg := TrustConfig{}
			if tc.srcTrust != nil {
				cfg = *tc.srcTrust
			}

			if tc.signedByTK {
				cfg.Keys = []string{pemPublic(t, signer)}
			}

			setTrust(t, s, cfg)

			if tc.predicate != "" {
				db.rows["r"] = &models.OCIAttestation{
					RepositoryID: testRepo, SubjectDigest: testSubject, Kind: models.AttestationKindAttestation,
					PredicateType: tc.predicate, Verified: tc.signedByTK,
				}
			}

			if err := s.CheckProvenance(context.Background(), testRepo, dst, testSubject); !errors.Is(err, tc.want) {
				t.Fatalf("CheckProvenance = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckProvenanceRequiredByDestination(t *testing.T) {
	const dst = "22222222-2222-2222-2222-222222222222"

	db := newFakeDB()
	s := newService(t, db)

	// The destination's configuration is the one the fake returns for every repository: only
	// the flag matters here, the source has no verified provenance.
	setTrust(t, s, TrustConfig{RequireVerifiedProvenance: true})

	if err := s.CheckProvenance(context.Background(), testRepo, dst, testSubject); !errors.Is(err, ErrProvenanceRequired) {
		t.Fatalf("CheckProvenance = %v, want ErrProvenanceRequired", err)
	}
}

func TestSetTrustConfigRejectsInvalid(t *testing.T) {
	s := newService(t, newFakeDB())

	err := s.SetTrustConfig(context.Background(), testRepo, &TrustConfig{Keys: []string{"nope"}})
	if !errors.Is(err, ErrInvalidTrust) {
		t.Fatalf("SetTrustConfig = %v, want ErrInvalidTrust", err)
	}
}
