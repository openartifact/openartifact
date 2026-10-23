// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func gpgTestKey(name string) *models.GPGKey {
	return &models.GPGKey{
		Name: name, Fingerprint: fmt.Sprintf("%040s", name), KeyID: "0123456789ABCDEF", Algorithm: "ed25519",
		UserID: "Test <t@example.com>", PublicKey: "pub", PrivateKeyEnc: "enc:v1:xxxx", Source: models.GPGKeySourceGenerated, CreatedBy: "test",
	}
}

func newGPGRepo(t *testing.T, pg *database.PostgresDB, name, format string) string {
	t.Helper()

	org := "00000000-0000-0000-0000-000000000001"
	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &org}

	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository %s: %v", name, err)
	}

	return repo.ID
}

// Replicas starting together must create exactly one key: the check and the
// insert run under a cluster-wide advisory lock.
func TestCreateGPGKeyIfNoneIsExclusiveAcrossReplicas(t *testing.T) {
	pg := freshPostgres(t, 12)
	ctx := context.Background()

	const replicas = 10

	var (
		created atomic.Int32
		wg      sync.WaitGroup
		start   = make(chan struct{})
	)

	for i := range replicas {
		wg.Go(func() {
			<-start

			ok, err := pg.CreateGPGKeyIfNone(ctx, gpgTestKey(fmt.Sprintf("auto-%d", i)))
			if err != nil {
				t.Errorf("replica %d: %v", i, err)
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
		t.Fatalf("%d replicas created a key, want 1", created.Load())
	}

	keys, err := pg.ListGPGKeys(ctx)
	if err != nil || len(keys) != 1 || !keys[0].IsGlobalActive {
		t.Fatalf("keys = %+v, err = %v; want one globally active key", keys, err)
	}

	if keys[0].PrivateKeyEnc != "" {
		t.Error("ListGPGKeys must not select the private key column")
	}

	// And once any key exists, nothing is created.
	if ok, err := pg.CreateGPGKeyIfNone(ctx, gpgTestKey("late")); err != nil || ok {
		t.Errorf("late call = %v, %v; want false, nil", ok, err)
	}
}

func TestGPGKeyActivationAndResolution(t *testing.T) {
	pg := freshPostgres(t, 6)
	ctx := context.Background()

	a, b := gpgTestKey("a"), gpgTestKey("b")
	for _, k := range []*models.GPGKey{a, b} {
		if err := pg.CreateGPGKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}

	if err := pg.CreateGPGKey(ctx, gpgTestKey("a")); !errors.Is(err, database.ErrGPGKeyExists) {
		t.Errorf("duplicate name: %v, want ErrGPGKeyExists", err)
	}

	debRepo := newGPGRepo(t, pg, "apt-signed", "deb")
	rpmRepo := newGPGRepo(t, pg, "yum-signed", "rpm")
	newGPGRepo(t, pg, "raw-plain", "raw")

	if k, err := pg.ResolveGPGKeyForRepository(ctx, debRepo); err != nil || k != nil {
		t.Fatalf("nothing active: %v, %v; want nil, nil", k, err)
	}

	// Concurrent global activations leave exactly one active key.
	var wg sync.WaitGroup

	for _, k := range []*models.GPGKey{a, b, a, b} {
		wg.Go(func() {
			if err := pg.SetGlobalGPGKey(ctx, k.ID); err != nil {
				t.Errorf("SetGlobalGPGKey: %v", err)
			}
		})
	}

	wg.Wait()

	var active int

	if err := pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM gpg_keys WHERE is_global_active`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("globally active keys = %d, err = %v; want 1", active, err)
	}

	resolveGPGKeys(t, pg, a, b, debRepo, rpmRepo)
}

func resolveGPGKeys(t *testing.T, pg *database.PostgresDB, a, b *models.GPGKey, debRepo, rpmRepo string) {
	t.Helper()

	ctx := context.Background()

	if err := pg.SetGlobalGPGKey(ctx, a.ID); err != nil {
		t.Fatal(err)
	}

	if k, _ := pg.ResolveGPGKeyForRepository(ctx, debRepo); k == nil || k.ID != a.ID || k.PrivateKeyEnc == "" {
		t.Fatalf("resolver must return the global key with its sealed private key, got %+v", k)
	}

	if err := pg.SetRepositoryGPGKey(ctx, rpmRepo, b.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	if k, _ := pg.ResolveGPGKeyForRepository(ctx, rpmRepo); k == nil || k.ID != b.ID {
		t.Errorf("repository key must win over the global one, got %+v", k)
	}

	without, err := pg.ListSigningRepositoriesWithoutKey(ctx)
	if err != nil || len(without) != 1 || without[0].ID != debRepo || without[0].Format != "deb" {
		t.Errorf("repositories following the global key = %+v, %v; want only the deb repo", without, err)
	}

	assigned, _ := pg.ListRepositoriesForGPGKey(ctx, b.ID)
	if len(assigned) != 1 || assigned[0].ID != rpmRepo {
		t.Errorf("assigned = %+v", assigned)
	}

	// Deleting the key frees its repositories (rows cascade) and they fall back to the global key.
	if err := pg.DeleteGPGKey(ctx, b.ID); err != nil {
		t.Fatal(err)
	}

	if k, _ := pg.ResolveGPGKeyForRepository(ctx, rpmRepo); k == nil || k.ID != a.ID {
		t.Errorf("after deleting the repository key, got %+v; want the global key", k)
	}

	if err := pg.DeleteGPGKey(ctx, b.ID); !errors.Is(err, database.ErrGPGKeyNotFound) {
		t.Errorf("second delete: %v, want ErrGPGKeyNotFound", err)
	}
}

func TestSealReleaseBundleStoresSignatureAndPinsItems(t *testing.T) {
	pg := freshPostgres(t, 4)
	ctx := context.Background()
	repoID := newGPGRepo(t, pg, "bundle-src", "raw")

	b := &models.ReleaseBundle{Name: "rel", Version: "1", State: models.BundleStateDraft, CreatedBy: "admin"}
	if err := pg.CreateReleaseBundle(ctx, b); err != nil {
		t.Fatal(err)
	}

	add := func(sha string) {
		t.Helper()

		item := &models.ReleaseBundleItem{BundleID: b.ID, SourceRepoID: repoID, SHA256: sha, ArtifactPath: sha[:4], Format: "raw", SizeBytes: 1, AddedAt: time.Now()}
		if err := pg.AddBundleItems(ctx, b.ID, []*models.ReleaseBundleItem{item}); err != nil {
			t.Fatal(err)
		}
	}

	add("a" + fmt.Sprintf("%063d", 1))
	add("b" + fmt.Sprintf("%063d", 2))

	ids := func() []string {
		items, err := pg.ListBundleItems(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}

		out := make([]string, 0, len(items))
		for _, i := range items {
			out = append(out, i.ID)
		}

		return out
	}

	// Signed for a stale item set (one more item arrived meanwhile): refused, still a draft.
	stale := &models.BundleSignature{KeyFingerprint: "FP", Signature: "sig", ItemIDs: ids()[:1]}
	if ok, err := pg.SealReleaseBundle(ctx, b.ID, "admin", time.Now(), stale); err != nil || ok {
		t.Fatalf("seal with a stale item set = %v, %v; want false, nil", ok, err)
	}

	if got, _ := pg.GetReleaseBundle(ctx, "rel", "1"); got.State != models.BundleStateDraft {
		t.Fatalf("state = %s, want draft", got.State)
	}

	// The set the signature covers, compared in byte order whatever the id order.
	sorted := ids()
	slices.Sort(sorted)

	good := &models.BundleSignature{KeyFingerprint: "ABCDEF", Signature: "-----BEGIN PGP SIGNATURE-----", ItemIDs: sorted}
	if ok, err := pg.SealReleaseBundle(ctx, b.ID, "admin", time.Now(), good); err != nil || !ok {
		t.Fatalf("seal = %v, %v", ok, err)
	}

	got, err := pg.GetReleaseBundle(ctx, "rel", "1")
	if err != nil || got.State != models.BundleStateSealed || got.GPGKeyID == nil || *got.GPGKeyID != "ABCDEF" ||
		got.GPGSignature == nil || *got.GPGSignature != good.Signature {
		t.Fatalf("sealed bundle = %+v, %v", got, err)
	}
}
