// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/go-chi/chi/v5"
	rpmutils "github.com/sassoftware/go-rpmutils"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/gpgkeys/gpgkeystest"
	"github.com/openartifact/openartifact/pkg/gpg"
)

type signUploadEnv struct {
	db     *MockDatabase
	blobs  *MockBlobStore
	router chi.Router
	svc    *gpgkeys.Service
}

func newSignUploadEnv(t *testing.T, signPackages bool) *signUploadEnv {
	t.Helper()

	if err := crypto.Init("rpm-sign-test-secret-0123456789ab"); err != nil {
		t.Fatalf("crypto.Init: %v", err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	db := newMockDatabase()
	blobs := newMockBlobStore()
	db.repositories["repo-sign"] = &models.Repository{ID: "repo-sign", Name: "signed", Format: "rpm", SignPackages: signPackages}

	svc := gpgkeys.New(gpgkeystest.New(), newHandlerLogger(t), nil)

	h := api.NewRPMUploadHandler(newHandlerLogger(t), db, blobs, nil)
	h.SetKeyResolver(svc)

	router := chi.NewRouter()
	h.Register(router)

	return &signUploadEnv{db: db, blobs: blobs, router: router, svc: svc}
}

func (e *signUploadEnv) activateKey(t *testing.T) *openpgp.Entity {
	t.Helper()

	ctx := context.Background()

	key, err := e.svc.Generate(ctx, gpgkeys.GenerateInput{Name: "pkg-key", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.svc.Activate(ctx, key.ID, nil, "tester"); err != nil {
		t.Fatal(err)
	}

	pub, _, err := e.svc.PublicKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}

	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(pub))
	if err != nil || len(ring) != 1 {
		t.Fatalf("public key ring: %v (%d)", err, len(ring))
	}

	return ring[0]
}

func (e *signUploadEnv) upload(t *testing.T, data []byte) *httptest.ResponseRecorder {
	t.Helper()

	rw := httptest.NewRecorder()
	e.router.ServeHTTP(rw, buildRPMUploadRequest(t, data, "signed"))

	return rw
}

func (e *signUploadEnv) onlyBlob(t *testing.T) []byte {
	t.Helper()

	e.blobs.mu.Lock()
	defer e.blobs.mu.Unlock()

	if len(e.blobs.blobs) != 1 {
		t.Fatalf("blobs stored = %d, want 1", len(e.blobs.blobs))
	}

	for _, b := range e.blobs.blobs {
		return b
	}

	return nil
}

func helloRPM(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "pkg", "rpmsign", "testdata", "hello.rpm"))
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestRPMUploadSignsPackagesWithTheActiveKey(t *testing.T) {
	e := newSignUploadEnv(t, true)
	entity := e.activateKey(t)
	orig := helloRPM(t)

	rw := e.upload(t, orig)
	if rw.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rw.Code, rw.Body.String())
	}

	var resp struct {
		Data struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	stored := e.onlyBlob(t)
	sum := sha256.Sum256(stored)

	// The stored file is the signed one: its digest, not the upload's, is the identity everywhere.
	if resp.Data.SHA256 != hex.EncodeToString(sum[:]) || resp.Data.Size != int64(len(stored)) {
		t.Errorf("response sha/size = %s/%d, stored = %x/%d", resp.Data.SHA256, resp.Data.Size, sum, len(stored))
	}

	if origSum := sha256.Sum256(orig); sum == origSum {
		t.Error("stored package is the unsigned upload")
	}

	if _, _, err := rpmutils.Verify(bytes.NewReader(stored), openpgp.EntityList{entity}); err != nil {
		t.Fatalf("stored package does not verify against the active key: %v", err)
	}

	for _, c := range e.db.components {
		if c.PackageSize != int64(len(stored)) {
			t.Errorf("component package size = %d, stored %d", c.PackageSize, len(stored))
		}

		if c.HeaderEnd <= c.HeaderStart || c.HeaderStart == 0 {
			t.Errorf("component header range = %d-%d", c.HeaderStart, c.HeaderEnd)
		}
	}

	// Uploading the same package again is still a duplicate, signed or not.
	if again := e.upload(t, orig); again.Code != http.StatusConflict {
		t.Errorf("second upload: %d %s", again.Code, again.Body.String())
	}
}

func TestRPMUploadWithoutSignPackagesStoresTheUploadUnchanged(t *testing.T) {
	e := newSignUploadEnv(t, false)
	e.activateKey(t)
	orig := helloRPM(t)

	if rw := e.upload(t, orig); rw.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rw.Code, rw.Body.String())
	}

	if !bytes.Equal(e.onlyBlob(t), orig) {
		t.Error("an unsigned repository altered the uploaded bytes")
	}
}

func TestRPMUploadSigningNeedsAKey(t *testing.T) {
	e := newSignUploadEnv(t, true)

	rw := e.upload(t, helloRPM(t))
	if rw.Code != http.StatusConflict || !bytes.Contains(rw.Body.Bytes(), []byte("SIGNING_KEY_REQUIRED")) {
		t.Fatalf("upload without a key: %d %s", rw.Code, rw.Body.String())
	}

	if len(e.blobs.blobs) != 0 || len(e.db.components) != 0 {
		t.Error("a rejected upload left a blob or a row behind")
	}
}

func TestRPMUploadReportsUnsignablePackages(t *testing.T) {
	e := newSignUploadEnv(t, true)
	e.activateKey(t)

	// Parses as an rpm but has no payload digest to sign over.
	rw := e.upload(t, buildMinimalRPM())
	if rw.Code != http.StatusUnprocessableEntity || !bytes.Contains(rw.Body.Bytes(), []byte("SIGNING_FAILED")) {
		t.Fatalf("unsignable upload: %d %s", rw.Code, rw.Body.String())
	}

	if len(e.blobs.blobs) != 0 || len(e.db.components) != 0 {
		t.Error("a rejected upload left a blob or a row behind")
	}
}
