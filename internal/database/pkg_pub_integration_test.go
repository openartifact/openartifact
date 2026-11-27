// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// A pub release round trips through the typed store over the generic package
// tables: one package, one version and one file per release, the pubspec kept
// as the version metadata, and the quota reservation consumed by the write.
func TestPubStoreRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	repo := newQuotaSeeder(t, pg, "rt-pub", "pub").repo

	release := func(name, version string) *models.PubPackage {
		return &models.PubPackage{
			RepositoryID: repo, Name: name, Version: version, Filename: name + "-" + version + ".tar.gz",
			BlobKey: "pub/" + name + "/" + version, SHA256: digest("pub" + name + version), SizeBytes: 13,
			Pubspec: json.RawMessage(`{"name":"` + name + `","version":"` + version + `","description":"d"}`),
		}
	}

	first := release("demo", "1.0.0")
	reservedWrite(t, pg, repo, 13, func() {
		if err := pg.CreatePubPackage(ctx, first); err != nil {
			t.Fatal(err)
		}
	})

	if first.ID == "" || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatalf("create did not fill id and times: %+v", first)
	}

	if err := pg.CreatePubPackage(ctx, release("demo", "2.0.0-beta.1")); err != nil {
		t.Fatal(err)
	}

	if err := pg.CreatePubPackage(ctx, release("demo", "1.0.0")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate publish = %v, want ErrDuplicate", err)
	}

	requireGeneric(t, pg, repo, genericRows{1, 2, 2})

	got, err := pg.GetPubPackage(ctx, repo, "demo", "1.0.0")
	if err != nil || got == nil {
		t.Fatalf("GetPubPackage = %v, %v", got, err)
	}

	requireEq(t, "ID", got.ID, first.ID)
	requireEq(t, "Filename", got.Filename, "demo-1.0.0.tar.gz")
	requireEq(t, "BlobKey", got.BlobKey, "pub/demo/1.0.0")
	requireEq(t, "SHA256", got.SHA256, digest("pubdemo1.0.0"))
	requireEq(t, "SizeBytes", got.SizeBytes, int64(13))

	var spec struct{ Description string }
	if err := json.Unmarshal(got.Pubspec, &spec); err != nil || spec.Description != "d" {
		t.Errorf("pubspec = %s (%v), want the stored pubspec", got.Pubspec, err)
	}

	if missing, err := pg.GetPubPackage(ctx, repo, "demo", "9.9.9"); err != nil || missing != nil {
		t.Errorf("unknown version = %v, %v, want nil", missing, err)
	}

	list, err := pg.ListPubVersions(ctx, repo, "demo")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListPubVersions = %v, %v, want both versions", list, err)
	}

	if other, err := pg.ListPubVersions(ctx, repo, "other"); err != nil || len(other) != 0 {
		t.Errorf("ListPubVersions(other) = %v, %v, want none", other, err)
	}

	f := readFile(t, pg, first.ID)
	requireEq(t, "path", f.path, "demo/1.0.0/demo-1.0.0.tar.gz")
	requireEq(t, "role", f.role, "primary")
	requireEq(t, "format", f.format, "pub")
	requireEq(t, "package name_key", f.pkgKey, "demo")
	requireEq(t, "version_key", f.versionKey, "1.0.0")
	requireEq(t, "blob sha256", f.blobSHA, digest("pubdemo1.0.0"))
}
