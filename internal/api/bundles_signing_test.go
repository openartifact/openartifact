// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/gpgkeys/gpgkeystest"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// signingDB is a bundle mock that remembers what the seal stored, so the
// manifest/signature routes can serve it back.
type signingDB struct {
	bundlesScansMockDB

	items   []*models.ReleaseBundleItem
	gotSig  *models.BundleSignature
	sealRet bool
}

func (db *signingDB) ListBundleItems(_ context.Context, _ string) ([]*models.ReleaseBundleItem, error) {
	return db.items, nil
}

func (db *signingDB) SealReleaseBundle(_ context.Context, _, _ string, _ time.Time, sig *models.BundleSignature) (bool, error) {
	db.sealCalled = true
	db.gotSig = sig

	if !db.sealRet {
		return false, nil
	}

	db.bundle.State = models.BundleStateSealed

	if sig != nil {
		db.bundle.GPGKeyID = &sig.KeyFingerprint
		db.bundle.GPGSignature = &sig.Signature
	}

	return true, nil
}

func signingFixture(t *testing.T) (*signingDB, *BundlesHandler, *gpgkeys.Service) {
	t.Helper()

	if err := crypto.Init("bundle-signing-test-secret-0123456789"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	db := &signingDB{
		items: []*models.ReleaseBundleItem{
			{ID: "bbbbbbbb-0000-4000-8000-000000000002", SHA256: strings.Repeat("b", 64), ArtifactPath: "pkg/b.tgz", Format: "npm", SizeBytes: 20, SourceRepoID: testRepoID},
			{ID: "aaaaaaaa-0000-4000-8000-000000000001", SHA256: strings.Repeat("a", 64), ArtifactPath: "pkg/a.tgz", Format: "npm", SizeBytes: 10, SourceRepoID: testRepoID},
		},
		sealRet: true,
	}
	db.bundle = &models.ReleaseBundle{
		ID: "55555555-5555-5555-5555-555555555555", Name: "rel", Version: "1.0", Description: "release <1.0> & co",
		State: models.BundleStateDraft,
	}

	log, ev := newBundlesScansEnv(t, &db.bundlesScansMockDB)
	svc := gpgkeys.New(gpgkeystest.New(), log, nil)

	return db, NewBundlesHandler(db, log, ev).WithGPGKeys(svc), svc
}

func TestBundleSealSignsTheManifest(t *testing.T) {
	db, h, svc := signingFixture(t)

	key, err := svc.Generate(context.Background(), gpgkeys.GenerateInput{Name: "release", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/rel/1.0/seal", `{"gpg_key_id":"`+key.ID+`"}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("seal: %d %s", rw.Code, rw.Body.String())
	}

	var sealed map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &sealed); err != nil {
		t.Fatal(err)
	}

	if sealed["gpg_key_id"] != key.Fingerprint {
		t.Errorf("gpg_key_id = %v, want the signer fingerprint %s", sealed["gpg_key_id"], key.Fingerprint)
	}

	if s, _ := sealed["gpg_signature"].(string); !strings.Contains(s, "BEGIN PGP SIGNATURE") {
		t.Errorf("gpg_signature = %.40q", s)
	}

	if got := db.gotSig.ItemIDs; len(got) != 2 || got[0] > got[1] {
		t.Errorf("signature must pin the sorted item ids, got %v", got)
	}

	verifyServedManifest(t, h, svc, key.ID)
}

// verifyServedManifest downloads manifest and signature and checks them against the key.
func verifyServedManifest(t *testing.T, h *BundlesHandler, svc *gpgkeys.Service, keyID string) {
	t.Helper()

	man := serveAs(h.RegisterAuthed, http.MethodGet, "/api/v1/bundles/rel/1.0/manifest", "")
	asc := serveAs(h.RegisterAuthed, http.MethodGet, "/api/v1/bundles/rel/1.0/manifest.asc", "")

	if man.Code != http.StatusOK || man.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("manifest: %d %s", man.Code, man.Header().Get("Content-Type"))
	}

	if asc.Code != http.StatusOK || asc.Header().Get("Content-Type") != "application/pgp-signature" {
		t.Fatalf("manifest.asc: %d %s", asc.Code, asc.Header().Get("Content-Type"))
	}

	pub, _, err := svc.PublicKey(context.Background(), keyID)
	if err != nil {
		t.Fatal(err)
	}

	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(pub))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := openpgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(man.Body.Bytes()), bytes.NewReader(asc.Body.Bytes()), nil); err != nil {
		t.Fatalf("signature does not verify against the served manifest: %v", err)
	}

	// A tampered manifest does not.
	tampered := bytes.Replace(man.Body.Bytes(), []byte(`"1.0"`), []byte(`"1.1"`), 1)
	if _, err := openpgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(tampered), bytes.NewReader(asc.Body.Bytes()), nil); err == nil {
		t.Error("a modified manifest verified")
	}

	checkManifestDocument(t, man.Body.Bytes())
}

// checkManifestDocument asserts the manifest is canonical: items by digest, not by insertion order.
func checkManifestDocument(t *testing.T, body []byte) {
	t.Helper()

	var doc bundleManifest
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Schema != bundleManifestSchema || doc.Name != "rel" || doc.Version != "1.0" || len(doc.Items) != 2 ||
		doc.Items[0].SHA256 != strings.Repeat("a", 64) || doc.Items[1].ArtifactPath != "pkg/b.tgz" {
		t.Errorf("manifest = %+v", doc)
	}

	if bytes.Contains(body, []byte(`\u003c`)) {
		t.Error("manifest HTML-escapes its content; bytes must stay human-readable")
	}
}

func TestBundleSealWithFingerprintAndConfigKey(t *testing.T) {
	db, h, svc := signingFixture(t)

	key, err := svc.Generate(context.Background(), gpgkeys.GenerateInput{Name: "release", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/rel/1.0/seal", `{"gpg_key_id":"`+strings.ToLower(key.Fingerprint)+`"}`)
	if rw.Code != http.StatusOK || db.gotSig == nil || db.gotSig.KeyFingerprint != key.Fingerprint {
		t.Errorf("by fingerprint: %d %s", rw.Code, rw.Body.String())
	}

	// The read-only config key can sign bundles too.
	cfgEntity, err := gpg.GenerateEntity("Config", "", "", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	cfgSigner, err := gpg.NewLocalSignerFromEntity(cfgEntity)
	if err != nil {
		t.Fatal(err)
	}

	db.bundle.State = models.BundleStateDraft
	h.WithGPGKeys(gpgkeys.New(gpgkeystest.New(), svcLog(t, db), cfgSigner))

	rw = serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/rel/1.0/seal", `{"gpg_key_id":"config"}`)
	if rw.Code != http.StatusOK || db.gotSig.KeyFingerprint != cfgSigner.KeyFingerprint() {
		t.Errorf("config key: %d %s", rw.Code, rw.Body.String())
	}
}

func svcLog(t *testing.T, db *signingDB) *logger.Logger {
	t.Helper()

	log, _ := newBundlesScansEnv(t, &db.bundlesScansMockDB)

	return log
}

func TestBundleSealRejectsBadKeys(t *testing.T) {
	db, h, _ := signingFixture(t)

	for name, body := range map[string]string{
		"unknown id":          `{"gpg_key_id":"00000000-0000-4000-8000-00000000ffff"}`,
		"unknown fingerprint": `{"gpg_key_id":"DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF"}`,
		"garbage":             `{"gpg_key_id":"nope"}`,
		"empty":               `{"gpg_key_id":"  "}`,
		"config not set":      `{"gpg_key_id":"config"}`,
	} {
		db.sealCalled = false

		rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/rel/1.0/seal", body)
		if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "validation_failed") {
			t.Errorf("%s: %d %s", name, rw.Code, rw.Body.String())
		}

		if db.sealCalled {
			t.Errorf("%s: the bundle was sealed anyway", name)
		}
	}
}

func TestBundleSealWithoutKeyStaysUnsigned(t *testing.T) {
	db, h, _ := signingFixture(t)

	rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/rel/1.0/seal", `{}`)
	if rw.Code != http.StatusOK || db.gotSig != nil {
		t.Fatalf("unsigned seal: %d sig=%v", rw.Code, db.gotSig)
	}

	if asc := serveAs(h.RegisterAuthed, http.MethodGet, "/api/v1/bundles/rel/1.0/manifest.asc", ""); asc.Code != http.StatusNotFound {
		t.Errorf("manifest.asc of an unsigned bundle: %d, want 404", asc.Code)
	}

	if man := serveAs(h.RegisterAuthed, http.MethodGet, "/api/v1/bundles/rel/1.0/manifest", ""); man.Code != http.StatusOK {
		t.Errorf("manifest of an unsigned bundle: %d, want 200", man.Code)
	}
}

func TestBundleSealConflictWhenItemsChanged(t *testing.T) {
	db, h, svc := signingFixture(t)
	db.sealRet = false // the conditional UPDATE matched nothing

	key, err := svc.Generate(context.Background(), gpgkeys.GenerateInput{Name: "k", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/rel/1.0/seal", `{"gpg_key_id":"`+key.ID+`"}`)
	if rw.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", rw.Code, rw.Body.String())
	}
}
