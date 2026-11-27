// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestSwiftStoreRoundTrip(t *testing.T) { //nolint:gocyclo,cyclop // one round trip checks every typed read of the store
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	repo := newQuotaSeeder(t, pg, "rt-swift", "swift").repo

	release := func(scope, name, version string, urls ...string) *models.SwiftRelease {
		return &models.SwiftRelease{
			RepositoryID: repo, Scope: scope, Name: name, Version: version,
			Filename: name + "-" + version + ".zip",
			SHA256:   digest("swift" + scope + name + version), BlobKey: "swift/" + scope + name + version, SizeBytes: 21,
			Metadata:       []byte(`{"description":"d"}`),
			RepositoryURLs: urls,
			Manifests: []models.SwiftManifest{
				{Filename: "Package.swift", ToolsVersion: "5.9", Content: "// swift-tools-version:5.9\n"},
				{Filename: "Package@swift-4.2.swift", SwiftVersion: "4.2", ToolsVersion: "4.2", Content: "// swift-tools-version:4.2\n"},
			},
			SignatureFormat: "cms-1.0.0", Signature: "c2ln", MetadataSignature: "bXNpZw==",
		}
	}

	r1 := release("mona", "LinkedList", "1.0.0", "github.com/mona/linkedlist")
	reservedWrite(t, pg, repo, 21, func() {
		if err := pg.CreateSwiftRelease(ctx, r1); err != nil {
			t.Fatal(err)
		}
	})

	r2 := release("mona", "LinkedList", "1.1.0", "github.com/mona/linkedlist")
	if err := pg.CreateSwiftRelease(ctx, r2); err != nil {
		t.Fatal(err)
	}

	r3 := release("other", "Tool", "0.1.0")
	r3.Metadata, r3.Manifests, r3.SignatureFormat, r3.Signature, r3.MetadataSignature = nil, nil, "", "", ""

	if err := pg.CreateSwiftRelease(ctx, r3); err != nil {
		t.Fatal(err)
	}

	// The same release under another spelling of the name is a duplicate.
	if err := pg.CreateSwiftRelease(ctx, release("MONA", "linkedlist", "1.0.0")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate publish = %v, want ErrDuplicate", err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 3, 3})

	got, err := pg.GetSwiftRelease(ctx, repo, "MONA", "linkedlist", "1.0.0")
	if err != nil || got == nil {
		t.Fatalf("GetSwiftRelease = %v, %v", got, err)
	}

	requireEq(t, "ID", got.ID, r1.ID)
	requireEq(t, "Scope", got.Scope, "mona")
	requireEq(t, "Name", got.Name, "LinkedList")
	requireEq(t, "Filename", got.Filename, "LinkedList-1.0.0.zip")
	requireEq(t, "BlobKey", got.BlobKey, "swift/monaLinkedList1.0.0")
	requireEq(t, "SizeBytes", got.SizeBytes, int64(21))
	requireEq(t, "SHA256", got.SHA256, digest("swiftmonaLinkedList1.0.0"))
	requireEq(t, "Metadata", string(got.Metadata), `{"description": "d"}`)
	requireEq(t, "RepositoryURLs", got.RepositoryURLs[0], "github.com/mona/linkedlist")
	requireEq(t, "SignatureFormat", got.SignatureFormat, "cms-1.0.0")
	requireEq(t, "Signature", got.Signature, "c2ln")
	requireEq(t, "MetadataSignature", got.MetadataSignature, "bXNpZw==")

	if len(got.Manifests) != 2 || got.Manifests[0].Filename != "Package.swift" || got.Manifests[1].SwiftVersion != "4.2" ||
		got.Manifests[1].ToolsVersion != "4.2" {
		t.Errorf("manifests = %+v", got.Manifests)
	}

	if other, _ := pg.GetSwiftRelease(ctx, repo, "mona", "LinkedList", "9.9.9"); other != nil {
		t.Errorf("unknown version = %+v, want nil", other)
	}

	bare, _ := pg.GetSwiftRelease(ctx, repo, "other", "Tool", "0.1.0")
	if bare == nil || bare.Signature != "" || len(bare.Manifests) != 0 {
		t.Errorf("release without extras = %+v", bare)
	}

	versions, err := pg.ListSwiftReleaseVersions(ctx, repo, "Mona", "LINKEDLIST")
	if err != nil || len(versions) != 2 {
		t.Fatalf("ListSwiftReleaseVersions = %v, %v", versions, err)
	}

	vs := []string{versions[0].Version, versions[1].Version}
	slices.Sort(vs)

	if !slices.Equal(vs, []string{"1.0.0", "1.1.0"}) {
		t.Errorf("versions = %v", vs)
	}

	ids, err := pg.ListSwiftIdentifiers(ctx, repo, "github.com/mona/linkedlist")
	if err != nil || !slices.Equal(ids, []string{"mona.LinkedList"}) {
		t.Fatalf("ListSwiftIdentifiers = %v, %v (a package once, whatever its releases)", ids, err)
	}

	if none, err := pg.ListSwiftIdentifiers(ctx, repo, "github.com/mona/other"); err != nil || len(none) != 0 {
		t.Errorf("identifiers for an unknown URL = %v, %v", none, err)
	}

	f := readFile(t, pg, r1.ID)
	requireEq(t, "path", f.path, "mona/linkedlist/1.0.0.zip")
	requireEq(t, "filename", f.filename, "LinkedList-1.0.0.zip")
	requireEq(t, "package name", f.pkgName, "mona.LinkedList")
	requireEq(t, "package name_key", f.pkgKey, "mona.linkedlist")
	requireEq(t, "format", f.format, "swift")
	requireEq(t, "version", f.version, "1.0.0")

	if err := pg.DeleteSwiftRelease(ctx, repo, r2.ID); err != nil {
		t.Fatal(err)
	}

	requireGeneric(t, pg, repo, genericRows{2, 2, 2})
	requireMiscReleased(t, pg, repo, 1)

	// A deleted release is gone from the version list and from the identifier
	// lookup once its package has no release left.
	if versions, _ := pg.ListSwiftReleaseVersions(ctx, repo, "mona", "LinkedList"); len(versions) != 1 {
		t.Errorf("versions after delete = %v", versions)
	}

	for _, id := range []string{r1.ID, r3.ID} {
		if err := pg.DeleteSwiftRelease(ctx, repo, id); err != nil {
			t.Fatal(err)
		}
	}

	requireGeneric(t, pg, repo, genericRows{0, 0, 0})

	if ids, _ := pg.ListSwiftIdentifiers(ctx, repo, "github.com/mona/linkedlist"); len(ids) != 0 {
		t.Errorf("identifiers after every delete = %v", ids)
	}
}
