// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/format/storage"
)

// detailFixtures are hand-written descriptors for the formats the test seeds;
// the real values are registered by the plugin layer.
func registerDetailFixtures(t *testing.T) {
	t.Helper()

	storage.Reset()
	t.Cleanup(storage.Reset)

	v := func(path string) storage.Field { return storage.Field{Source: storage.FromVersion, Path: path} }

	storage.Register("npm", storage.Storage{Detail: storage.Detail{
		Description: v("manifest_json.description"),
		License:     v("manifest_json.license"),
		SHA1:        storage.Field{Source: storage.FromChecksums, Path: "shasum"},
		Info: []storage.InfoField{
			{Key: "integrity", From: storage.Field{Source: storage.FromChecksums, Path: "integrity"}},
			{Key: "scope", From: storage.Field{Source: storage.FromPackage, Path: "scope"}},
		},
		Deps:    v("manifest_json"),
		Decoder: "npm-manifest",
	}})
	storage.Register("maven", storage.Storage{Detail: storage.Detail{
		MD5: storage.Field{Source: storage.FromChecksums, Path: "md5"},
		Info: []storage.InfoField{
			{Key: "group_id", From: storage.Field{Source: storage.FromPackage, Path: "group_id"}},
			{Key: "classifier", From: storage.Field{Source: storage.FromSide, Path: "classifier"}},
			{Key: "snapshot", From: storage.Field{Source: storage.FromSide, Path: "is_snapshot"}},
		},
	}})
	storage.Register("go", storage.Storage{Detail: storage.Detail{
		Info:    []storage.InfoField{{Key: "module_path", From: storage.Field{Source: storage.FromColumns, Path: "name"}}},
		Deps:    storage.Field{Source: storage.FromSibling, Path: "go_mod_content"},
		Decoder: "go-mod",
	}})
}

func TestArtifactDetailReadsGenericTables(t *testing.T) {
	registerDetailFixtures(t)

	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()
	ctx := context.Background()

	npmRepo, mavenRepo, goRepo := seedRepo(t, db, "detail-npm", "npm"), seedRepo(t, db, "detail-maven", "maven"), seedRepo(t, db, "detail-go", "go")

	npm, err := pg.writePackage(ctx, &PackageWrite{
		RepoID: npmRepo, Format: "npm", Name: "@acme/widget", NameKey: "@acme/widget",
		PkgAttrs: map[string]any{"scope": "acme"},
		Version:  "1.0.0", VersionKey: "1.0.0",
		VersionMeta: map[string]any{"manifest_json": map[string]any{
			"description": "A widget", "license": map[string]any{"type": "MIT"},
			"dependencies": map[string]string{"left-pad": "^1.0.0"},
		}},
		Files: []FileWrite{{
			Path: "@acme/widget/-/widget-1.0.0.tgz", Filename: "widget-1.0.0.tgz",
			Blob:      BlobWrite{SHA256: strings.Repeat("a", 64), Key: "k-npm", Size: 11},
			Checksums: map[string]string{"shasum": "d34db33f", "integrity": "sha512-x"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	d, err := pg.GetRepoComponentDetail(ctx, npmRepo, npm.Files[0].ID)
	if err != nil || d == nil {
		t.Fatalf("npm detail %+v err %v", d, err)
	}

	if d.Format != "npm" || d.Name != "@acme/widget" || d.Version != "1.0.0" || d.Filename != "widget-1.0.0.tgz" || d.SizeBytes != 11 {
		t.Fatalf("npm columns %+v", d)
	}

	// An object license is not display text; the description and checksums are.
	if d.Description != "A widget" || d.License != "" || d.SHA1 != "d34db33f" ||
		d.Info["integrity"] != "sha512-x" || d.Info["scope"] != "acme" || d.DepsDecoder != "npm-manifest" {
		t.Fatalf("npm metadata %+v", d)
	}

	mvn, err := pg.writePackage(ctx, &PackageWrite{
		RepoID: mavenRepo, Format: "maven", Name: "org.acme:lib", NameKey: "org.acme:lib",
		PkgAttrs: map[string]any{"group_id": "org.acme", "artifact_id": "lib"},
		Version:  "1.0", VersionKey: "1.0",
		Files: []FileWrite{{
			Path: "org/acme/lib/1.0/lib-1.0-sources.jar", Filename: "org/acme/lib/1.0/lib-1.0-sources.jar", Arch: "sources",
			Blob:      BlobWrite{SHA256: strings.Repeat("b", 64), Key: "k-mvn", Size: 3},
			Checksums: map[string]string{"md5": "0123"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO maven_files (id, classifier, extension, is_snapshot) VALUES ($1, 'sources', 'jar', true)`,
		mvn.Files[0].ID); err != nil {
		t.Fatal(err)
	}

	d, err = pg.GetRepoComponentDetail(ctx, mavenRepo, mvn.Files[0].ID)
	if err != nil || d == nil {
		t.Fatalf("maven detail %+v err %v", d, err)
	}

	if d.Arch != "sources" || d.MD5 != "0123" || d.Info["group_id"] != "org.acme" || d.Info["classifier"] != "sources" || d.Info["snapshot"] != "true" {
		t.Fatalf("maven detail %+v", d)
	}

	goMod, err := pg.writePackage(ctx, &PackageWrite{
		RepoID: goRepo, Format: "go", Name: "example.com/m", NameKey: "example.com/m",
		Version: "v1.0.0", VersionKey: "v1.0.0",
		Files: []FileWrite{
			{Path: "example.com/m/@v/v1.0.0.zip", Filename: "v1.0.0.zip", Blob: BlobWrite{SHA256: strings.Repeat("c", 64), Key: "k-go", Size: 5}},
			{
				Path: "example.com/m/@v/v1.0.0.mod", Filename: "v1.0.0.mod", Role: "mod", Unlisted: true,
				Attrs: map[string]any{"go_mod_content": "module example.com/m\n\nrequire example.com/dep v1.2.3\n"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	d, err = pg.GetRepoComponentDetail(ctx, goRepo, goMod.Files[0].ID)
	if err != nil || d == nil || d.Info["module_path"] != "example.com/m" || d.DepsDecoder != "go-mod" || len(d.Deps) == 0 {
		t.Fatalf("go detail %+v err %v", d, err)
	}

	// The unlisted go.mod file is no listing row: its id resolves nothing.
	if d, err := pg.GetRepoComponentDetail(ctx, goRepo, goMod.Files[1].ID); err != nil || d != nil {
		t.Fatalf("unlisted file resolved: %+v %v", d, err)
	}

	// The id is scoped to its repository.
	if d, err := pg.GetRepoComponentDetail(ctx, mavenRepo, npm.Files[0].ID); err != nil || d != nil {
		t.Fatalf("foreign repository resolved: %+v %v", d, err)
	}
}
