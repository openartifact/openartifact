// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const attachManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type attachDB struct {
	database.BaseMockDB

	mu      sync.Mutex
	format  string
	enabled bool
	report  *models.SBOMReport
}

func (d *attachDB) repo() *models.Repository {
	f := d.format
	if f == "" {
		f = "docker"
	}

	return &models.Repository{ID: attestRepoID, Name: "reg", Format: f, RepoType: "hosted"}
}

func (d *attachDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if name != "reg" {
		return nil, nil
	}

	return d.repo(), nil
}

func (d *attachDB) GetRepositoryByID(context.Context, string) (*models.Repository, error) {
	return d.repo(), nil
}

func (d *attachDB) GetRepositoryAttestationAttach(context.Context, string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.enabled, nil
}

func (d *attachDB) SetRepositoryAttestationAttach(_ context.Context, _ string, on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enabled = on

	return nil
}

func (d *attachDB) GetDockerTag(_ context.Context, _, _, name string) (*models.DockerTag, error) {
	if name != "1.0" {
		return nil, nil
	}

	return &models.DockerTag{Name: name, ManifestDigest: attachManifestDigest}, nil
}

func (d *attachDB) GetSBOMReportByID(context.Context, string, string) (*models.SBOMReport, error) {
	return d.report, nil
}

func (d *attachDB) ListArtifactProperties(context.Context, string, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (d *attachDB) ListVEXStatements(context.Context, string, string) ([]*models.VEXStatement, error) {
	return nil, nil
}

func (d *attachDB) ListVulnScanResults(context.Context, string, bool) ([]*models.VulnScanResult, error) {
	return nil, nil
}

func (d *attachDB) GetVulnScanResult(context.Context, string, string) (*models.VulnScanResult, error) {
	return nil, nil
}

func (d *attachDB) GetLicenseScanResult(context.Context, string, string) (*models.LicenseScanResult, error) {
	return nil, nil
}

func (d *attachDB) SearchComponents(context.Context, string, database.ComponentListOptions) ([]*models.Component, int, error) {
	return nil, 0, nil
}

type fakeRegistry struct {
	mu       sync.Mutex
	attached []docker.ReferrerRequest
	found    string
	err      error
}

func (f *fakeRegistry) AttachReferrer(_ context.Context, _ *models.Repository, req docker.ReferrerRequest) (*docker.ReferrerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}

	f.attached = append(f.attached, req)

	return &docker.ReferrerResult{Digest: "sha256:cccc", Created: true, Replaced: "sha256:dddd"}, nil
}

func (f *fakeRegistry) FindReferrer(context.Context, *models.Repository, string, string, string, map[string]string) (string, error) {
	return f.found, nil
}

func (f *fakeRegistry) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.attached)
}

type attachFixture struct {
	router   chi.Router
	db       *attachDB
	reg      *fakeRegistry
	attacher *AttestationAttacher
	blobs    *storage.FilesystemStore
}

func newAttachFixture(t *testing.T) *attachFixture {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	require.NoError(t, err)

	blobs, err := storage.NewFilesystemStore(t.TempDir())
	require.NoError(t, err)

	keyPEM, err := attest.GenerateKeyPEM()
	require.NoError(t, err)

	keyFile := filepath.Join(t.TempDir(), "key.pem")
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0o600))

	db := &attachDB{}
	reg := &fakeRegistry{}
	eval := auth.NewEvaluator(db, log, false)
	attacher := NewAttestationAttacher(db, log, sbom.NewGenerator(db, nil), attest.NewService(keyFile, nil), reg)

	r := chi.NewRouter()
	NewAttestationAttachHandler(db, log, eval, attacher, blobs).RegisterAuthed(r)

	return &attachFixture{router: r, db: db, reg: reg, attacher: attacher, blobs: blobs}
}

func (f *attachFixture) do(method, target, body string, admin bool) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if admin {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))
	}

	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)

	return rw
}

// attachedStatement decodes the in-toto statement inside the attached Sigstore bundle.
func attachedStatement(t *testing.T, req docker.ReferrerRequest) (string, []string) {
	t.Helper()

	var bundle struct {
		DSSE struct {
			Payload string `json:"payload"`
		} `json:"dsseEnvelope"` //nolint:tagliatelle // Sigstore bundle wire format
	}

	require.NoError(t, json.Unmarshal(req.Content, &bundle))

	raw, err := base64.StdEncoding.DecodeString(bundle.DSSE.Payload)
	require.NoError(t, err)

	var st struct {
		PredicateType string `json:"predicateType"` //nolint:tagliatelle // in-toto wire format
		Subject       []struct {
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}

	require.NoError(t, json.Unmarshal(raw, &st))

	var digests []string
	for _, s := range st.Subject {
		digests = append(digests, s.Digest["sha256"])
	}

	return st.PredicateType, digests
}

func TestAttestationAttachSetting(t *testing.T) {
	f := newAttachFixture(t)

	assert.Equal(t, http.StatusForbidden, f.do(http.MethodGet, "/api/v1/repositories/reg/attestation-attach", "", false).Code)
	assert.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/repositories/nope/attestation-attach", "", true).Code)

	rec := f.do(http.MethodGet, "/api/v1/repositories/reg/attestation-attach", "", true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"enabled":false}`, rec.Body.String())

	rec = f.do(http.MethodPut, "/api/v1/repositories/reg/attestation-attach", `{"enabled":true}`, true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"enabled":true}`, rec.Body.String())
	assert.True(t, f.attacher.Enabled(context.Background(), f.db.repo()))

	f.db.format = "npm"
	rec = f.do(http.MethodPut, "/api/v1/repositories/reg/attestation-attach", `{"enabled":true}`, true)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, f.attacher.Enabled(context.Background(), f.db.repo()), "only docker repositories attach")
}

func TestAttestationAttachVEX(t *testing.T) {
	f := newAttachFixture(t)
	body := `{"artifact_id":"reg/app@1.0","format":"openvex"}`

	rec := f.do(http.MethodPost, "/api/v1/repositories/reg/vex/attach", body, false)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = f.do(http.MethodPost, "/api/v1/repositories/reg/vex/attach", body, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var out struct {
		Subject        string `json:"subject"`
		ReferrerDigest string `json:"referrer_digest"`
		ArtifactType   string `json:"artifact_type"`
		Created        bool   `json:"created"`
		Replaced       string `json:"replaced"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, attachManifestDigest, out.Subject)
	assert.Equal(t, attest.BundleContentType, out.ArtifactType)
	assert.Equal(t, "sha256:cccc", out.ReferrerDigest)
	assert.True(t, out.Created)
	assert.Equal(t, "sha256:dddd", out.Replaced)

	require.Equal(t, 1, f.reg.count())
	req := f.reg.attached[0]
	assert.Equal(t, attachManifestDigest, req.SubjectDigest)
	assert.Equal(t, attest.BundleContentType, req.ArtifactType)
	assert.Equal(t, attest.PredicateOpenVEX, req.Annotations["dev.sigstore.bundle.predicateType"])
	assert.Equal(t, "vex", req.Annotations["dev.openartifact.attestation.kind"])

	predicate, subjects := attachedStatement(t, req)
	assert.Equal(t, attest.PredicateOpenVEX, predicate)
	assert.Contains(t, subjects, strings.TrimPrefix(attachManifestDigest, "sha256:"), "the image manifest digest is the attested subject")
}

func TestAttestationAttachIsIdempotentWhenAlreadyAttached(t *testing.T) {
	f := newAttachFixture(t)
	f.reg.found = "sha256:eeee"

	rec := f.do(http.MethodPost, "/api/v1/repositories/reg/vex/attach", `{"artifact_id":"reg/app@1.0"}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"referrer_digest":"sha256:eeee"`)
	assert.Contains(t, rec.Body.String(), `"created":false`)
	assert.Zero(t, f.reg.count(), "nothing is signed or stored again for an attached document")
}

func TestAttestationAttachErrors(t *testing.T) {
	f := newAttachFixture(t)
	url := "/api/v1/repositories/reg/vex/attach"

	assert.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, url, `{}`, true).Code, "artifact_id is required")
	assert.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, url, `{"artifact_id":"reg/app@1.0","format":"xml"}`, true).Code)
	assert.Equal(t, http.StatusNotFound, f.do(http.MethodPost, url, `{"artifact_id":"reg/app@9.9"}`, true).Code, "unknown tag")
	assert.Equal(t, http.StatusNotFound, f.do(http.MethodPost, url, `{"artifact_id":"other/app@1.0"}`, true).Code, "image of another repository")

	f.reg.err = quota.ErrExceeded
	assert.Equal(t, http.StatusInsufficientStorage, f.do(http.MethodPost, url, `{"artifact_id":"reg/app@1.0"}`, true).Code)

	f.reg.err = docker.ErrSubjectNotFound
	assert.Equal(t, http.StatusNotFound, f.do(http.MethodPost, url, `{"artifact_id":"reg/app@1.0"}`, true).Code)

	f.db.format = "npm"
	assert.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, url, `{"artifact_id":"reg/app@1.0"}`, true).Code)
}

func TestAttestationAttachSBOM(t *testing.T) {
	f := newAttachFixture(t)
	doc := `{"bomFormat":"CycloneDX","specVersion":"1.6"}`
	sha, err := f.blobs.Put(context.Background(), strings.NewReader(doc), nil)
	require.NoError(t, err)

	url := "/api/v1/repositories/reg/sbom/" + attestSBOMID + "/attach"
	f.db.report = &models.SBOMReport{ID: attestSBOMID, RepositoryID: attestRepoID, ArtifactID: "reg/app@1.0", Format: "cyclonedx", BlobSHA256: sha}

	assert.Equal(t, http.StatusNotFound, f.do(http.MethodPost, "/api/v1/repositories/reg/sbom/not-a-uuid/attach", "", true).Code)

	rec := f.do(http.MethodPost, url, "", true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, 1, f.reg.count())

	predicate, subjects := attachedStatement(t, f.reg.attached[0])
	assert.Equal(t, attest.PredicateCycloneDX, predicate)
	assert.Contains(t, subjects, strings.TrimPrefix(attachManifestDigest, "sha256:"))
	assert.Contains(t, subjects, sha, "the SBOM file digest is a subject too")
	assert.True(t, bytes.Contains(f.reg.attached[0].Content, []byte("dsseEnvelope")))

	f.db.report.Format = "spdx-tag"
	assert.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, url, "", true).Code, "tag-value SBOMs cannot be attested")
}

func TestAttestationAttachHooksOnlyRunWhenEnabled(t *testing.T) {
	f := newAttachFixture(t)
	ctx := context.Background()
	report := &models.SBOMReport{ArtifactID: "reg/app@1.0", Format: "cyclonedx", BlobSHA256: strings.Repeat("a", 64)}

	f.attacher.OnSBOMGenerated(ctx, f.db.repo(), report, []byte(`{}`))
	f.attacher.OnScan(ctx, attestRepoID, "reg/app@1.0")
	assert.Zero(t, f.reg.count(), "the default is off")

	f.db.enabled = true

	f.attacher.OnSBOMGenerated(ctx, f.db.repo(), report, []byte(`{}`))
	assert.Equal(t, 1, f.reg.count())

	f.attacher.OnScan(ctx, attestRepoID, "reg/app@1.0")
	assert.Equal(t, 3, f.reg.count(), "a scan attaches the SBOM and the VEX document")
}
