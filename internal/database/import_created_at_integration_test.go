// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestImportCreatedAt drives every hosted-format create query with and without
// an origin creation time: a set CreatedAt must land in the row's created_at,
// an unset one must be stamped by the database (now). updated_at is always now.
// importEnv carries what the per-format creators share.
type importEnv struct {
	pg  *PostgresDB
	ctx context.Context //nolint:containedctx // test fixture
	seq int
}

// uniq returns a fresh 64-hex string: names, blob keys and checksums alike
// (several formats use the blob key as the blob's sha256).
func (e *importEnv) uniq(prefix string) string {
	e.seq++

	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", prefix, e.seq)))

	return hex.EncodeToString(sum[:])
}

// check re-reads created_at/updated_at from table and compares created_at with
// want (zero want = "about now").
func (e *importEnv) check(t *testing.T, table, id string, want time.Time) {
	t.Helper()

	var created, updated time.Time

	q := fmt.Sprintf(`SELECT created_at, updated_at FROM %s WHERE id = $1`, table)
	if err := e.pg.GetDB().QueryRowContext(e.ctx, q, id).Scan(&created, &updated); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}

	if want.IsZero() {
		if d := time.Since(created); d < -time.Minute || d > time.Minute {
			t.Errorf("%s created_at = %v, want about now", table, created)
		}
	} else if !created.Equal(want) {
		t.Errorf("%s created_at = %v, want %v", table, created, want)
	}

	if d := time.Since(updated); d < -time.Minute || d > time.Minute {
		t.Errorf("%s updated_at = %v, want about now", table, updated)
	}
}

func (e *importEnv) createNpm(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	pkg, err := e.pg.CreateNPMPackage(
		e.ctx,
		&models.NPMPackage{RepositoryID: repo, Name: e.uniq("n"), FullName: e.uniq("n"), LatestVersion: "1.0.0"},
	)
	if err != nil {
		t.Fatal(err)
	}

	blobID, err := e.pg.UpsertBlob(e.ctx, e.uniq("sha"), "k", 1)
	if err != nil {
		t.Fatal(err)
	}

	v, err := e.pg.CreateNPMVersion(e.ctx, &models.NPMVersion{
		PackageID: pkg.ID, Version: "1.0.0", ManifestJSON: []byte(`{}`), Integrity: "i", Shasum: "s",
		BlobID: blobID, TarballFilename: "a.tgz", CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}

	return "npm_versions", v.ID
}

func (e *importEnv) createPypi(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.PyPIPackage{RepositoryID: repo, Name: "d", Version: "1", Filename: e.uniq("f"), PackageType: "sdist",
		SHA256Digest: e.uniq("sha"), BlobKey: e.uniq("k"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreatePyPIPackage(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "package_files", p.ID
}

func (e *importEnv) createHelm(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	c := &models.HelmChart{RepositoryID: repo, Name: "c", Version: "1.0.0", Digest: e.uniq("sha"), BlobKey: e.uniq("k"),
		SizeBytes: 1, APIVersion: "v2", Type: "application", CreatedAt: at}
	if err := e.pg.CreateHelmChart(e.ctx, c); err != nil {
		t.Fatal(err)
	}

	return "helm_charts", c.ID
}

func (e *importEnv) createNuget(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.NuGetPackage{RepositoryID: repo, PackageID: "p", Version: "1.0.0", BlobKey: e.uniq("k"), PackageSize: 1, CreatedAt: at}
	if err := e.pg.CreateNuGetPackage(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "nuget_packages", p.ID
}

func (e *importEnv) createRubygems(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	g := &models.RubyGem{RepositoryID: repo, Name: "g", Version: "1.0.0", Platform: "ruby", ChecksumSHA256: e.uniq("sha"),
		BlobKey: e.uniq("k"), Size: 1, Dependencies: []byte(`[]`), MetadataJSON: []byte(`{}`), CreatedAt: at}
	if err := e.pg.CreateRubyGem(e.ctx, g); err != nil {
		t.Fatal(err)
	}

	return "rubygems_gems", g.ID
}

func (e *importEnv) createCargo(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	c := &models.CargoCrate{RepositoryID: repo, Name: "c", Version: "0.1.0", Checksum: e.uniq("sha"), BlobKey: e.uniq("k"),
		SizeBytes: 1, MetadataJSON: []byte(`{}`), CreatedAt: at}
	if err := e.pg.CreateCargoCrate(e.ctx, c); err != nil {
		t.Fatal(err)
	}

	return "cargo_crates", c.ID
}

func (e *importEnv) createGo(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	m := &models.GoModule{RepositoryID: repo, ModulePath: "example.com/m", Version: "v1.0.0", GoModContent: "module example.com/m",
		ZipHash: e.uniq("sha"), ZipBlobKey: e.uniq("k"), ZipSizeBytes: 1, ModBlobKey: e.uniq("k"), CreatedAt: at}
	if err := e.pg.CreateGoModule(e.ctx, m); err != nil {
		t.Fatal(err)
	}

	return "gomod_modules", m.ID
}

func (e *importEnv) createConda(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.CondaPackage{RepositoryID: repo, Name: "d", Version: "1.0", BuildString: "py_0", Platform: "linux-64",
		SHA256: e.uniq("sha"), BlobKey: e.uniq("k"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateCondaPackage(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "package_files", p.ID
}

func (e *importEnv) createComposer(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.ComposerPackage{
		RepositoryID:      repo,
		Name:              "acme/d",
		Version:           "v1.0.0",
		VersionNormalized: "1.0.0.0",
		SHA1:              "da39a3ee5e6b4b0d3255bfef95601890afd80709",
		ComposerJSON:      []byte(`{"name":"acme/d"}`),
		SHA256:            e.uniq("sha"),
		BlobKey:           e.uniq("k"),
		SizeBytes:         1,
		CreatedAt:         at,
	}
	if err := e.pg.CreateComposerPackage(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "composer_packages", p.ID
}

func (e *importEnv) createAnsible(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	c := &models.AnsibleCollection{RepositoryID: repo, Namespace: "acme", Name: "d", Version: "1.0.0", Filename: "acme-d-1.0.0.tar.gz",
		SHA256: e.uniq("sha"), BlobKey: e.uniq("k"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateAnsibleCollection(e.ctx, c); err != nil {
		t.Fatal(err)
	}

	return "ansible_collections", c.ID
}

func (e *importEnv) createPacman(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.PacmanPackage{RepositoryID: repo, Name: "d", Base: "d", Version: "1.0-1", Arch: "x86_64", RepoArch: "x86_64",
		Filename: "d-1.0-1-x86_64.pkg.tar.zst", MD5: "abc", SHA256: e.uniq("sha"), BlobKey: e.uniq("k"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreatePacmanPackage(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "pacman_packages", p.ID
}

func (e *importEnv) createNix(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.NixPath{RepositoryID: repo, StoreHash: e.uniq("h")[:32], Name: "hello-2.12", NarHash: "sha256:abc", NarSize: 1,
		NarFile: "f.nar.xz", Compression: "xz", SHA256: e.uniq("sha"), BlobKey: e.uniq("k"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateNixPath(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "nix_paths", p.ID
}

func (e *importEnv) createConan(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.ConanPackage{
		RepositoryID:    repo,
		Name:            "z",
		Version:         "1.3",
		UserChannel:     "_/_",
		RecipeRevision:  "r",
		PackageID:       "_",
		PackageRevision: "_",
		Filename:        "conanfile.py",
		SHA256:          e.uniq("sha"),
		BlobKey:         e.uniq("k"),
		SizeBytes:       1,
		CreatedAt:       at,
	}
	if err := e.pg.CreateConanPackage(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "conan_packages", p.ID
}

func (e *importEnv) createIvy(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	a := &models.IvyArtifact{RepositoryID: repo, Organisation: "o", Module: "m", Revision: "1", ArtifactName: "m",
		ArtifactType: "jar", ArtifactExt: "jar", SHA256: e.uniq("sha"), BlobKey: e.uniq("k"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateIvyArtifact(e.ctx, a); err != nil {
		t.Fatal(err)
	}

	return "ivy_artifacts", a.ID
}

func (e *importEnv) createSbt(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	a := &models.SBTArtifact{RepositoryID: repo, GroupID: "g", ArtifactID: "a", Version: "1", Filename: "a-1.jar",
		SHA256: e.uniq("sha"), BlobKey: e.uniq("k"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateSBTArtifact(e.ctx, a); err != nil {
		t.Fatal(err)
	}

	return "sbt_artifacts", a.ID
}

func (e *importEnv) createApk(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	a := &models.ApkPackage{RepositoryID: repo, Branch: "v3.20", Repo: "main", Arch: "x86_64", PkgArch: "x86_64", Name: "musl",
		Version: "1.2.5-r0", Checksum: "Q1x", DataHash: "dh", BlobKey: e.uniq("k"), BlobSHA256: e.uniq("sha"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateApkPackage(e.ctx, a); err != nil {
		t.Fatal(err)
	}

	return "apk_packages", a.ID
}

func (e *importEnv) createTerraformModule(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	m := &models.TerraformModule{RepositoryID: repo, Name: "vpc", System: "aws", Version: "1.0.0", ArchiveExt: "zip",
		BlobKey: e.uniq("k"), Checksum: e.uniq("sha"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateTerraformModule(e.ctx, m); err != nil {
		t.Fatal(err)
	}

	return "terraform_modules", m.ID
}

func (e *importEnv) createTerraformProvider(t *testing.T, repo string, at time.Time) (string, string) {
	t.Helper()

	p := &models.TerraformProvider{RepositoryID: repo, Type: "null", Version: "1.0.0", OS: "linux", Arch: "amd64",
		Protocols: []string{"5.0"}, BlobKey: e.uniq("k"), Checksum: e.uniq("sha"), SizeBytes: 1, CreatedAt: at}
	if err := e.pg.CreateTerraformProvider(e.ctx, p); err != nil {
		t.Fatal(err)
	}

	return "terraform_providers", p.ID
}

func TestImportCreatedAt(t *testing.T) {
	e := &importEnv{pg: newQuotaTestDB(t, 8), ctx: context.Background()}
	origin := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)

	formats := map[string]func(*testing.T, string, time.Time) (string, string){
		"npm":      e.createNpm,
		"pypi":     e.createPypi,
		"helm":     e.createHelm,
		"nuget":    e.createNuget,
		"rubygems": e.createRubygems,
		"cargo":    e.createCargo,
		"composer": e.createComposer,
		"go":       e.createGo,
		"conda":    e.createConda,
		"conan":    e.createConan,
		"pacman":   e.createPacman,
		"nix":      e.createNix,
		"ansible":  e.createAnsible,
		"ivy":      e.createIvy,
		"sbt":      e.createSbt,
		"apk":      e.createApk,
		// Both terraform tables live in a terraform repository.
		"terraform":          e.createTerraformModule,
		"terraform-provider": e.createTerraformProvider,
	}

	for format, create := range formats {
		for _, tc := range []struct {
			name string
			at   time.Time
		}{{"imported", origin}, {"normal", time.Time{}}} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				s := newQuotaSeeder(t, e.pg, "import-"+format+"-"+tc.name+e.uniq("r")[:6], strings.TrimSuffix(format, "-provider"))
				table, id := create(t, s.repo, tc.at)
				e.check(t, table, id, tc.at)
			})
		}
	}

	t.Run("docker", func(t *testing.T) { e.dockerCreatedAt(t, origin) })
}

func (e *importEnv) dockerCreatedAt(t *testing.T, origin time.Time) {
	t.Helper()

	pg, ctx := e.pg, e.ctx

	for _, tc := range []struct {
		name string
		at   time.Time
	}{{"imported", origin}, {"normal", time.Time{}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newQuotaSeeder(t, pg, "import-docker-"+e.uniq("r")[:8], "docker")
			blobID := s.blob(10)
			digest := "sha256:" + e.uniq("d")

			m := &models.DockerManifest{
				RepositoryID: s.repo,
				Image:        "img",
				Digest:       digest,
				MediaType:    "application/vnd.oci.image.manifest.v1+json",
				BlobID:       blobID,
				Size:         10,
				CreatedAt:    tc.at,
			}
			if err := pg.CreateDockerManifest(ctx, m); err != nil {
				t.Fatal(err)
			}

			e.check(t, "docker_manifests", m.ID, tc.at)

			tag := &models.DockerTag{RepositoryID: s.repo, Image: "img", Name: "latest", ManifestDigest: digest, CreatedAt: tc.at}
			if err := pg.UpsertDockerTag(ctx, tag); err != nil {
				t.Fatal(err)
			}

			e.check(t, "docker_tags", tag.ID, tc.at)

			// Moving the tag keeps its created_at: only the target changes.
			moved := &models.DockerTag{
				RepositoryID:   s.repo,
				Image:          "img",
				Name:           "latest",
				ManifestDigest: digest,
				CreatedAt:      time.Now().Add(time.Hour),
			}
			if err := pg.UpsertDockerTag(ctx, moved); err != nil {
				t.Fatal(err)
			}

			e.check(t, "docker_tags", tag.ID, tc.at)
		})
	}
}
