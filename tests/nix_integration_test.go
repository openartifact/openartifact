// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const nixTestAlphabet = "0123456789abcdfghijklmnpqrsvwxyz"

func nixHash(n int) string {
	return strings.Repeat(string(nixTestAlphabet[n%len(nixTestAlphabet)]), 31) + "a"
}

func nixPathFixture(repoID, name string, hash, blob int) *models.NixPath {
	sha := fmt.Sprintf("%064x", blob)

	return &models.NixPath{
		RepositoryID: repoID, StoreHash: nixHash(hash), Name: name, NarHash: "sha256:0abc", NarSize: 4096,
		NarFile: sha[:52] + ".nar.xz", Compression: "xz", References: []string{"abc-glibc-2.39"}, Deriver: "x.drv",
		Signatures: []string{"cache.example-1:AAAA"}, SHA256: sha, BlobKey: sha, SizeBytes: 100,
	}
}

func nixRefs(t *testing.T, pg *database.PostgresDB, sha string) int {
	t.Helper()

	n, err := pg.BlobReferenceCount(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}

	return n
}

// A staged NAR is idempotent, committing the path consumes the staging row
// and keeps the blob referenced, and a live store hash is unique.
//
//nolint:gocyclo,cyclop // one scenario: stage, commit, lookups, uniqueness, delete, recreate
func TestNixPendingCommitAndUnique(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "cache", "nix")

	p := nixPathFixture(repoID, "hello-2.12", 1, 1)
	pend := &models.NixPendingNar{RepositoryID: repoID, SHA256: p.SHA256, NarFile: p.NarFile, BlobKey: p.BlobKey, SizeBytes: p.SizeBytes}

	for range 2 {
		if err := pg.PutNixPendingNar(ctx, pend); err != nil {
			t.Fatalf("stage: %v", err)
		}
	}

	got, err := pg.GetNixPendingNar(ctx, repoID, p.SHA256)
	if err != nil || got == nil || got.NarFile != p.NarFile || got.SizeBytes != 100 || got.BlobKey != p.BlobKey {
		t.Fatalf("pending = %+v, %v", got, err)
	}

	if missing, err := pg.GetNixPendingNar(ctx, repoID, fmt.Sprintf("%064x", 99)); err != nil || missing != nil {
		t.Fatalf("absent pending = %+v, %v; want nil, nil", missing, err)
	}

	if n := nixRefs(t, pg, p.SHA256); n != 1 {
		t.Fatalf("staged NAR references = %d, want 1", n)
	}

	if err := pg.CreateNixPath(ctx, p); err != nil || p.ID == "" {
		t.Fatalf("create = %v (id %q)", err, p.ID)
	}

	if after, err := pg.GetNixPendingNar(ctx, repoID, p.SHA256); err != nil || after != nil {
		t.Fatalf("pending after commit = %+v, %v; want consumed", after, err)
	}

	if n := nixRefs(t, pg, p.SHA256); n != 1 {
		t.Fatalf("committed NAR references = %d, want 1", n)
	}

	dup := nixPathFixture(repoID, "hello-2.12", 1, 2)
	if err := pg.CreateNixPath(ctx, dup); !errors.Is(err, database.ErrDuplicate) {
		t.Fatalf("duplicate live hash = %v, want ErrDuplicate", err)
	}

	byHash, err := pg.GetNixPathByHash(ctx, repoID, p.StoreHash)
	if err != nil || byHash == nil || byHash.Name != "hello-2.12" || byHash.SHA256 != p.SHA256 || byHash.SizeBytes != 100 ||
		byHash.NarHash != p.NarHash || byHash.References[0] != "abc-glibc-2.39" || byHash.Signatures[0] != p.Signatures[0] {
		t.Fatalf("by hash = %+v, %v", byHash, err)
	}

	if none, err := pg.GetNixPathByHash(ctx, repoID, nixHash(7)); err != nil || none != nil {
		t.Fatalf("absent hash = %+v, %v; want nil, nil", none, err)
	}

	if err := pg.IncrementNixDownloadCount(ctx, p.ID, repoID); err != nil {
		t.Fatal(err)
	}

	list, err := pg.ListNixPaths(ctx, repoID)
	if err != nil || len(list) != 1 || list[0].DownloadCount != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if err := pg.DeleteNixPath(ctx, repoID, p.ID); err != nil {
		t.Fatal(err)
	}

	if n := nixRefs(t, pg, p.SHA256); n != 0 {
		t.Fatalf("references after delete = %d, want 0", n)
	}

	if err := pg.CreateNixPath(ctx, nixPathFixture(repoID, "hello-2.12", 1, 3)); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

// Two paths may share one NAR file; its blob stays referenced until both are gone.
func TestNixSharedNarKeepsBlob(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "shared", "nix")

	a := nixPathFixture(repoID, "a-1.0", 1, 5)
	b := nixPathFixture(repoID, "b-1.0", 2, 5)

	if err := pg.CreateNixPath(ctx, a); err != nil {
		t.Fatal(err)
	}

	if err := pg.CreateNixPath(ctx, b); err != nil {
		t.Fatal(err)
	}

	if n := nixRefs(t, pg, a.SHA256); n != 2 {
		t.Fatalf("shared NAR references = %d, want 2", n)
	}

	byFile, err := pg.GetNixPathByNarFile(ctx, repoID, a.NarFile)
	if err != nil || byFile == nil || byFile.SHA256 != a.SHA256 {
		t.Fatalf("by nar file = %+v, %v", byFile, err)
	}

	if none, err := pg.GetNixPathByNarFile(ctx, repoID, "nope.nar.xz"); err != nil || none != nil {
		t.Fatalf("absent nar file = %+v, %v; want nil, nil", none, err)
	}

	if err := pg.DeleteNixPath(ctx, repoID, a.ID); err != nil {
		t.Fatal(err)
	}

	if n := nixRefs(t, pg, a.SHA256); n != 1 {
		t.Fatalf("references after one delete = %d, want 1", n)
	}

	still, err := pg.GetNixPathByNarFile(ctx, repoID, a.NarFile)
	if err != nil || still == nil || still.StoreHash != b.StoreHash {
		t.Fatalf("NAR file still served by the other path: %+v, %v", still, err)
	}

	if err := pg.DeleteNixPath(ctx, repoID, b.ID); err != nil {
		t.Fatal(err)
	}

	if n := nixRefs(t, pg, a.SHA256); n != 0 {
		t.Fatalf("references after both deleted = %d, want 0", n)
	}
}

// The sweep removes only staging rows older than the cutoff and returns their
// file hashes.
func TestNixDeleteStalePendingNars(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "stale", "nix")

	for i := 1; i <= 3; i++ {
		sha := fmt.Sprintf("%064x", 100+i)
		if err := pg.PutNixPendingNar(ctx, &models.NixPendingNar{
			RepositoryID: repoID, SHA256: sha, NarFile: sha[:52] + ".nar.xz", BlobKey: sha, SizeBytes: 10,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing is older than an hour ago.
	if got, err := pg.DeleteStaleNixPendingNars(ctx, time.Now().Add(-time.Hour), 10); err != nil || len(got) != 0 {
		t.Fatalf("early cutoff = %v, %v; want none", got, err)
	}

	cutoff := time.Now().Add(time.Minute)

	first, err := pg.DeleteStaleNixPendingNars(ctx, cutoff, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first batch = %v, %v; want 2", first, err)
	}

	second, err := pg.DeleteStaleNixPendingNars(ctx, cutoff, 2)
	if err != nil || len(second) != 1 {
		t.Fatalf("second batch = %v, %v; want 1", second, err)
	}

	seen := map[string]bool{}
	for _, s := range append(first, second...) {
		seen[s] = true
	}

	if len(seen) != 3 {
		t.Fatalf("swept hashes = %v, want 3 distinct", seen)
	}

	for s := range seen {
		if n := nixRefs(t, pg, s); n != 0 {
			t.Errorf("swept NAR %s still has %d references", s, n)
		}
	}
}

// The signing key is created once, replaced on demand.
func TestNixSigningKey(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "keys", "nix")

	if k, err := pg.GetNixSigningKey(ctx, repoID); err != nil || k != nil {
		t.Fatalf("absent key = %+v, %v; want nil, nil", k, err)
	}

	k := &models.NixSigningKey{
		RepositoryID: repoID, KeyName: "cache-1", PublicKey: "cache-1:AAAA", PrivateKeyEnc: []byte("sealed"), CreatedBy: "admin",
	}

	if created, err := pg.CreateNixSigningKeyIfAbsent(ctx, k); err != nil || !created {
		t.Fatalf("create = %v, %v", created, err)
	}

	if created, err := pg.CreateNixSigningKeyIfAbsent(ctx, &models.NixSigningKey{
		RepositoryID: repoID, KeyName: "other", PublicKey: "other:BBBB", PrivateKeyEnc: []byte("x"),
	}); err != nil || created {
		t.Fatalf("second create = %v, %v; want not created", created, err)
	}

	k.KeyName, k.PublicKey = "cache-2", "cache-2:CCCC"
	if err := pg.ReplaceNixSigningKey(ctx, k); err != nil {
		t.Fatal(err)
	}

	got, err := pg.GetNixSigningKey(ctx, repoID)
	if err != nil || got == nil || got.KeyName != "cache-2" || got.PublicKey != "cache-2:CCCC" || string(got.PrivateKeyEnc) != "sealed" ||
		got.CreatedBy != "admin" {
		t.Fatalf("key = %+v, %v", got, err)
	}
}
