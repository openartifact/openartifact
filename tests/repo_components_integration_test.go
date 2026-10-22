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
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
)

const componentsBlobSHA = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func execSQL(t *testing.T, pg *database.PostgresDB, query string, args ...any) {
	t.Helper()

	if _, err := pg.GetDB().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func newRepo(t *testing.T, pg *database.PostgresDB, name, format string) string {
	t.Helper()

	systemOrg := "00000000-0000-0000-0000-000000000001"
	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &systemOrg}
	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	return repo.ID
}

type npmRawFixture struct {
	pg               *database.PostgresDB
	npmRepo, rawRepo string
}

// seedNpmAndRaw creates an npm repo with two versions of @acme/widget, a
// second npm repo holding a package that must not leak, and a raw repo with
// two paths. Every row points at one 1234-byte blob.
func seedNpmAndRaw(t *testing.T, pg *database.PostgresDB) npmRawFixture {
	t.Helper()

	ctx := context.Background()

	blobID, err := pg.UpsertBlob(ctx, componentsBlobSHA, componentsBlobSHA, 1234)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	f := npmRawFixture{
		pg:      pg,
		npmRepo: newRepo(t, pg, "npm-main", "npm"),
		rawRepo: newRepo(t, pg, "raw-main", "raw"),
	}
	otherNpmRepo := newRepo(t, pg, "npm-other", "npm")

	pkgID := insertNpmPackage(t, pg, f.npmRepo, "@acme/widget")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		insertNpmVersion(t, pg, pkgID, v, "widget-"+v+".tgz", blobID)
	}

	// A package in another repository must not leak into npm-main.
	insertNpmVersion(t, pg, insertNpmPackage(t, pg, otherNpmRepo, "leak"), "9.9.9", "leak-9.9.9.tgz", blobID)

	execSQL(t, pg, `INSERT INTO raw_artifacts (repository_id, path, blob_id) VALUES ($1, 'dir/a.txt', $2), ($1, 'b.bin', $2)`, f.rawRepo, blobID)

	return f
}

func insertNpmPackage(t *testing.T, pg *database.PostgresDB, repoID, fullName string) string {
	t.Helper()

	var id string
	if err := pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO npm_packages (repository_id, name, full_name) VALUES ($1, $2, $2) RETURNING id`, repoID, fullName,
	).Scan(&id); err != nil {
		t.Fatalf("insert npm_packages: %v", err)
	}

	return id
}

func insertNpmVersion(t *testing.T, pg *database.PostgresDB, pkgID, version, tarball, blobID string) {
	t.Helper()

	execSQL(t, pg,
		`INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id)
		 VALUES ($1, $2, '{}'::jsonb, 'sha512-x', 'abc', $3, $4)`,
		pkgID, version, tarball, blobID)
}

// listOK lists a repo's packages and fails the test on error or when the
// total / page length differ from the expectation.
func listOK(t *testing.T, pg *database.PostgresDB, repoID, format string, page, limit, wantTotal, wantLen int) []*models.Component {
	t.Helper()

	got, total, err := pg.ListComponentsByRepo(context.Background(), repoID, format, page, limit)
	if err != nil {
		t.Fatalf("ListComponentsByRepo(format=%q page=%d limit=%d): %v", format, page, limit, err)
	}

	if total != wantTotal || len(got) != wantLen {
		t.Fatalf("ListComponentsByRepo(format=%q page=%d limit=%d): total=%d len=%d, want %d/%d",
			format, page, limit, total, len(got), wantTotal, wantLen)
	}

	return got
}

// TestListComponentsByRepo_NpmAndRaw covers the original bug: repositories of
// formats other than deb/rpm listed zero packages.
func TestListComponentsByRepo_NpmAndRaw(t *testing.T) {
	f := seedNpmAndRaw(t, freshPostgres(t, 5))

	t.Run("npm lists every version with blob sha and size", func(t *testing.T) {
		versions := map[string]*models.Component{}
		for _, c := range listOK(t, f.pg, f.npmRepo, "", 1, 25, 2, 2) {
			versions[c.Version] = c
		}

		for _, v := range []string{"1.0.0", "1.1.0"} {
			c := versions[v]
			if c == nil {
				t.Fatalf("version %s missing from %+v", v, versions)
			}

			if c.Name != "@acme/widget" || c.Format != "npm" || c.SHA256 != componentsBlobSHA ||
				c.PackageSize != 1234 || c.Filename != "widget-"+v+".tgz" || c.RepositoryID != f.npmRepo || c.ID == "" {
				t.Errorf("unexpected component for %s: %+v", v, c)
			}
		}
	})

	t.Run("raw lists paths", func(t *testing.T) {
		names := map[string]bool{}

		for _, c := range listOK(t, f.pg, f.rawRepo, "", 1, 25, 2, 2) {
			names[c.Name] = true

			if c.Format != "raw" || c.Filename != c.Name || c.PackageSize != 1234 {
				t.Errorf("unexpected raw component: %+v", c)
			}
		}

		if !names["dir/a.txt"] || !names["b.bin"] {
			t.Errorf("raw names = %v", names)
		}
	})

	t.Run("format filter", func(t *testing.T) {
		listOK(t, f.pg, f.npmRepo, "npm", 1, 25, 2, 2)
		listOK(t, f.pg, f.npmRepo, "raw", 1, 25, 0, 0)
	})

	t.Run("pagination keeps the full total", func(t *testing.T) {
		listOK(t, f.pg, f.npmRepo, "", 2, 1, 2, 1)
		listOK(t, f.pg, f.npmRepo, "", 3, 1, 2, 0)
	})
}

// TestListComponentsByRepo_AllFormats seeds one row in every per-format table
// and checks each is listed exactly once with the documented mapping.
func TestListComponentsByRepo_AllFormats(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	blobID, err := pg.UpsertBlob(ctx, componentsBlobSHA, componentsBlobSHA, 4096)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	repo := newRepo(t, pg, "everything", "raw")

	var distID, compID, npmPkgID string
	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO debian_distributions (repository_id, name) VALUES ($1, 'stable') RETURNING id`, repo).Scan(&distID); err != nil {
		t.Fatalf("insert debian_distributions: %v", err)
	}

	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO debian_components (distribution_id, name) VALUES ($1, 'main') RETURNING id`, distID).Scan(&compID); err != nil {
		t.Fatalf("insert debian_components: %v", err)
	}

	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO npm_packages (repository_id, name, full_name) VALUES ($1, 'n', 'n') RETURNING id`, repo).Scan(&npmPkgID); err != nil {
		t.Fatalf("insert npm_packages: %v", err)
	}

	seeds := []struct {
		format, name, version, arch, filename string
		query                                 string
		args                                  []any
	}{
		{"deb", "dpkg", "1.0", "amd64", "dpkg_1.0_amd64.deb",
			`INSERT INTO deb_packages (repository_id, distribution_id, component_id, blob_id, name, version, arch, filename) VALUES ($1,$2,$3,$4,'dpkg','1.0','amd64','dpkg_1.0_amd64.deb')`,
			[]any{repo, distID, compID, blobID}},
		{"rpm", "rpmpkg", "2.0", "x86_64", "rpmpkg-2.0-1.x86_64.rpm",
			`INSERT INTO rpm_packages (repository_id, blob_id, name, version, release, arch, filename, package_size) VALUES ($1,$2,'rpmpkg','2.0','1','x86_64','rpmpkg-2.0-1.x86_64.rpm',4096)`,
			[]any{repo, blobID}},
		{"npm", "n", "3.0.0", "", "n-3.0.0.tgz",
			`INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id) VALUES ($1,'3.0.0','{}'::jsonb,'i','s','n-3.0.0.tgz',$2)`,
			[]any{npmPkgID, blobID}},
		{"pypi", "pyp", "4.0", "py3", "pyp-4.0-py3-none-any.whl",
			`INSERT INTO pypi_packages (repository_id, name, version, filename, package_type, python_version, blob_id) VALUES ($1,'pyp','4.0','pyp-4.0-py3-none-any.whl','bdist_wheel','py3',$2)`,
			[]any{repo, blobID}},
		{"maven", "org.acme:lib", "5.0", "sources", "org/acme/lib/5.0/lib-5.0-sources.jar",
			`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, classifier, extension, repo_path, blob_id) VALUES ($1,'org.acme','lib','5.0','sources','jar','org/acme/lib/5.0/lib-5.0-sources.jar',$2)`,
			[]any{repo, blobID}},
		{"ivy", "org.acme:mod", "6.0", "", "mod.jar",
			`INSERT INTO ivy_artifacts (repository_id, organisation, module, revision, artifact_name, artifact_ext, blob_id) VALUES ($1,'org.acme','mod','6.0','mod','jar',$2)`,
			[]any{repo, blobID}},
		{"sbt", "org.acme:lib_2.13", "7.0", "2.13", "lib_2.13-7.0.jar",
			`INSERT INTO sbt_artifacts (repository_id, group_id, artifact_id, version, scala_version, filename, blob_id) VALUES ($1,'org.acme','lib_2.13','7.0','2.13','lib_2.13-7.0.jar',$2)`,
			[]any{repo, blobID}},
		{"nuget", "Acme.Lib", "8.0", "", "Acme.Lib.8.0.nupkg",
			`INSERT INTO nuget_packages (repository_id, package_id, version, blob_id) VALUES ($1,'Acme.Lib','8.0',$2)`,
			[]any{repo, blobID}},
		{"go", "example.com/mod", "v9.0.0", "", "v9.0.0.zip",
			`INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id) VALUES ($1,'example.com/mod','v9.0.0','module example.com/mod',$2)`,
			[]any{repo, blobID}},
		{"cargo", "crt", "10.0.0", "", "crt-10.0.0.crate",
			`INSERT INTO cargo_crates (repository_id, name, version, blob_id) VALUES ($1,'crt','10.0.0',$2)`,
			[]any{repo, blobID}},
		{"rubygems", "gem", "11.0", "java", "gem-11.0-java.gem",
			`INSERT INTO rubygems_gems (repository_id, name, version, platform, blob_id, size) VALUES ($1,'gem','11.0','java',$2,4096)`,
			[]any{repo, blobID}},
		{"conda", "cnd", "12.0", "linux-64", "cnd-12.0-h0_0.tar.bz2",
			`INSERT INTO conda_packages (repository_id, name, version, build_string, platform, blob_id) VALUES ($1,'cnd','12.0','h0_0','linux-64',$2)`,
			[]any{repo, blobID}},
		{"conan", "cn", "13.0", "", "cn/13.0@_/_#0",
			`INSERT INTO conan_packages (repository_id, name, version) VALUES ($1,'cn','13.0')`,
			[]any{repo}},
		{"helm", "chart", "14.0.0", "", "chart-14.0.0.tgz",
			`INSERT INTO helm_charts (repository_id, name, version, blob_id) VALUES ($1,'chart','14.0.0',$2)`,
			[]any{repo, blobID}},
		{"raw", "some/file.txt", "", "", "some/file.txt",
			`INSERT INTO raw_artifacts (repository_id, path, blob_id) VALUES ($1,'some/file.txt',$2)`,
			[]any{repo, blobID}},
		{"docker", "everything", "latest", "", "sha256:" + componentsBlobSHA,
			`WITH m AS (INSERT INTO docker_manifests (repository_id, digest, media_type, size, blob_id) VALUES ($1,'sha256:` + componentsBlobSHA + `','application/vnd.oci.image.manifest.v1+json',4096,$2) RETURNING repository_id, digest)
			 INSERT INTO docker_tags (repository_id, name, manifest_digest) SELECT repository_id, 'latest', digest FROM m`,
			[]any{repo, blobID}},
	}

	for _, s := range seeds {
		execSQL(t, pg, s.query, s.args...)
	}

	listOK(t, pg, repo, "", 1, 100, len(seeds), len(seeds))

	for _, s := range seeds {
		got := listOK(t, pg, repo, s.format, 1, 100, 1, 1)
		checkSeededComponent(t, got[0], s.format, s.name, s.version, s.arch, s.filename)
		checkDownloadLookupMatchesListing(t, pg, repo, got[0])
	}
}

// checkDownloadLookupMatchesListing guards GetRepoComponentBlob against
// drifting from ListRepoComponents: every listed id must resolve, in its own
// repository only, to the listed format, filename and blob.
func checkDownloadLookupMatchesListing(t *testing.T, pg *database.PostgresDB, repoID string, c *models.Component) {
	t.Helper()

	ctx := context.Background()

	blob, err := pg.GetRepoComponentBlob(ctx, repoID, c.ID)
	if err != nil || blob == nil {
		t.Fatalf("%s: GetRepoComponentBlob(%s) = %v, %v; want a row", c.Format, c.ID, blob, err)
	}

	if blob.ID != c.ID || blob.Format != c.Format || blob.Filename != c.Filename || blob.SHA256 != c.SHA256 {
		t.Errorf("%s: download lookup %+v differs from listing id=%s filename=%q sha256=%q", c.Format, blob, c.ID, c.Filename, c.SHA256)
	}

	if c.SHA256 != "" && blob.Size != c.PackageSize {
		t.Errorf("%s: download size %d, listing size %d", c.Format, blob.Size, c.PackageSize)
	}

	// The AI analysis worker takes a package's identity from this lookup.
	if blob.Name != c.Name || blob.Version != c.Version {
		t.Errorf("%s: download lookup name/version %q %q differ from listing %q %q", c.Format, blob.Name, blob.Version, c.Name, c.Version)
	}

	other := newRepo(t, pg, "other-"+c.Format, "raw")
	if foreign, err := pg.GetRepoComponentBlob(ctx, other, c.ID); err != nil || foreign != nil {
		t.Errorf("%s: id resolved in another repository: %+v, %v", c.Format, foreign, err)
	}
}

// checkSeededComponent asserts the documented per-format mapping. Every seeded
// row points at the 4096-byte blob, except conan whose blob_id is NULL.
func checkSeededComponent(t *testing.T, c *models.Component, format, name, version, arch, filename string) {
	t.Helper()

	if c.Format != format || c.Name != name || c.Version != version || c.Arch != arch || c.Filename != filename {
		t.Errorf("%s: got name=%q version=%q arch=%q filename=%q, want %q %q %q %q",
			format, c.Name, c.Version, c.Arch, c.Filename, name, version, arch, filename)
	}

	wantSHA, wantSize := componentsBlobSHA, int64(4096)
	if format == "conan" {
		wantSHA, wantSize = "", 0
	}

	if c.SHA256 != wantSHA || c.PackageSize != wantSize {
		t.Errorf("%s: sha256=%q size=%d, want %q %d", format, c.SHA256, c.PackageSize, wantSHA, wantSize)
	}
}

// downloadAdminDB grants the test principal full access without seeding users.
type downloadAdminDB struct {
	*database.PostgresDB
}

func (downloadAdminDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return []*models.PermissionTarget{{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: []string{"manage", "read", "write"}}}, nil
}

// TestDownloadPackage_NpmAndRaw covers the download half of the listing bug:
// ids listed for non-deb/rpm formats must download their blob, and an id from
// another repository must 404.
func TestDownloadPackage_NpmAndRaw(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	content := []byte("the exact bytes of the stored artifact")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if _, err := store.Put(ctx, bytes.NewReader(content), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("blob Put: %v", err)
	}

	blobID, err := pg.UpsertBlob(ctx, sha, sha, int64(len(content)))
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	npmRepo, rawRepo, otherNpm := newRepo(t, pg, "npm-main", "npm"), newRepo(t, pg, "raw-main", "raw"), newRepo(t, pg, "npm-other", "npm")
	insertNpmVersion(t, pg, insertNpmPackage(t, pg, npmRepo, "@acme/widget"), "1.0.0", "widget-1.0.0.tgz", blobID)
	insertNpmVersion(t, pg, insertNpmPackage(t, pg, otherNpm, "leak"), "9.9.9", "leak-9.9.9.tgz", blobID)
	execSQL(t, pg, `INSERT INTO raw_artifacts (repository_id, path, blob_id) VALUES ($1, 'dir/a.txt', $2)`, rawRepo, blobID)

	log := newTestLogger(t)
	db := downloadAdminDB{pg}
	router := chi.NewRouter()
	api.NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithBlobStore(store).RegisterAuthed(router)

	userID := "download-admin"
	download := func(repoName, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/repositories/"+repoName+"/packages/"+id+"/download", http.NoBody)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "admin"}))

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)

		return rw
	}

	npmID := listOK(t, pg, npmRepo, "", 1, 25, 1, 1)[0].ID
	rawID := listOK(t, pg, rawRepo, "", 1, 25, 1, 1)[0].ID
	otherID := listOK(t, pg, otherNpm, "", 1, 25, 1, 1)[0].ID

	for _, tc := range []struct{ name, repo, id, filename string }{
		{"npm tarball", "npm-main", npmID, "widget-1.0.0.tgz"},
		{"raw path basename", "raw-main", rawID, "a.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rw := download(tc.repo, tc.id)
			if rw.Code != http.StatusOK || !bytes.Equal(rw.Body.Bytes(), content) {
				t.Fatalf("status %d body %q, want 200 and the blob bytes", rw.Code, rw.Body.String())
			}

			if got := rw.Header().Get("Content-Length"); got != strconv.Itoa(len(content)) {
				t.Errorf("Content-Length = %q, want %d", got, len(content))
			}

			if _, params, err := mime.ParseMediaType(rw.Header().Get("Content-Disposition")); err != nil || params["filename"] != tc.filename {
				t.Errorf("Content-Disposition = %q, want attachment filename %q", rw.Header().Get("Content-Disposition"), tc.filename)
			}
		})
	}

	for _, tc := range []struct{ name, repo, id string }{
		{"npm id under the raw repository", "raw-main", npmID},
		{"raw id under the npm repository", "npm-main", rawID},
		{"id of another npm repository", "npm-main", otherID},
		{"unknown uuid", "npm-main", "00000000-0000-0000-0000-00000000dead"},
		{"malformed id", "npm-main", "not-a-uuid"},
	} {
		t.Run("404 "+tc.name, func(t *testing.T) {
			if rw := download(tc.repo, tc.id); rw.Code != http.StatusNotFound {
				t.Fatalf("status %d body %q, want 404", rw.Code, rw.Body.String())
			}
		})
	}
}

// TestSearchComponents_ExactName covers the server-side package lookup
// behind the package detail page: an exact listed name (scoped npm included)
// returns every version of that package and nothing else.
func TestSearchComponents_ExactName(t *testing.T) {
	pg := freshPostgres(t, 5)
	f := seedNpmAndRaw(t, pg)
	ctx := context.Background()
	search := func(repo, format, name string) ([]*models.Component, int, error) {
		return pg.SearchComponents(ctx, repo, database.ComponentListOptions{Format: format, Name: name, Page: 1, Limit: 100})
	}

	got, total, err := search(f.npmRepo, "", "@acme/widget")
	if err != nil || total != 2 || len(got) != 2 {
		t.Fatalf("npm @acme/widget: total=%d len=%d err=%v, want 2/2", total, len(got), err)
	}

	if got, total, err = search(f.npmRepo, "", "widget"); err != nil || total != 0 || len(got) != 0 {
		t.Fatalf("partial name must not match: total=%d len=%d err=%v", total, len(got), err)
	}

	if got, total, err = search(f.rawRepo, "raw", "dir/a.txt"); err != nil || total != 1 || len(got) != 1 {
		t.Fatalf("raw dir/a.txt: total=%d len=%d err=%v, want 1/1", total, len(got), err)
	}
}
