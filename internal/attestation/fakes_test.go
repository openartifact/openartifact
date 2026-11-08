// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attestation

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const testRepo = "11111111-1111-1111-1111-111111111111"

// fakeDB is an in-memory DB.
type fakeDB struct {
	blobs     map[string][]byte // blob key (hex) -> content; also what the blob reader serves
	manifests map[string]*models.DockerManifest
	tags      map[string]string // tag -> manifest digest
	rows      map[string]*models.OCIAttestation
	trust     json.RawMessage
	seq       int
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		blobs:     map[string][]byte{},
		manifests: map[string]*models.DockerManifest{},
		tags:      map[string]string{},
		rows:      map[string]*models.OCIAttestation{},
	}
}

func rowKey(a *models.OCIAttestation) string {
	return a.RepositoryID + "|" + a.Image + "|" + a.SubjectDigest + "|" + a.AttestationDigest
}

func (f *fakeDB) UpsertOCIAttestation(_ context.Context, a *models.OCIAttestation) error {
	k := rowKey(a)
	if old, ok := f.rows[k]; ok {
		a.ID, a.CreatedAt = old.ID, old.CreatedAt
	} else {
		f.seq++
		a.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", f.seq)

		if a.CreatedAt.IsZero() {
			a.CreatedAt = time.Now()
		}
	}

	c := *a
	f.rows[k] = &c

	return nil
}

func (f *fakeDB) GetOCIAttestationFirstSeen(_ context.Context, repoID, image, subject, digest string) (time.Time, bool, error) {
	r, ok := f.rows[repoID+"|"+image+"|"+subject+"|"+digest]
	if !ok {
		return time.Time{}, false, nil
	}

	return r.CreatedAt, true, nil
}

func (f *fakeDB) sorted() []*models.OCIAttestation {
	out := make([]*models.OCIAttestation, 0, len(f.rows))
	for _, r := range f.rows {
		c := *r
		out = append(out, &c)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}

func (f *fakeDB) ListOCIAttestations(_ context.Context, repoID, subject, image string) ([]*models.OCIAttestation, error) {
	var out []*models.OCIAttestation

	for _, r := range f.sorted() {
		if r.RepositoryID == repoID && r.SubjectDigest == subject && (image == "" || r.Image == image) {
			out = append(out, r)
		}
	}

	return out, nil
}

func (f *fakeDB) ListOCIAttestationsByDigest(_ context.Context, repoID, digest string) ([]*models.OCIAttestation, error) {
	var out []*models.OCIAttestation

	for _, r := range f.sorted() {
		if r.RepositoryID == repoID && r.AttestationDigest == digest {
			out = append(out, r)
		}
	}

	return out, nil
}

func (f *fakeDB) ListOCIAttestationsPage(_ context.Context, repoID, afterID string, limit int) ([]*models.OCIAttestation, error) {
	var out []*models.OCIAttestation

	for _, r := range f.sorted() {
		if r.RepositoryID == repoID && r.ID > afterID && len(out) < limit {
			out = append(out, r)
		}
	}

	return out, nil
}

func (f *fakeDB) HasVerifiedOCIAttestation(_ context.Context, repoID, subject string, types []string) (bool, error) {
	for _, r := range f.rows {
		if r.RepositoryID != repoID || r.SubjectDigest != subject || !r.Verified || r.Kind != models.AttestationKindAttestation {
			continue
		}

		if slices.Contains(types, r.PredicateType) {
			return true, nil
		}
	}

	return false, nil
}

func (f *fakeDB) GetRepositoryAttestationTrust(context.Context, string) (json.RawMessage, error) {
	return f.trust, nil
}

func (f *fakeDB) SetRepositoryAttestationTrust(_ context.Context, _ string, c json.RawMessage) error {
	f.trust = c
	return nil
}

func (f *fakeDB) GetDockerBlob(_ context.Context, _, digest string) (*models.DockerBlob, error) {
	data, ok := f.blobs[digest[len("sha256:"):]]
	if !ok {
		return nil, nil
	}

	return &models.DockerBlob{BlobSHA256: digest[len("sha256:"):], Size: int64(len(data))}, nil
}

func (f *fakeDB) GetDockerManifest(_ context.Context, _, _, digest string) (*models.DockerManifest, error) {
	return f.manifests[digest], nil
}

func (f *fakeDB) GetDockerTag(_ context.Context, _, _, name string) (*models.DockerTag, error) {
	d, ok := f.tags[name]
	if !ok {
		return nil, nil
	}

	return &models.DockerTag{Name: name, ManifestDigest: d}, nil
}

func (f *fakeDB) ListDockerManifestsBySubject(_ context.Context, _, _, subject string) ([]*models.DockerManifest, error) {
	var out []*models.DockerManifest

	for _, m := range f.manifests {
		if m.SubjectDigest == subject {
			out = append(out, m)
		}
	}

	return out, nil
}

// Get makes fakeDB the blob reader too: blobs and manifests share the key space.
func (f *fakeDB) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.blobs[key]
	if !ok {
		return nil, fmt.Errorf("blob %s: %w", key, io.ErrUnexpectedEOF)
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeDB) putBlob(data []byte) string {
	sum := sha256.Sum256(data)
	f.blobs[hex.EncodeToString(sum[:])] = data

	return "sha256:" + hex.EncodeToString(sum[:])
}

type layerSpec struct {
	mediaType   string
	data        []byte
	annotations map[string]string
}

// putManifest stores an attachment manifest with the layers and returns its digest and body.
func (f *fakeDB) putManifest(t *testing.T, artifactType, subject string, layers ...layerSpec) (string, []byte) {
	t.Helper()

	var ds []map[string]any

	for _, l := range layers {
		d := map[string]any{"mediaType": l.mediaType, "digest": f.putBlob(l.data), "size": len(l.data)}
		if l.annotations != nil {
			d["annotations"] = l.annotations
		}

		ds = append(ds, d)
	}

	m := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    "sha256:" + hex.EncodeToString(make([]byte, 32)),
			"size":      2,
		},
		"layers": ds,
	}
	if artifactType != "" {
		m["artifactType"] = artifactType
	}

	if subject != "" {
		m["subject"] = map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": subject, "size": 1}
	}

	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	digest := f.putBlob(body)
	row := &models.DockerManifest{ID: "manifest-" + digest[7:15], RepositoryID: testRepo, Digest: digest}

	row.SubjectDigest = subject

	f.manifests[digest] = row

	return digest, body
}

func newService(t *testing.T, db *fakeDB) *Service {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return NewService(log, db, db)
}

func newSigner(t *testing.T) (*attest.Signer, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	s, err := attest.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}

	return s, key
}

func pemPublic(t *testing.T, s *attest.Signer) string {
	t.Helper()

	return string(s.PublicKeyPEM())
}

func provenanceEnvelope(t *testing.T, s *attest.Signer, subjectDigest string) []byte {
	t.Helper()

	env, err := s.Attest(PredicateSLSAv02, []byte(`{"builder":{"id":"tekton"}}`),
		[]attest.Subject{attest.SHA256Subject("img", subjectDigest[len("sha256:"):])})
	if err != nil {
		t.Fatal(err)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// testCA is a Fulcio-like CA: a root and the ability to issue code-signing leaves.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-fulcio-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return &testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// issue signs a short-lived leaf for the SAN URI and OIDC issuer, valid from notBefore for ten minutes.
func (ca *testCA) issue(t *testing.T, san, issuer string, notBefore time.Time) (*attest.Signer, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(san)
	if err != nil {
		t.Fatal(err)
	}

	issuerDER, err := asn1.Marshal(issuer)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{},
		NotBefore:    notBefore,
		NotAfter:     notBefore.Add(10 * time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs:         []*url.URL{u},
		ExtraExtensions: []pkix.Extension{
			{Id: oidIssuer(oidIssuerDERArc), Value: issuerDER},
		},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}

	s, err := attest.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}

	return s, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
