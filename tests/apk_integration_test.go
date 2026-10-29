// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func apkTestPackage(repoID, branch, name, version string) *models.ApkPackage {
	sum := sha256.Sum256([]byte(branch + name + version))

	return &models.ApkPackage{
		RepositoryID: repoID, Branch: branch, Repo: "main", Arch: "x86_64", PkgArch: "noarch",
		Name: name, Version: version, Checksum: "Q1abc=", DataHash: "00ff",
		Description: "d", License: "MIT", BuildTime: 1700000000, InstalledSize: 4096,
		Depends: []string{"so:libc.musl-x86_64.so.1"}, ProviderPriority: 10,
		BlobKey: "apk/" + name + version, BlobSHA256: hex.EncodeToString(sum[:]), SizeBytes: 123,
	}
}

func requireApkRow(t *testing.T, got *models.ApkPackage) {
	t.Helper()

	if got.PkgArch != "noarch" || got.BlobKey != "apk/zlib1.3.1-r0" || got.SizeBytes != 123 || got.BlobSHA256 == "" ||
		!slices.Equal(got.Depends, []string{"so:libc.musl-x86_64.so.1"}) || got.Provides == nil || got.ProviderPriority != 10 {
		t.Errorf("unexpected package: %+v", got)
	}
}

// requireApkListOrder checks that ListApkPackages orders by branch, then name and version.
func requireApkListOrder(t *testing.T, pg *database.PostgresDB, repo string) {
	t.Helper()

	list, err := pg.ListApkPackages(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	var order []string
	for _, p := range list {
		order = append(order, p.Branch+"/"+p.Name+"-"+p.Version)
	}

	if want := []string{"edge/musl-1.2.5-r1", "v3.20/musl-1.2.5-r0", "v3.20/zlib-1.3.1-r0"}; !slices.Equal(order, want) {
		t.Errorf("list order = %v, want %v", order, want)
	}
}

func TestApkPackageLifecycle(t *testing.T) {
	ctx := context.Background()
	pg := freshPostgres(t, 5)
	repo := newRepo(t, pg, "apk-repo", "apk")

	for _, p := range []*models.ApkPackage{
		apkTestPackage(repo, "v3.20", "zlib", "1.3.1-r0"),
		apkTestPackage(repo, "edge", "musl", "1.2.5-r1"),
		apkTestPackage(repo, "v3.20", "musl", "1.2.5-r0"),
	} {
		if err := pg.CreateApkPackage(ctx, p); err != nil {
			t.Fatalf("CreateApkPackage: %v", err)
		}

		if p.ID == "" || p.CreatedAt.IsZero() {
			t.Fatalf("create did not stamp the package: %+v", p)
		}
	}

	dup := apkTestPackage(repo, "v3.20", "zlib", "1.3.1-r0")
	if err := pg.CreateApkPackage(ctx, dup); !errors.Is(err, database.ErrDuplicate) {
		t.Fatalf("duplicate create = %v, want ErrDuplicate", err)
	}

	got, err := pg.GetApkPackageFile(ctx, repo, "v3.20", "main", "x86_64", "zlib", "1.3.1-r0")
	if err != nil || got == nil {
		t.Fatalf("GetApkPackageFile = %v, %v", got, err)
	}

	requireApkRow(t, got)

	if miss, err := pg.GetApkPackageFile(ctx, repo, "v3.19", "main", "x86_64", "zlib", "1.3.1-r0"); miss != nil || err != nil {
		t.Errorf("missing package = %v, %v, want nil, nil", miss, err)
	}

	requireApkListOrder(t, pg, repo)

	if err := pg.IncrementApkDownloadCount(ctx, got.ID, repo); err != nil {
		t.Fatal(err)
	}

	if again, _ := pg.GetApkPackageFile(ctx, repo, "v3.20", "main", "x86_64", "zlib", "1.3.1-r0"); again.DownloadCount != 1 {
		t.Errorf("download count = %d, want 1", again.DownloadCount)
	}

	if err := pg.DeleteApkPackage(ctx, got.ID, repo); err != nil {
		t.Fatal(err)
	}

	if gone, err := pg.GetApkPackageFile(ctx, repo, "v3.20", "main", "x86_64", "zlib", "1.3.1-r0"); gone != nil || err != nil {
		t.Errorf("deleted package = %v, %v, want nil, nil", gone, err)
	}

	// The coordinates are free again once the row is gone.
	if err := pg.CreateApkPackage(ctx, apkTestPackage(repo, "v3.20", "zlib", "1.3.1-r0")); err != nil {
		t.Errorf("re-create after delete: %v", err)
	}
}

func TestApkSigningKey(t *testing.T) {
	ctx := context.Background()
	pg := freshPostgres(t, 5)
	repo := newRepo(t, pg, "apk-key", "apk")

	if k, err := pg.GetApkSigningKey(ctx, repo); k != nil || err != nil {
		t.Fatalf("no key = %v, %v, want nil, nil", k, err)
	}

	first := &models.ApkSigningKey{
		RepositoryID: repo, KeyName: "openartifact-apk-key.rsa.pub", Fingerprint: "f1",
		PublicKeyPEM: "pub1", PrivateKeyEnc: []byte("sealed1"), CreatedBy: "admin",
	}

	created, err := pg.CreateApkSigningKeyIfAbsent(ctx, first)
	if err != nil || !created {
		t.Fatalf("first create = %v, %v", created, err)
	}

	second := *first
	second.Fingerprint, second.PrivateKeyEnc = "f2", []byte("sealed2")

	if created, err := pg.CreateApkSigningKeyIfAbsent(ctx, &second); err != nil || created {
		t.Fatalf("second create = %v, %v, want false", created, err)
	}

	got, err := pg.GetApkSigningKey(ctx, repo)
	if err != nil || got == nil || got.Fingerprint != "f1" || !bytes.Equal(got.PrivateKeyEnc, []byte("sealed1")) ||
		got.PublicKeyPEM != "pub1" {
		t.Fatalf("key after create-if-absent = %+v, %v", got, err)
	}

	if err := pg.ReplaceApkSigningKey(ctx, &second); err != nil {
		t.Fatal(err)
	}

	got, err = pg.GetApkSigningKey(ctx, repo)
	if err != nil || got == nil || got.Fingerprint != "f2" || !bytes.Equal(got.PrivateKeyEnc, []byte("sealed2")) {
		t.Fatalf("key after replace = %+v, %v", got, err)
	}
}
