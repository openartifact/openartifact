// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/pacman"
)

func pacmanSigner(t *testing.T) (*gpg.LocalSigner, *openpgp.Entity) {
	t.Helper()

	entity, err := gpg.GenerateEntity("t", "", "t@example.org", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := gpg.NewLocalSignerFromEntity(entity)
	if err != nil {
		t.Fatal(err)
	}

	return signer, entity
}

// storePacmanPackage stores body as a blob and a pacman package for it.
func storePacmanPackage(
	t *testing.T,
	pg *database.PostgresDB,
	store *formattest.BlobStore,
	repoID, name, version, body string,
) *models.PacmanPackage {
	t.Helper()

	sum := sha256.Sum256([]byte(body))
	sha := hex.EncodeToString(sum[:])

	key, err := store.Put(context.Background(), strings.NewReader(body), map[string]string{"sha256": sha})
	if err != nil {
		t.Fatal(err)
	}

	md := md5.Sum([]byte(body))
	pkg := &models.PacmanPackage{
		RepositoryID: repoID, Name: name, Base: name, Version: version, Arch: "x86_64", RepoArch: "x86_64",
		Filename: pacman.PackageFileName(name, version, "x86_64", "zst"), MD5: hex.EncodeToString(md[:]),
		SHA256: sha, BlobKey: key, SizeBytes: int64(len(body)), BuildDate: 1700000000,
	}

	if err := pg.CreatePacmanPackage(context.Background(), pkg); err != nil {
		t.Fatalf("CreatePacmanPackage: %v", err)
	}

	return pkg
}

func publishedPacman(t *testing.T, pg *database.PostgresDB, store *formattest.BlobStore, repoID, path string) []byte {
	t.Helper()

	row, err := pg.GetRepoMetadataByPath(context.Background(), repoID, path)
	if err != nil || row == nil {
		t.Fatalf("metadata %s: %v, %v", path, row, err)
	}

	rc, err := store.Get(context.Background(), row.BlobSHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()

	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

//nolint:gocyclo,cyclop // one scenario: no key, first key, rotation, key removed
func TestPacmanRegenerateSignsAndRotates(t *testing.T) {
	pg := newTrashPurgeTestDB(t)
	store := formattest.NewBlobStore()
	ctx := context.Background()
	org := trashTestOrgID

	repo := &models.Repository{Name: "arch-sign", Format: "pacman", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	uploaded := storePacmanPackage(t, pg, store, repo.ID, "up", "1-1", "uploader signed")
	if err := pg.SetPacmanSignature(ctx, repo.ID, uploaded.ID, "dXA=", models.PacmanSigUploader, ""); err != nil {
		t.Fatal(err)
	}

	unsigned := storePacmanPackage(t, pg, store, repo.ID, "plain", "1-1", "server signed")

	// Without a key: no .sig, and the uploader signature is untouched.
	w := NewPacmanMetadataWorker(pg, store, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.regenerate(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	if row, _ := pg.GetRepoMetadataByPath(ctx, repo.ID, "os/x86_64/db.tar.gz.sig"); row != nil {
		t.Error("an unsigned repository publishes a database signature")
	}

	if got, _ := pg.GetPacmanPackageByFilename(ctx, repo.ID, unsigned.Filename); got.Signature != "" {
		t.Errorf("signature without a key: %q", got.Signature)
	}

	if len(publishedPacman(t, pg, store, repo.ID, "os/x86_64/files.tar.gz")) == 0 {
		t.Error("empty files database")
	}

	// With a key: the databases and the unsigned package are signed.
	first, firstEntity := pacmanSigner(t)

	w = NewPacmanMetadataWorker(pg, store, fixedResolver{first}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.regenerate(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	db := publishedPacman(t, pg, store, repo.ID, "os/x86_64/db.tar.gz")
	sig := publishedPacman(t, pg, store, repo.ID, "os/x86_64/db.tar.gz.sig")

	if _, err := openpgp.CheckDetachedSignature(
		openpgp.EntityList{firstEntity},
		bytes.NewReader(db),
		bytes.NewReader(sig),
		nil,
	); err != nil {
		t.Fatalf("database signature: %v", err)
	}

	got, _ := pg.GetPacmanPackageByFilename(ctx, repo.ID, unsigned.Filename)
	if got.SignatureSource != models.PacmanSigServer || got.SignatureKey != first.KeyFingerprint() || got.Signature == "" {
		t.Fatalf("server signature = %+v", got)
	}

	if up, _ := pg.GetPacmanPackageByFilename(
		ctx,
		repo.ID,
		uploaded.Filename,
	); up.Signature != "dXA=" ||
		up.SignatureSource != models.PacmanSigUploader {
		t.Errorf("uploader signature replaced: %+v", up)
	}

	// Another key re-signs the server's signatures only.
	second, secondEntity := pacmanSigner(t)

	w = NewPacmanMetadataWorker(pg, store, fixedResolver{second}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.regenerate(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	got, _ = pg.GetPacmanPackageByFilename(ctx, repo.ID, unsigned.Filename)
	if got.SignatureKey != second.KeyFingerprint() {
		t.Errorf("signature key after rotation = %q", got.SignatureKey)
	}

	db = publishedPacman(t, pg, store, repo.ID, "os/x86_64/db.tar.gz")
	sig = publishedPacman(t, pg, store, repo.ID, "os/x86_64/db.tar.gz.sig")

	if _, err := openpgp.CheckDetachedSignature(
		openpgp.EntityList{secondEntity},
		bytes.NewReader(db),
		bytes.NewReader(sig),
		nil,
	); err != nil {
		t.Fatalf("database signature after rotation: %v", err)
	}

	// Removing the key drops the server signatures and the database signature.
	w = NewPacmanMetadataWorker(pg, store, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.regenerate(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	got, _ = pg.GetPacmanPackageByFilename(ctx, repo.ID, unsigned.Filename)
	if got.Signature != "" || got.SignatureSource != "" {
		t.Errorf("server signature kept without a key: %+v", got)
	}

	if row, _ := pg.GetRepoMetadataByPath(ctx, repo.ID, "os/x86_64/db.tar.gz.sig"); row != nil {
		t.Error("the database signature outlived the key")
	}

	requireBlobsPresent(t, store, pg, repo.ID)
}

func TestPacmanRegenerateEmptiesAnArchitectureWithoutPackages(t *testing.T) {
	pg := newTrashPurgeTestDB(t)
	store := formattest.NewBlobStore()
	ctx := context.Background()
	org := trashTestOrgID

	repo := &models.Repository{Name: "arch-empty", Format: "pacman", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	pkg := storePacmanPackage(t, pg, store, repo.ID, "tool", "1-1", "body")

	w := NewPacmanMetadataWorker(pg, store, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.regenerate(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.GetRepoMetadataByPath(ctx, repo.ID, "os/x86_64/db.tar.gz"); err != nil {
		t.Fatal(err)
	}

	if err := pg.DeletePacmanPackage(ctx, repo.ID, pkg.ID); err != nil {
		t.Fatal(err)
	}

	if err := w.regenerate(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	if row, _ := pg.GetRepoMetadataByPath(ctx, repo.ID, "os/x86_64/db.tar.gz"); row != nil {
		t.Error("a database outlived its last package")
	}
}
