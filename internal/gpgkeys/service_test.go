// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpgkeys

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys/gpgkeystest"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

type recorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *recorder) NotifyStale(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.ids = append(r.ids, id)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.ids...)
}

func testLog(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return log
}

// withSecretKey enables at-rest encryption for the test.
func withSecretKey(t *testing.T) {
	t.Helper()

	if err := crypto.Init("gpgkeys-service-test-secret-0123456789"); err != nil {
		t.Fatalf("crypto.Init: %v", err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })
}

func newService(t *testing.T, config gpg.Signer) (*Service, *gpgkeystest.Store, *recorder, *recorder) {
	t.Helper()

	store := gpgkeystest.New()
	svc := New(store, testLog(t), config)
	svc.autoAlgorithm = gpg.AlgorithmED25519 // RSA-4096 generation is too slow for unit tests

	rpm, deb := &recorder{}, &recorder{}
	svc.SetRefreshers(rpm, deb)

	return svc, store, rpm, deb
}

// newBareService is newService for tests that need neither the store nor the refreshers.
func newBareService(t *testing.T, config gpg.Signer) *Service {
	t.Helper()

	svc, _, _, _ := newService(t, config) //nolint:dogsled // only the service matters here

	return svc
}

func protectedArmored(t *testing.T, passphrase string) (string, string) {
	t.Helper()

	e, err := gpg.GenerateEntity("Imported", "", "imp@example.com", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := gpg.NewLocalSignerFromEntity(e)
	if err != nil {
		t.Fatal(err)
	}

	fingerprint := signer.KeyFingerprint()

	if passphrase != "" {
		if err := e.PrivateKey.Encrypt([]byte(passphrase)); err != nil {
			t.Fatal(err)
		}

		for _, sub := range e.Subkeys {
			if err := sub.PrivateKey.Encrypt([]byte(passphrase)); err != nil {
				t.Fatal(err)
			}
		}
	}

	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}

	_ = w.Close()

	return buf.String(), fingerprint
}

func configSigner(t *testing.T) gpg.Signer {
	t.Helper()

	e, err := gpg.GenerateEntity("Config", "", "", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	s, err := gpg.NewLocalSignerFromEntity(e)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestPrivateKeysAreEncryptedAtRest(t *testing.T) {
	withSecretKey(t)

	svc, store, _, _ := newService(t, nil)
	ctx := context.Background()

	armored, fp := protectedArmored(t, "s3cret-passphrase")

	imported, err := svc.Import(ctx, ImportInput{Name: "imp", PrivateKey: armored, Passphrase: "s3cret-passphrase", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	generated, err := svc.Generate(ctx, GenerateInput{Name: "gen", Algorithm: gpg.AlgorithmED25519, CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, k := range []*models.GPGKey{imported, generated} {
		raw, ok := store.Raw(k.ID)
		if !ok {
			t.Fatalf("key %s not stored", k.Name)
		}

		if !crypto.IsEncrypted(raw.PrivateKeyEnc) {
			t.Errorf("%s: stored private key is not sealed: %.40q", k.Name, raw.PrivateKeyEnc)
		}

		if strings.Contains(raw.PrivateKeyEnc, "PRIVATE KEY") || strings.Contains(raw.PublicKey, "PRIVATE KEY") {
			t.Errorf("%s: plaintext private key material in the stored record", k.Name)
		}

		if !strings.Contains(raw.PublicKey, "BEGIN PGP PUBLIC KEY BLOCK") {
			t.Errorf("%s: public key not stored armored", k.Name)
		}

		// Round-trips through the secret key and signs.
		signer, _, err := svc.SignerByID(ctx, k.ID)
		if err != nil {
			t.Fatalf("%s: SignerByID: %v", k.Name, err)
		}

		if _, err := signer.DetachedSign(strings.NewReader("data")); err != nil {
			t.Errorf("%s: cannot sign after decrypt: %v", k.Name, err)
		}
	}

	if imported.Fingerprint != fp {
		t.Errorf("imported fingerprint = %s, want %s", imported.Fingerprint, fp)
	}

	// The passphrase is not part of what is stored: the key is stored unlocked and sealed.
	raw, _ := store.Raw(imported.ID)
	if strings.Contains(raw.PrivateKeyEnc, "s3cret-passphrase") {
		t.Error("passphrase leaked into storage")
	}
}

func TestStoringKeysRequiresSecretKey(t *testing.T) {
	svc, store, _, _ := newService(t, nil) // crypto disabled
	ctx := context.Background()

	armored, _ := protectedArmored(t, "")

	if _, err := svc.Import(ctx, ImportInput{PrivateKey: armored}); !errors.Is(err, ErrSecretKeyRequired) {
		t.Errorf("Import err = %v, want ErrSecretKeyRequired", err)
	}

	if _, err := svc.Generate(ctx, GenerateInput{Algorithm: gpg.AlgorithmED25519}); !errors.Is(err, ErrSecretKeyRequired) {
		t.Errorf("Generate err = %v, want ErrSecretKeyRequired", err)
	}

	if _, err := svc.AutoGenerate(ctx); !errors.Is(err, ErrSecretKeyRequired) {
		t.Errorf("AutoGenerate err = %v, want ErrSecretKeyRequired", err)
	}

	if store.Count() != 0 {
		t.Errorf("%d keys stored without a secret key", store.Count())
	}
}

func TestImportRejections(t *testing.T) {
	withSecretKey(t)

	svc, store, _, _ := newService(t, nil)
	ctx := context.Background()

	protected, _ := protectedArmored(t, "pw")

	if _, err := svc.Import(ctx, ImportInput{PrivateKey: protected}); !errors.Is(err, gpg.ErrPassphraseRequired) {
		t.Errorf("missing passphrase: err = %v", err)
	}

	if _, err := svc.Import(ctx, ImportInput{PrivateKey: protected, Passphrase: "nope"}); !errors.Is(err, gpg.ErrBadPassphrase) {
		t.Errorf("wrong passphrase: err = %v", err)
	}

	if _, err := svc.Import(ctx, ImportInput{PrivateKey: "garbage"}); err == nil {
		t.Error("garbage key was accepted")
	}

	if _, err := svc.Import(ctx, ImportInput{PrivateKey: "enc:v1:abcdef"}); err == nil {
		t.Error("an already-encrypted value was accepted as a private key")
	}

	if store.Count() != 0 {
		t.Errorf("%d keys stored by rejected imports", store.Count())
	}

	plain, _ := protectedArmored(t, "")
	if _, err := svc.Import(ctx, ImportInput{Name: "a", PrivateKey: plain}); err != nil {
		t.Fatalf("first import: %v", err)
	}

	if _, err := svc.Import(ctx, ImportInput{Name: "b", PrivateKey: plain}); !errors.Is(err, ErrExists) {
		t.Errorf("same fingerprint twice: err = %v, want ErrExists", err)
	}
}

func TestSignerPrecedence(t *testing.T) {
	withSecretKey(t)

	cfg := configSigner(t)
	svc, store, _, _ := newService(t, cfg)
	store.AddRepository("repo-a", "apt-a", "deb")
	store.AddRepository("repo-b", "yum-b", "rpm")

	ctx := context.Background()

	global, err := svc.Generate(ctx, GenerateInput{Name: "global", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	own, err := svc.Generate(ctx, GenerateInput{Name: "own", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	fpOf := func(repo string) string {
		t.Helper()

		s, err := svc.SignerForRepo(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}

		if s == nil {
			return ""
		}

		return s.KeyFingerprint()
	}

	if got := fpOf("repo-a"); got != cfg.KeyFingerprint() {
		t.Errorf("nothing active: signer = %s, want the config key", got)
	}

	if err := svc.Activate(ctx, global.ID, nil, "admin"); err != nil {
		t.Fatal(err)
	}

	if got := fpOf("repo-a"); got != global.Fingerprint {
		t.Errorf("global active: signer = %s, want %s", got, global.Fingerprint)
	}

	if err := svc.Activate(ctx, own.ID, &models.GPGSigningRepo{ID: "repo-a", Format: "deb"}, "admin"); err != nil {
		t.Fatal(err)
	}

	if got := fpOf("repo-a"); got != own.Fingerprint {
		t.Errorf("repository key: signer = %s, want %s", got, own.Fingerprint)
	}

	if got := fpOf("repo-b"); got != global.Fingerprint {
		t.Errorf("other repository: signer = %s, want the global key %s", got, global.Fingerprint)
	}

	if err := svc.Deactivate(ctx, global.ID, nil); err != nil {
		t.Fatal(err)
	}

	if got := fpOf("repo-b"); got != cfg.KeyFingerprint() {
		t.Errorf("global deactivated: signer = %s, want the config key", got)
	}

	// Without any key at all, the repository is simply unsigned.
	none := newBareService(t, nil)
	if s, err := none.SignerForRepo(ctx, "repo-a"); err != nil || s != nil {
		t.Errorf("no keys: signer = %v, err = %v; want nil, nil", s, err)
	}
}

func TestActivationAndDeletionResignRepositories(t *testing.T) {
	withSecretKey(t)

	svc, store, rpm, deb := newService(t, nil)
	store.AddRepository("deb-1", "apt", "deb")
	store.AddRepository("rpm-1", "yum", "rpm")
	store.AddRepository("rpm-own", "yum-own", "rpm")

	ctx := context.Background()

	k, err := svc.Generate(ctx, GenerateInput{Name: "k", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	own, err := svc.Generate(ctx, GenerateInput{Name: "own", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Activate(ctx, own.ID, &models.GPGSigningRepo{ID: "rpm-own", Format: "rpm"}, "admin"); err != nil {
		t.Fatal(err)
	}

	if err := svc.Activate(ctx, k.ID, nil, "admin"); err != nil {
		t.Fatal(err)
	}

	// Global activation re-signs every repository without its own key.
	if got := deb.seen(); len(got) == 0 || got[len(got)-1] != "deb-1" {
		t.Errorf("deb refresh = %v, want deb-1", got)
	}

	if got := rpm.seen(); got[len(got)-1] != "rpm-1" {
		t.Errorf("rpm refresh = %v, want rpm-1 last", got)
	}

	stale := store.StaleRepositories()
	if stale[len(stale)-1] != "rpm-1" {
		t.Errorf("rpm repositories must be marked stale before the notification, got %v", stale)
	}

	// In-use keys are refused unless forced.
	if err := svc.Delete(ctx, k.ID, false); !errors.Is(err, ErrKeyInUse) {
		t.Errorf("delete of the global key: err = %v, want ErrKeyInUse", err)
	}

	if err := svc.Delete(ctx, own.ID, false); !errors.Is(err, ErrKeyInUse) {
		t.Errorf("delete of an assigned key: err = %v, want ErrKeyInUse", err)
	}

	before := len(deb.seen())

	if err := svc.Delete(ctx, k.ID, true); err != nil {
		t.Fatalf("forced delete: %v", err)
	}

	if len(deb.seen()) <= before {
		t.Error("deleting the global key did not re-render the repositories that followed it")
	}

	if err := svc.Delete(ctx, k.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}

	if err := svc.Deactivate(ctx, own.ID, &models.GPGSigningRepo{ID: "deb-1", Format: "deb"}); !errors.Is(err, ErrNotActive) {
		t.Errorf("deactivating where it is not active: err = %v, want ErrNotActive", err)
	}

	if err := svc.Deactivate(ctx, own.ID, &models.GPGSigningRepo{ID: "rpm-own", Format: "rpm"}); err != nil {
		t.Errorf("deactivate: %v", err)
	}

	if err := svc.Delete(ctx, own.ID, false); err != nil {
		t.Errorf("unassigned key must delete without force: %v", err)
	}
}

func TestConfigKeyIsReadOnly(t *testing.T) {
	withSecretKey(t)

	cfg := configSigner(t)
	svc := newBareService(t, cfg)
	ctx := context.Background()

	if err := svc.Delete(ctx, ConfigKeyID, true); !errors.Is(err, ErrReadOnly) {
		t.Errorf("delete: err = %v", err)
	}

	if err := svc.Activate(ctx, ConfigKeyID, nil, "admin"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("activate: err = %v", err)
	}

	if err := svc.Deactivate(ctx, ConfigKeyID, nil); !errors.Is(err, ErrReadOnly) {
		t.Errorf("deactivate: err = %v", err)
	}

	pub, _, err := svc.PublicKey(ctx, ConfigKeyID)
	if err != nil || !bytes.Contains(pub, []byte("PUBLIC KEY")) {
		t.Errorf("export of the config key: %v / %.30q", err, pub)
	}

	entries, err := svc.List(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %v, %v", entries, err)
	}

	if e := entries[0]; e.ID != ConfigKeyID || !e.ReadOnly || !e.ActiveGlobal || e.Source != SourceConfig {
		t.Errorf("config entry = %+v", e)
	}

	// A database key that is globally active takes over from the config key.
	k, err := svc.Generate(ctx, GenerateInput{Name: "db", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Activate(ctx, k.ID, nil, "admin"); err != nil {
		t.Fatal(err)
	}

	entries, _ = svc.List(ctx)
	for _, e := range entries {
		if e.ID == ConfigKeyID && e.ActiveGlobal {
			t.Error("config key still reported as the global signer")
		}
	}
}

func TestSignerByRef(t *testing.T) {
	withSecretKey(t)

	cfg := configSigner(t)
	svc := newBareService(t, cfg)
	ctx := context.Background()

	k, err := svc.Generate(ctx, GenerateInput{Name: "k", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	for name, ref := range map[string]string{
		"id": k.ID, "fingerprint": k.Fingerprint, "lowercase 0x": "0x" + strings.ToLower(k.Fingerprint),
	} {
		s, _, err := svc.SignerByRef(ctx, ref)
		if err != nil || s.KeyFingerprint() != k.Fingerprint {
			t.Errorf("%s: signer = %v, err = %v", name, s, err)
		}
	}

	if s, _, err := svc.SignerByRef(ctx, ConfigKeyID); err != nil || s.KeyFingerprint() != cfg.KeyFingerprint() {
		t.Errorf("config: %v, %v", s, err)
	}

	if s, _, err := svc.SignerByRef(ctx, cfg.KeyFingerprint()); err != nil || s.KeyFingerprint() != cfg.KeyFingerprint() {
		t.Errorf("config by fingerprint: %v, %v", s, err)
	}

	for _, bad := range []string{"nope", "00000000-0000-4000-8000-00000000ffff", "DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF"} {
		if _, _, err := svc.SignerByRef(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("SignerByRef(%q) err = %v, want ErrNotFound", bad, err)
		}
	}
}

// TestAutoGenerateReplicaRace starts several "replicas" at once against one
// store: exactly one key is created, and it is the global signer.
func TestAutoGenerateReplicaRace(t *testing.T) {
	withSecretKey(t)

	store := gpgkeystest.New()
	store.AddRepository("deb-1", "apt", "deb")

	const replicas = 8

	var (
		created atomic.Int32
		wg      sync.WaitGroup
		start   = make(chan struct{})
	)

	for range replicas {
		svc := New(store, testLog(t), nil)
		svc.autoAlgorithm = gpg.AlgorithmED25519
		svc.SetRefreshers(nil, &recorder{})

		wg.Go(func() {
			<-start

			ok, err := svc.AutoGenerate(context.Background())
			if err != nil {
				t.Errorf("AutoGenerate: %v", err)
				return
			}

			if ok {
				created.Add(1)
			}
		})
	}

	close(start)
	wg.Wait()

	if created.Load() != 1 {
		t.Errorf("%d replicas created a key, want exactly 1", created.Load())
	}

	if store.Count() != 1 {
		t.Errorf("%d keys stored, want 1", store.Count())
	}

	svc := New(store, testLog(t), nil)

	signer, err := svc.SignerForRepo(context.Background(), "deb-1")
	if err != nil || signer == nil {
		t.Fatalf("auto-generated key does not sign: %v, %v", signer, err)
	}

	entries, _ := svc.List(context.Background())
	if len(entries) != 1 || !entries[0].ActiveGlobal || entries[0].Source != models.GPGKeySourceAuto {
		t.Errorf("entries = %+v", entries)
	}
}

func TestAutoGenerateSkips(t *testing.T) {
	withSecretKey(t)

	ctx := context.Background()

	// A configured key means there is nothing to generate.
	svc, store, _, _ := newService(t, configSigner(t))
	if ok, err := svc.AutoGenerate(ctx); ok || err != nil || store.Count() != 0 {
		t.Errorf("with a config key: created=%v err=%v keys=%d", ok, err, store.Count())
	}

	// An existing key (even an inactive one) is never joined by a second.
	svc, store, _, _ = newService(t, nil)
	if _, err := svc.Generate(ctx, GenerateInput{Name: "mine", Algorithm: gpg.AlgorithmED25519}); err != nil {
		t.Fatal(err)
	}

	if ok, err := svc.AutoGenerate(ctx); ok || err != nil || store.Count() != 1 {
		t.Errorf("with a stored key: created=%v err=%v keys=%d", ok, err, store.Count())
	}
}
