// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	attestPkgSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	attestRepoID = "11111111-1111-1111-1111-111111111111"
	attestSBOMID = "22222222-2222-2222-2222-222222222222"
)

type attestDB struct {
	database.BaseMockDB

	report *models.SBOMReport
}

func (d *attestDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if name != "libs" {
		return nil, nil
	}

	return &models.Repository{ID: attestRepoID, Name: "libs", Format: "npm"}, nil
}

func (d *attestDB) GetSBOMReportByID(context.Context, string, string) (*models.SBOMReport, error) {
	return d.report, nil
}

func (d *attestDB) SearchComponents(context.Context, string, database.ComponentListOptions) ([]*models.Component, int, error) {
	return []*models.Component{{Name: "lib", Version: "1.0.0", SHA256: attestPkgSHA}}, 1, nil
}

func attestScan() *models.VulnScanResult {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	return &models.VulnScanResult{
		ArtifactID: "lib@1.0.0", ScanStatus: "completed", Scanner: "osv", ScannedAt: &now,
		Vulnerabilities: []models.Vulnerability{{ID: "CVE-2024-0001", Summary: "bad", Severity: "HIGH", FixedIn: "1.0.5"}},
	}
}

func (d *attestDB) ListVulnScanResults(context.Context, string, bool) ([]*models.VulnScanResult, error) {
	return []*models.VulnScanResult{attestScan()}, nil
}

func (d *attestDB) GetVulnScanResult(context.Context, string, string) (*models.VulnScanResult, error) {
	return attestScan(), nil
}

func (d *attestDB) ListArtifactProperties(context.Context, string, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (d *attestDB) GetLicenseScanResult(context.Context, string, string) (*models.LicenseScanResult, error) {
	return nil, nil
}

func (d *attestDB) ListVEXStatements(context.Context, string, string) ([]*models.VEXStatement, error) {
	return nil, nil
}

func (d *attestDB) ListRepositoryVEXStatements(context.Context, string) ([]*models.VEXStatement, error) {
	return nil, nil
}

type attestFixture struct {
	router chi.Router
	pubKey any
	blobs  *storage.FilesystemStore
	db     *attestDB
}

func newAttestFixture(t *testing.T, withService bool) *attestFixture {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	require.NoError(t, err)

	blobs, err := storage.NewFilesystemStore(t.TempDir())
	require.NoError(t, err)

	db := &attestDB{}
	eval := auth.NewEvaluator(db, log, false)
	gen := sbom.NewGenerator(db, nil).WithToolVersion("9.9.9")

	var svc *attest.Service

	f := &attestFixture{blobs: blobs, db: db}

	if withService {
		keyPEM, err := attest.GenerateKeyPEM()
		require.NoError(t, err)

		keyFile := filepath.Join(t.TempDir(), "key.pem")
		require.NoError(t, os.WriteFile(keyFile, keyPEM, 0o600))

		svc = attest.NewService(keyFile, nil)
		info, err := svc.Info(context.Background())
		require.NoError(t, err)

		block, _ := pem.Decode([]byte(info.PublicKeyPEM))
		require.NotNil(t, block)

		f.pubKey, err = x509.ParsePKIXPublicKey(block.Bytes)
		require.NoError(t, err)
	}

	r := chi.NewRouter()
	NewSBOMHandler(db, log, eval, gen, blobs).WithAttestation(svc).RegisterAuthed(r)
	NewVEXHandler(db, log, eval, gen).WithAttestation(svc).RegisterAuthed(r)
	ah := NewAttestationHandler(svc, log, eval)
	ah.RegisterPublic(r)
	ah.RegisterAuthed(r)
	f.router = r

	return f
}

func (f *attestFixture) get(t *testing.T, target string, admin bool) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if admin {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))
	}

	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)

	return rw
}

func (f *attestFixture) storeSBOM(t *testing.T, format, contentType, body string) {
	t.Helper()

	sha, err := f.blobs.Put(context.Background(), strings.NewReader(body), nil)
	require.NoError(t, err)

	f.db.report = &models.SBOMReport{
		ID: attestSBOMID, RepositoryID: attestRepoID, ArtifactID: "lib@1.0.0", Format: format,
		ContentType: contentType, BlobSHA256: sha,
	}
}

// decodeEnvelope verifies body as a DSSE envelope signed by the fixture key and returns its statement.
func (f *attestFixture) decodeEnvelope(t *testing.T, body []byte) *attest.Statement {
	t.Helper()

	var env attest.Envelope
	require.NoError(t, json.Unmarshal(body, &env))
	st, err := attest.Verify(&env, f.pubKey)
	require.NoError(t, err, "signature must verify with the published public key")

	return st
}

func TestSBOMDownloadAttestEnvelopeSignsStoredDocument(t *testing.T) {
	f := newAttestFixture(t, true)
	doc := `{"bomFormat":"CycloneDX","specVersion":"1.5"}`
	f.storeSBOM(t, "cyclonedx", "application/json", doc)
	sum := sha256.Sum256([]byte(doc))

	rw := f.get(t, "/api/v1/repositories/libs/sbom/"+attestSBOMID+"/download?attest=true", true)
	require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
	assert.Equal(t, attest.EnvelopeContentType, rw.Header().Get("Content-Type"))
	assert.Contains(t, rw.Header().Get("Content-Disposition"), "sbom.cdx.json.intoto.json")

	st := f.decodeEnvelope(t, rw.Body.Bytes())
	assert.Equal(t, attest.PredicateCycloneDX, st.PredicateType)

	var predicate map[string]any
	require.NoError(t, json.Unmarshal(st.Predicate, &predicate))
	assert.Equal(t, "CycloneDX", predicate["bomFormat"], "the predicate is the stored SBOM itself")

	digests := map[string]bool{}
	for _, s := range st.Subject {
		digests[s.Digest["sha256"]] = true
	}

	assert.True(t, digests[attestPkgSHA], "the package digest is a subject (cosign verify-blob-attestation <package>)")
	assert.True(t, digests[hex.EncodeToString(sum[:])], "the SBOM file digest is a subject")
}

func TestSBOMDownloadAttestBundleIsSigstoreBundle(t *testing.T) {
	f := newAttestFixture(t, true)
	f.storeSBOM(t, "spdx3-json", "application/ld+json", `{"@context":"x","@graph":[]}`)

	rw := f.get(t, "/api/v1/repositories/libs/sbom/"+attestSBOMID+"/download?attest=bundle", true)
	require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
	assert.Equal(t, attest.BundleContentType, rw.Header().Get("Content-Type"))
	assert.Contains(t, rw.Header().Get("Content-Disposition"), "sbom.spdx3.json.sigstore.json")

	var bundle struct {
		MediaType        string `json:"mediaType"`
		DSSEEnvelope     attest.Envelope
		DSSEEnvelopeJSON json.RawMessage `json:"dsseEnvelope"`
	}

	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &bundle))
	assert.Equal(t, attest.BundleMediaType, bundle.MediaType)
	require.NoError(t, json.Unmarshal(bundle.DSSEEnvelopeJSON, &bundle.DSSEEnvelope))

	st, err := attest.Verify(&bundle.DSSEEnvelope, f.pubKey)
	require.NoError(t, err)
	assert.Equal(t, attest.PredicateSPDX3, st.PredicateType)
}

func TestSBOMDownloadAttestRejections(t *testing.T) {
	url := "/api/v1/repositories/libs/sbom/" + attestSBOMID + "/download"

	t.Run("tag-value SPDX cannot be attested", func(t *testing.T) {
		f := newAttestFixture(t, true)
		f.storeSBOM(t, "spdx", "text/plain", "SPDXVersion: SPDX-2.3\n")

		rw := f.get(t, url+"?attest=true", true)
		assert.Equal(t, http.StatusBadRequest, rw.Code)
		assert.Contains(t, rw.Body.String(), "validation_failed")
	})

	t.Run("unknown attest value", func(t *testing.T) {
		f := newAttestFixture(t, true)
		f.storeSBOM(t, "cyclonedx", "application/json", `{}`)

		assert.Equal(t, http.StatusBadRequest, f.get(t, url+"?attest=yes", true).Code)
	})

	t.Run("not enabled", func(t *testing.T) {
		f := newAttestFixture(t, false)
		f.storeSBOM(t, "cyclonedx", "application/json", `{}`)

		assert.Equal(t, http.StatusNotImplemented, f.get(t, url+"?attest=true", true).Code)
	})

	t.Run("admin only", func(t *testing.T) {
		f := newAttestFixture(t, true)
		f.storeSBOM(t, "cyclonedx", "application/json", `{}`)

		assert.Equal(t, http.StatusForbidden, f.get(t, url+"?attest=true", false).Code)
	})

	t.Run("plain download is unchanged", func(t *testing.T) {
		f := newAttestFixture(t, true)
		f.storeSBOM(t, "cyclonedx", "application/json", `{"a":1}`)

		rw := f.get(t, url, true)
		require.Equal(t, http.StatusOK, rw.Code)
		assert.Equal(t, `{"a":1}`, rw.Body.String())
	})
}

func TestVEXDocumentAttest(t *testing.T) {
	for name, tc := range map[string]struct {
		query     string
		predicate string
		file      string
	}{
		"openvex":   {"", attest.PredicateOpenVEX, "vex.openvex.json.intoto.json"},
		"cyclonedx": {"&format=cyclonedx", attest.PredicateCycloneDX, "vex.cyclonedx.json.intoto.json"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAttestFixture(t, true)

			rw := f.get(t, "/api/v1/repositories/libs/vex?artifact_id=lib@1.0.0&attest=true"+tc.query, true)
			require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
			assert.Contains(t, rw.Header().Get("Content-Disposition"), tc.file)

			st := f.decodeEnvelope(t, rw.Body.Bytes())
			assert.Equal(t, tc.predicate, st.PredicateType)
			require.NotEmpty(t, st.Subject)
			assert.Equal(t, attestPkgSHA, st.Subject[0].Digest["sha256"], "subject is the package the VEX speaks about")
			assert.True(t, strings.HasPrefix(st.Subject[0].Name, "pkg:"), st.Subject[0].Name)
		})
	}

	t.Run("invalid attest value", func(t *testing.T) {
		f := newAttestFixture(t, true)
		assert.Equal(t, http.StatusBadRequest, f.get(t, "/api/v1/repositories/libs/vex?attest=maybe", true).Code)
	})

	t.Run("not enabled", func(t *testing.T) {
		f := newAttestFixture(t, false)
		assert.Equal(t, http.StatusNotImplemented, f.get(t, "/api/v1/repositories/libs/vex?attest=true", true).Code)
	})
}

func TestAttestationPublicKeyEndpoints(t *testing.T) {
	f := newAttestFixture(t, true)

	rw := f.get(t, AttestationKeyPath, false)
	require.Equal(t, http.StatusOK, rw.Code, "the verification key is public")
	assert.Contains(t, rw.Body.String(), "BEGIN PUBLIC KEY")
	assert.NotContains(t, rw.Body.String(), "PRIVATE")

	pubPEM := rw.Body.Bytes()

	rw = f.get(t, "/api/v1/admin/attestation", true)
	require.Equal(t, http.StatusOK, rw.Code)

	var info struct {
		KeyID        string `json:"key_id"`
		Algorithm    string `json:"algorithm"`
		Source       string `json:"source"`
		PublicKeyPEM string `json:"public_key_pem"`
		PublicKeyURL string `json:"public_key_url"`
	}

	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &info))
	assert.Equal(t, "ecdsa-p256", info.Algorithm)
	assert.Equal(t, "file", info.Source)
	assert.Equal(t, AttestationKeyPath, info.PublicKeyURL)
	assert.True(t, bytes.Equal(pubPEM, []byte(info.PublicKeyPEM)))
	assert.NotEmpty(t, info.KeyID)
	assert.NotContains(t, rw.Body.String(), "PRIVATE")

	assert.Equal(t, http.StatusForbidden, f.get(t, "/api/v1/admin/attestation", false).Code)

	off := newAttestFixture(t, false)
	assert.Equal(t, http.StatusNotImplemented, off.get(t, AttestationKeyPath, false).Code)
}

func TestAttestEnvelopeSignatureIsBase64(t *testing.T) {
	f := newAttestFixture(t, true)
	f.storeSBOM(t, "spdx-json", "application/json", `{"spdxVersion":"SPDX-2.3"}`)

	rw := f.get(t, "/api/v1/repositories/libs/sbom/"+attestSBOMID+"/download?attest=true", true)
	require.Equal(t, http.StatusOK, rw.Code)

	var env struct {
		PayloadType string `json:"payloadType"`
		Payload     string `json:"payload"`
	}

	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &env))
	assert.Equal(t, attest.PayloadType, env.PayloadType)

	raw, err := base64.StdEncoding.DecodeString(env.Payload)
	require.NoError(t, err)
	assert.Contains(t, string(raw), attest.StatementType)
	assert.Contains(t, string(raw), attest.PredicateSPDX)
}
