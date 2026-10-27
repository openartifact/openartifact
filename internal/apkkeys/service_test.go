// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apkkeys

import (
	"context"
	"crypto/rsa"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/apk"
)

type memStore struct {
	mu   sync.Mutex
	keys map[string]*models.ApkSigningKey
}

func newMemStore() *memStore { return &memStore{keys: map[string]*models.ApkSigningKey{}} }

func (m *memStore) GetApkSigningKey(_ context.Context, repoID string) (*models.ApkSigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.keys[repoID], nil
}

func (m *memStore) CreateApkSigningKeyIfAbsent(_ context.Context, k *models.ApkSigningKey) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.keys[k.RepositoryID]; ok {
		return false, nil
	}

	m.keys[k.RepositoryID] = k

	return true, nil
}

func (m *memStore) ReplaceApkSigningKey(_ context.Context, k *models.ApkSigningKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.keys[k.RepositoryID] = k

	return nil
}

type recordingRefresher struct{ repos []string }

func (r *recordingRefresher) NotifyStale(id string) { r.repos = append(r.repos, id) }

func withSecretKey(t *testing.T) {
	t.Helper()

	if err := crypto.Init(strings.Repeat("k", 32)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })
}

func newService(t *testing.T) (*Service, *memStore, *recordingRefresher) {
	t.Helper()

	st := newMemStore()
	ref := &recordingRefresher{}
	svc := New(st, formattest.NewLogger(t))
	svc.SetRefresher(ref)

	return svc, st, ref
}

func TestDefaultKeyName(t *testing.T) {
	if got := DefaultKeyName("alpine"); got != "openartifact-alpine.rsa.pub" {
		t.Errorf("short name: %q", got)
	}

	long := strings.Repeat("a", 100)
	other := strings.Repeat("a", 99) + "b"
	got, gotOther := DefaultKeyName(long), DefaultKeyName(other)

	if len(got) > maxKeyNameBytes || !strings.HasSuffix(got, ".rsa.pub") {
		t.Errorf("long name %q (%d bytes) does not fit a signature member name", got, len(got))
	}

	if got == gotOther {
		t.Error("distinct long repository names must get distinct key names")
	}

	if _, err := apk.NewSigner(got, mustKey(t)); err != nil {
		t.Errorf("pkg/apk rejects the shortened name: %v", err)
	}
}

func mustKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	k, err := apk.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}

	return k
}

func TestSignerForRepoGeneratesOnceAndSealsTheSecret(t *testing.T) {
	withSecretKey(t)

	svc, st, _ := newService(t)
	repo := &models.Repository{ID: "r1", Name: "alp"}

	first, err := svc.SignerForRepo(context.Background(), repo)
	if err != nil || first == nil {
		t.Fatalf("SignerForRepo = %v, %v", first, err)
	}

	stored := st.keys["r1"]
	if stored == nil || !crypto.IsEncrypted(string(stored.PrivateKeyEnc)) || strings.Contains(string(stored.PrivateKeyEnc), "PRIVATE KEY") {
		t.Fatal("the private key must be stored sealed")
	}

	if stored.KeyName != "openartifact-alp.rsa.pub" || stored.CreatedAt.IsZero() {
		t.Errorf("stored key = %+v", stored)
	}

	second, err := svc.SignerForRepo(context.Background(), repo)
	if err != nil || second.Fingerprint() != first.Fingerprint() {
		t.Fatalf("second call returned another key (%v)", err)
	}

	info, err := svc.Info(context.Background(), repo)
	if err != nil || info.Fingerprint != first.Fingerprint() || strings.Contains(info.PublicKey, "PRIVATE") {
		t.Fatalf("Info = %+v, %v", info, err)
	}
}

func TestWithoutSecretKeyTheIndexIsUnsigned(t *testing.T) {
	svc, st, _ := newService(t)
	repo := &models.Repository{ID: "r1", Name: "alp"}

	if signer, err := svc.SignerForRepo(context.Background(), repo); signer != nil || err != nil {
		t.Fatalf("SignerForRepo = %v, %v; want nil, nil", signer, err)
	}

	if len(st.keys) != 0 {
		t.Error("no key may be stored without a secret key")
	}

	if _, err := svc.Info(context.Background(), repo); !errors.Is(err, ErrSecretKeyRequired) {
		t.Errorf("Info error = %v", err)
	}

	if _, err := svc.Generate(context.Background(), repo, "u"); !errors.Is(err, ErrSecretKeyRequired) {
		t.Errorf("Generate error = %v", err)
	}

	if _, err := svc.Import(context.Background(), repo, "x", "", "u"); !errors.Is(err, ErrSecretKeyRequired) {
		t.Errorf("Import error = %v", err)
	}
}

func TestImportReplacesTheKeyAndResigns(t *testing.T) {
	withSecretKey(t)

	svc, st, ref := newService(t)
	repo := &models.Repository{ID: "r1", Name: "alp"}

	pemKey := string(apk.MarshalPrivateKeyPEM(mustKey(t)))

	info, err := svc.Import(context.Background(), repo, pemKey, "ci@example.rsa.pub", "admin")
	if err != nil {
		t.Fatal(err)
	}

	if info.KeyName != "ci@example.rsa.pub" || st.keys["r1"].CreatedBy != "admin" {
		t.Errorf("imported key = %+v", info)
	}

	if len(ref.repos) != 1 || ref.repos[0] != "r1" {
		t.Errorf("refresher notified for %v, want [r1]", ref.repos)
	}

	signer, err := svc.SignerForRepo(context.Background(), repo)
	if err != nil || signer.KeyName() != "ci@example.rsa.pub" {
		t.Fatalf("SignerForRepo after import = %v, %v", signer, err)
	}
}

func TestImportRejections(t *testing.T) {
	withSecretKey(t)

	svc, st, ref := newService(t)
	repo := &models.Repository{ID: "r1", Name: "alp"}
	good := string(apk.MarshalPrivateKeyPEM(mustKey(t)))

	for name, tc := range map[string]struct{ pem, keyName string }{
		"not a key":          {"hello", ""},
		"public key only":    {"-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n", ""},
		"bad name":           {good, "../evil.rsa.pub"},
		"name without .pub":  {good, "evil.rsa"},
		"name too long":      {good, strings.Repeat("a", 90) + ".rsa.pub"},
		"smuggled ciphertex": {"enc:v1:AAAA", ""},
	} {
		if _, err := svc.Import(context.Background(), repo, tc.pem, tc.keyName, "u"); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%s: error = %v, want ErrInvalidKey", name, err)
		}
	}

	if len(st.keys) != 0 || len(ref.repos) != 0 {
		t.Error("a rejected import must change nothing")
	}
}

func TestGenerateRotatesTheKey(t *testing.T) {
	withSecretKey(t)

	svc, _, ref := newService(t)
	repo := &models.Repository{ID: "r1", Name: "alp"}

	before, err := svc.Info(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	after, err := svc.Generate(context.Background(), repo, "admin")
	if err != nil {
		t.Fatal(err)
	}

	if after.Fingerprint == before.Fingerprint {
		t.Error("Generate must produce a new key")
	}

	if len(ref.repos) != 1 {
		t.Errorf("refresher notified %d times, want 1", len(ref.repos))
	}
}
