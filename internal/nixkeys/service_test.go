// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nixkeys

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/nix"
)

type memStore struct {
	mu   sync.Mutex
	keys map[string]*models.NixSigningKey
}

func (m *memStore) GetNixSigningKey(_ context.Context, repoID string) (*models.NixSigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.keys[repoID], nil
}

func (m *memStore) CreateNixSigningKeyIfAbsent(_ context.Context, k *models.NixSigningKey) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.keys[k.RepositoryID]; ok {
		return false, nil
	}

	m.keys[k.RepositoryID] = k

	return true, nil
}

func (m *memStore) ReplaceNixSigningKey(_ context.Context, k *models.NixSigningKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.keys[k.RepositoryID] = k

	return nil
}

func withSecretKey(t *testing.T) {
	t.Helper()

	if err := crypto.Init(strings.Repeat("s", 32)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })
}

func newService(t *testing.T, st *memStore) *Service {
	t.Helper()

	return New(st, formattest.NewLogger(t))
}

func newStore() *memStore { return &memStore{keys: map[string]*models.NixSigningKey{}} }

var testRepo = &models.Repository{ID: "r1", Name: "cache"}

// verifies reports whether sig, made by key, covers the narinfo under the public key line.
func verifies(t *testing.T, publicKey string, key *nix.SecretKey) bool {
	t.Helper()

	ni := &nix.NarInfo{
		StorePath: nix.StoreDir + "/" + strings.Repeat("a", 32) + "-hello",
		NarHash:   "sha256:" + strings.Repeat("ab", 32),
		NarSize:   1234,
	}

	sig, err := key.Sign(ni)
	if err != nil {
		t.Fatal(err)
	}

	fp, err := ni.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	name, pub, _ := strings.Cut(publicKey, ":")
	signame, b64, _ := strings.Cut(sig, ":")

	rawPub, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		t.Fatal(err)
	}

	rawSig, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}

	return name == signame && ed25519.Verify(rawPub, []byte(fp), rawSig)
}

func TestDefaultKeyName(t *testing.T) {
	if got := DefaultKeyName("cache", 2); got != "openartifact-cache-2" {
		t.Errorf("DefaultKeyName = %q", got)
	}
}

func TestWithoutSecretKeyNothingIsSigned(t *testing.T) {
	st := newStore()
	svc := newService(t, st)
	ctx := context.Background()

	if key, err := svc.SecretKeyForRepo(ctx, testRepo); key != nil || err != nil {
		t.Fatalf("SecretKeyForRepo = %v, %v; want nil, nil", key, err)
	}

	if len(st.keys) != 0 {
		t.Error("no key may be stored without a secret key")
	}

	calls := map[string]func() error{
		"Info":     func() error { _, err := svc.Info(ctx, testRepo); return err },
		"Generate": func() error { _, err := svc.Generate(ctx, testRepo, "u"); return err },
		"Import":   func() error { _, err := svc.Import(ctx, testRepo, "x", "", "u"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrSecretKeyRequired) {
			t.Errorf("%s error = %v, want ErrSecretKeyRequired", name, err)
		}
	}
}

func TestFirstUseGeneratesOnceAndSealsTheSecret(t *testing.T) {
	withSecretKey(t)

	st := newStore()
	svc := newService(t, st)
	ctx := context.Background()

	first, err := svc.SecretKeyForRepo(ctx, testRepo)
	if err != nil || first == nil {
		t.Fatalf("SecretKeyForRepo = %v, %v", first, err)
	}

	stored := st.keys["r1"]
	if stored == nil || stored.KeyName != "openartifact-cache-1" || stored.CreatedAt.IsZero() {
		t.Fatalf("stored key = %+v", stored)
	}

	enc := string(stored.PrivateKeyEnc)
	if !crypto.IsEncrypted(enc) || strings.Contains(enc, first.String()) || strings.Contains(enc, "openartifact-cache-1:") {
		t.Fatal("the private key must be stored sealed")
	}

	second, err := svc.SecretKeyForRepo(ctx, testRepo)
	if err != nil || second.String() != first.String() {
		t.Fatalf("second call returned another key (%v)", err)
	}

	info, err := svc.Info(ctx, testRepo)
	if err != nil || info.PublicKey != first.PublicKey() || info.KeyName != "openartifact-cache-1" {
		t.Fatalf("Info = %+v, %v", info, err)
	}

	if !verifies(t, info.PublicKey, first) {
		t.Error("a narinfo signed by the key must verify under the published public key")
	}
}

func TestGenerateRotatesAndOtherInstancesFollow(t *testing.T) {
	withSecretKey(t)

	st := newStore()
	svc, other := newService(t, st), newService(t, st)
	ctx := context.Background()

	before, err := other.SecretKeyForRepo(ctx, testRepo)
	if err != nil {
		t.Fatal(err)
	}

	after, err := svc.Generate(ctx, testRepo, "admin")
	if err != nil {
		t.Fatal(err)
	}

	if after.KeyName != "openartifact-cache-2" || after.PublicKey == before.PublicKey() || st.keys["r1"].CreatedBy != "admin" {
		t.Errorf("rotated key = %+v", after)
	}

	// The second instance cached the old key; the changed record must evict it.
	now, err := other.SecretKeyForRepo(ctx, testRepo)
	if err != nil || now.PublicKey() != after.PublicKey || !verifies(t, after.PublicKey, now) {
		t.Fatalf("after a rotation the other instance signs with %v (%v)", now, err)
	}

	third, err := svc.Generate(ctx, testRepo, "admin")
	if err != nil || third.KeyName != "openartifact-cache-3" {
		t.Errorf("second rotation = %+v, %v", third, err)
	}
}

func TestImportKeepsOrRenamesTheKey(t *testing.T) {
	withSecretKey(t)

	svc := newService(t, newStore())
	ctx := context.Background()

	src, err := nix.GenerateKey("ci.example.org-1")
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct{ keyName, want string }{
		"keeps the name": {"", "ci.example.org-1"},
		"same name":      {"ci.example.org-1", "ci.example.org-1"},
		"renames":        {"cache.example.org-7", "cache.example.org-7"},
	} {
		info, err := svc.Import(ctx, testRepo, src.String(), tc.keyName, "admin")
		if err != nil || info.KeyName != tc.want {
			t.Fatalf("%s: Import = %+v, %v", name, info, err)
		}

		key, err := svc.SecretKeyForRepo(ctx, testRepo)
		if err != nil || key.Name() != tc.want || !verifies(t, info.PublicKey, key) {
			t.Errorf("%s: SecretKeyForRepo = %v, %v", name, key, err)
		}
	}
}

func TestImportRejections(t *testing.T) {
	withSecretKey(t)

	st := newStore()
	svc := newService(t, st)
	good, err := nix.GenerateKey("k-1")
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct{ secret, keyName string }{
		"not a key":          {"hello", ""},
		"no name":            {":AAAA", ""},
		"bad base64":         {"k-1:!!!", ""},
		"short key":          {"k-1:AAAA", ""},
		"bad name":           {good.String(), "../evil"},
		"smuggled ciphertex": {"enc:v1:AAAA", ""},
	} {
		if _, err := svc.Import(context.Background(), testRepo, tc.secret, tc.keyName, "u"); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%s: error = %v, want ErrInvalidKey", name, err)
		}
	}

	if len(st.keys) != 0 {
		t.Error("a rejected import must store nothing")
	}
}
