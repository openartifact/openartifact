// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// A deb or rpm Package URL carries its vendor namespace, the form the
// vulnerability side uses (pkg:deb/debian/...); the version is encoded as
// strictly as there ("1:2" is "1%3A2").
func TestGeneratePURLNamespacesDebAndRPM(t *testing.T) {
	cases := []struct{ format, name, version, want string }{
		{"deb", "openssl", "3.0.11-1~deb12u2", "pkg:deb/debian/openssl@3.0.11-1~deb12u2"},
		{"deb", "zlib1g", "1:1.2.11-1ubuntu1", "pkg:deb/ubuntu/zlib1g@1%3A1.2.11-1ubuntu1"},
		{"deb", "openssl", "unknown", "pkg:deb/debian/openssl"},
		{"rpm", "openssl", "3.0.7-1.el9", "pkg:rpm/redhat/openssl@3.0.7-1.el9"},
		{"rpm", "openssl", "3.0.7-1.fc39", "pkg:rpm/fedora/openssl@3.0.7-1.fc39"},
		{"rpm", "openssl", "2:3.0.7-1.el9", "pkg:rpm/redhat/openssl@2%3A3.0.7-1.el9"},
		// Nothing tells the vendor of this one.
		{"rpm", "openssl", "3.0.7", "pkg:rpm/openssl@3.0.7"},
		{"npm", "left-pad", "1:2", "pkg:npm/left-pad@1%3A2"},
	}

	for _, c := range cases {
		if got := generatePURL(c.format, c.name, c.version); got != c.want {
			t.Errorf("generatePURL(%q, %q, %q) = %q, want %q", c.format, c.name, c.version, got, c.want)
		}
	}

	// An explicit namespace, told by the package file, wins over the version.
	if got, want := generatePURLNS("deb", "ubuntu", "demo", "1.0-1"), "pkg:deb/ubuntu/demo@1.0-1"; got != want {
		t.Errorf("generatePURLNS = %q, want %q", got, want)
	}
}

func TestPurlEncodeEscapesTheColon(t *testing.T) {
	if got, want := purlEncode("a:b c"), "a%3Ab%20c"; got != want {
		t.Errorf("purlEncode = %q, want %q", got, want)
	}
}

func rootAndComponents(t *testing.T, res *SBOMResult) (cdxComponent, map[string]cdxComponent) {
	t.Helper()

	var bom cdxBOM
	if err := json.Unmarshal(res.Body, &bom); err != nil {
		t.Fatal(err)
	}

	return *bom.Metadata.Component, cdxComponents(t, res)
}

// The subject of an SBOM of a deb or rpm has the namespace too, with or without
// the package file, so that it matches the vulnerability reports.
func TestSBOMRootOfDebAndRPMHasANamespace(t *testing.T) {
	ctx := context.Background()

	t.Run("deb from properties only", func(t *testing.T) {
		meta := map[string]any{"dependencies": []any{map[string]any{"name": "libc6", "version": "2.31"}}}

		res, err := newFakeGen(&fakeDB{}).GenerateCycloneDX(ctx, "repo", "demo@1.0-1", "deb", meta)
		if err != nil {
			t.Fatal(err)
		}

		root, comps := rootAndComponents(t, res)
		if root.PURL != "pkg:deb/debian/demo@1.0-1" {
			t.Errorf("root purl = %q", root.PURL)
		}

		if _, ok := comps["pkg:deb/debian/libc6@2.31"]; !ok {
			t.Errorf("dependency purls = %v", keys(comps))
		}
	})

	t.Run("ubuntu version", func(t *testing.T) {
		res, err := newFakeGen(&fakeDB{}).GenerateCycloneDX(ctx, "repo", "demo@1:1.0-1ubuntu1", "deb", nil)
		if err != nil {
			t.Fatal(err)
		}

		if root, _ := rootAndComponents(t, res); root.PURL != "pkg:deb/ubuntu/demo@1%3A1.0-1ubuntu1" {
			t.Errorf("root purl = %q", root.PURL)
		}
	})

	t.Run("rpm from properties only", func(t *testing.T) {
		res, err := newFakeGen(&fakeDB{}).GenerateCycloneDX(ctx, "repo", "demo@1.0-1.el9", "rpm", nil)
		if err != nil {
			t.Fatal(err)
		}

		if root, _ := rootAndComponents(t, res); root.PURL != "pkg:rpm/redhat/demo@1.0-1.el9" {
			t.Errorf("root purl = %q", root.PURL)
		}
	})

	// The package file knows better than the version: its maintainer is Ubuntu's.
	t.Run("namespace told by the package file", func(t *testing.T) {
		deb := debPackage(t, "Package: demo\nVersion: 1.0-1\nArchitecture: amd64\n"+
			"Maintainer: Ubuntu Developers <ubuntu-devel-discuss@lists.ubuntu.com>\nDepends: libc6\n")
		db := &fakeDB{comps: []*models.Component{
			{Format: "deb", Name: "demo", Version: "1.0-1", Filename: "demo_1.0-1_amd64.deb", SHA256: shaTGZ},
		}}
		g := newFakeGen(db).WithBlobStore(&memBlobs{files: map[string][]byte{shaTGZ: deb}})

		res, err := g.GenerateCycloneDX(ctx, "repo", "demo@1.0-1", "deb", nil)
		if err != nil {
			t.Fatal(err)
		}

		root, comps := rootAndComponents(t, res)
		if root.PURL != "pkg:deb/ubuntu/demo@1.0-1" {
			t.Errorf("root purl = %q, want the namespace of the package file", root.PURL)
		}

		if _, ok := comps["pkg:deb/ubuntu/libc6"]; !ok {
			t.Errorf("dependency purls = %v", keys(comps))
		}
	})

	t.Run("subject from a listing row id", func(t *testing.T) {
		const rowID = "0b7e7f64-3a4f-4b43-9d0f-7a3a3a3a3a3a"

		db := &rowDB{
			blob: &models.ComponentBlob{
				ID:       rowID,
				Format:   "deb",
				Name:     "demo",
				Version:  "1.0-1",
				Filename: "demo_1.0-1_amd64.deb",
				SHA256:   shaTGZ,
			},
		}
		g := newFakeGen(&db.fakeDB)
		g.db = db

		res, err := g.GenerateCycloneDX(ctx, "repo", rowID, "deb", nil)
		if err != nil {
			t.Fatal(err)
		}

		if root, _ := rootAndComponents(t, res); root.PURL != "pkg:deb/debian/demo@1.0-1" {
			t.Errorf("root purl = %q", root.PURL)
		}
	})
}
