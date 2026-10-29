// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// detailFixture is the seeded state of TestArtifactDetailReadsGenericTables.
type detailFixture struct {
	pg                          *PostgresDB
	npmRepo, mavenRepo, goRepo  string
	npmFile, mavenFile, goFiles []string
}

func (f *detailFixture) detail(t *testing.T, repo, id string) *models.ComponentDetail {
	t.Helper()

	d, err := f.pg.GetRepoComponentDetail(context.Background(), repo, id)
	if err != nil {
		t.Fatalf("detail of %s: %v", id, err)
	}

	return d
}

func (f *detailFixture) write(t *testing.T, w *PackageWrite) []string {
	t.Helper()

	res, err := f.pg.writePackage(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 0, len(res.Files))
	for _, file := range res.Files {
		ids = append(ids, file.ID)
	}

	return ids
}

func (f *detailFixture) seedNPM(t *testing.T) {
	t.Helper()

	f.npmFile = f.write(t, &PackageWrite{
		RepoID: f.npmRepo, Format: "npm", Name: "@acme/widget", NameKey: "@acme/widget",
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
}

func (f *detailFixture) seedMaven(t *testing.T) {
	t.Helper()

	f.mavenFile = f.write(t, &PackageWrite{
		RepoID: f.mavenRepo, Format: "maven", Name: "org.acme:lib", NameKey: "org.acme:lib",
		PkgAttrs: map[string]any{"group_id": "org.acme", "artifact_id": "lib"},
		Version:  "1.0", VersionKey: "1.0",
		Files: []FileWrite{{
			Path: "org/acme/lib/1.0/lib-1.0-sources.jar", Filename: "org/acme/lib/1.0/lib-1.0-sources.jar",
			Arch:      "sources",
			Blob:      BlobWrite{SHA256: strings.Repeat("b", 64), Key: "k-mvn", Size: 3},
			Checksums: map[string]string{"md5": "0123"},
		}},
	})

	_, err := f.pg.GetDB().ExecContext(context.Background(),
		`INSERT INTO maven_files (id, classifier, extension, is_snapshot) VALUES ($1, 'sources', 'jar', true)`,
		f.mavenFile[0])
	if err != nil {
		t.Fatal(err)
	}
}

func (f *detailFixture) seedGo(t *testing.T) {
	t.Helper()

	f.goFiles = f.write(t, &PackageWrite{
		RepoID: f.goRepo, Format: "go", Name: "example.com/m", NameKey: "example.com/m",
		Version: "v1.0.0", VersionKey: "v1.0.0",
		Files: []FileWrite{
			{
				Path: "example.com/m/@v/v1.0.0.zip", Filename: "v1.0.0.zip",
				Blob: BlobWrite{SHA256: strings.Repeat("c", 64), Key: "k-go", Size: 5},
			},
			{
				Path: "example.com/m/@v/v1.0.0.mod", Filename: "v1.0.0.mod", Role: "mod", Unlisted: true,
				Attrs: map[string]any{"go_mod_content": "module example.com/m\n\nrequire example.com/dep v1.2.3\n"},
			},
		},
	})
}

type detailCase struct {
	name    string
	repo    string
	id      string
	resolve bool
	ok      func(d *models.ComponentDetail) bool
}

// detailCases are the expectations on the seeded fixture.
func detailCases(f *detailFixture) []detailCase {
	return []detailCase{

		{"npm columns", f.npmRepo, f.npmFile[0], true, func(d *models.ComponentDetail) bool {
			return d.Format == "npm" && d.Name == "@acme/widget" && d.Version == "1.0.0" &&
				d.Filename == "widget-1.0.0.tgz" && d.SizeBytes == 11
		}},
		// An object license is not display text; the description and checksums are.
		{"npm metadata", f.npmRepo, f.npmFile[0], true, func(d *models.ComponentDetail) bool {
			return d.Description == "A widget" && d.License == "" && d.SHA1 == "d34db33f" &&
				d.Info["integrity"] == "sha512-x" && d.Info["scope"] == "acme" && d.DepsDecoder == "npm-manifest"
		}},
		{"maven", f.mavenRepo, f.mavenFile[0], true, func(d *models.ComponentDetail) bool {
			return d.Arch == "sources" && d.MD5 == "0123" && d.Info["group_id"] == "org.acme" &&
				d.Info["classifier"] == "sources" && d.Info["snapshot"] == "true"
		}},
		{"go", f.goRepo, f.goFiles[0], true, func(d *models.ComponentDetail) bool {
			return d.Info["module_path"] == "example.com/m" && d.DepsDecoder == "go-mod" && len(d.Deps) > 0
		}},
		{"unlisted file resolves nothing", f.goRepo, f.goFiles[1], false, nil},
		{"id is scoped to its repository", f.mavenRepo, f.npmFile[0], false, nil},
	}
}

func TestArtifactDetailReadsGenericTables(t *testing.T) {
	registerFixtures(t)

	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()
	f := &detailFixture{
		pg:        pg,
		npmRepo:   seedRepo(t, db, "detail-npm", "npm"),
		mavenRepo: seedRepo(t, db, "detail-maven", "maven"),
		goRepo:    seedRepo(t, db, "detail-go", "go"),
	}
	f.seedNPM(t)
	f.seedMaven(t)
	f.seedGo(t)

	cases := detailCases(f)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := f.detail(t, tc.repo, tc.id)
			if !tc.resolve {
				if d != nil {
					t.Fatalf("resolved: %+v", d)
				}

				return
			}

			if d == nil || !tc.ok(d) {
				t.Fatalf("detail %+v", d)
			}
		})
	}
}
