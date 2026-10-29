// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

type pkgSeed struct {
	t    *testing.T
	db   *sql.DB
	repo string
}

func (s pkgSeed) exec(query string, args ...any) {
	s.t.Helper()

	if _, err := s.db.ExecContext(context.Background(), query, args...); err != nil {
		s.t.Fatalf("seed %q: %v", query, err)
	}
}

func (s pkgSeed) scan(query string, args ...any) string {
	s.t.Helper()

	var out string
	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&out); err != nil {
		s.t.Fatalf("seed %q: %v", query, err)
	}

	return out
}

func (s pkgSeed) pkg(name string, attrs string) string {
	return s.scan(`INSERT INTO packages (repository_id, format, name, name_key, attrs)
		VALUES ($1, (SELECT format FROM repositories WHERE id = $1), $2, $2, $3::jsonb) RETURNING id::text`, s.repo, name, attrs)
}

func (s pkgSeed) version(pkg, version, key, metadata string) string {
	return s.scan(`INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata)
		VALUES ($1, $2, $3, $4, $5::jsonb) RETURNING id::text`, pkg, s.repo, version, key, metadata)
}

func (s pkgSeed) file(version, path, sha, part string, size int64) string {
	blob := s.scan(`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1, $2, $3)
		ON CONFLICT (sha256) DO UPDATE SET size_bytes = blobs.size_bytes RETURNING id::text`, sha, size, "blobs/"+sha)

	return s.scan(`INSERT INTO package_files
		(version_id, repository_id, path, filename, blob_id, size_bytes, part, attrs, checksums, unit_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '{"k":"v"}'::jsonb, jsonb_build_object('sha256', $8::text), $3) RETURNING id::text`,
		version, s.repo, path, path[strings.LastIndex(path, "/")+1:], blob, size, part, sha)
}

func newPkgRepo(t *testing.T, pg *database.PostgresDB, name, format string) pkgSeed {
	t.Helper()

	sys := models.SystemProjectID
	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &sys}
	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatal(err)
	}

	return pkgSeed{t: t, db: pg.GetDB(), repo: repo.ID}
}

// backupAndRestoreMetadataOnly runs a metadata-only backup of src and restores
// it into dst: the seeded blobs have no bytes in a store, only rows.
func backupAndRestoreMetadataOnly(t *testing.T, src, dst *database.PostgresDB) {
	t.Helper()

	ctx := context.Background()

	job := &models.BackupJob{Status: "pending", Type: "metadata-only"}
	if err := src.CreateBackupJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := newBackupEngine(t, src).RunBackup(ctx, job.ID, "metadata-only"); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	done, err := src.GetBackupJob(ctx, job.ID)
	if err != nil || done.Status != "completed" {
		t.Fatalf("backup job = %+v, %v", done, err)
	}

	if err := newBackupEngine(t, dst).RunRestore(ctx, done.StoragePath); err != nil {
		t.Fatalf("RunRestore: %v", err)
	}
}

func sha(c string) string { return strings.Repeat(c, 64) }

// seedBackupPackages seeds an npm repository with dist-tags, a maven repository
// with a side table, a deb repository with a distribution graph, and a trashed
// version that a backup must leave out.
func seedBackupPackages(t *testing.T, pg *database.PostgresDB) {
	t.Helper()

	npm := newPkgRepo(t, pg, "bk-npm", "npm")
	lodash := npm.pkg("lodash", `{"latest_version":"2.0.0"}`)
	v1 := npm.version(lodash, "1.0.0", "1.0.0", `{"main":"index.js"}`)
	v2 := npm.version(lodash, "2.0.0", "2.0.0", `{"main":"main.js"}`)
	trashed := npm.version(lodash, "0.9.0", "0.9.0", `{}`)
	npm.file(v1, "lodash/-/lodash-1.0.0.tgz", sha("1"), "", 11)
	npm.file(v2, "lodash/-/lodash-2.0.0.tgz", sha("2"), "", 22)
	gone := npm.file(trashed, "lodash/-/lodash-0.9.0.tgz", sha("3"), "", 33)
	npm.exec(`UPDATE package_files SET deleted_at = now() WHERE id = $1`, gone)
	npm.exec(`UPDATE package_versions SET deleted_at = now() WHERE id = $1`, trashed)
	npm.exec(`INSERT INTO package_tags (package_id, tag, version, unit_key)
		VALUES ($1, 'latest', '2.0.0', 'lodash#latest'), ($1, 'next', '1.0.0', 'lodash#next')`, lodash)

	mvn := newPkgRepo(t, pg, "bk-maven", "maven")
	app := mvn.pkg("com.x:app", `{"group_id":"com.x","artifact_id":"app"}`)
	mv := mvn.version(app, "1.0", "1.0", `{}`)
	jar := mvn.file(mv, "com/x/app/1.0/app-1.0.jar", sha("4"), "", 44)
	pom := mvn.file(mv, "com/x/app/1.0/app-1.0.pom", sha("5"), "", 55)
	mvn.exec(`INSERT INTO maven_files (id, classifier, extension, md5, sha1)
		VALUES ($1, '', 'jar', 'm', 's'), ($2, 'sources', 'pom', 'm2', 's2')`, jar, pom)

	deb := newPkgRepo(t, pg, "bk-deb", "deb")
	dist := deb.scan(`INSERT INTO debian_distributions (repository_id, name, codename)
		VALUES ($1, 'stable', 'bookworm') RETURNING id::text`, deb.repo)
	comp := deb.scan(`INSERT INTO debian_components (distribution_id, name) VALUES ($1, 'main') RETURNING id::text`, dist)
	deb.exec(`INSERT INTO debian_architectures (distribution_id, name) VALUES ($1, 'amd64'), ($1, 'all')`, dist)
	hello := deb.pkg("hello", `{}`)
	dv := deb.version(hello, "1.0", "1.0|amd64|"+comp, `{"license":"MIT"}`)
	df := deb.file(dv, "pool/main/h/hello/hello_1.0_amd64.deb", sha("6"), dist, 66)
	deb.exec(`INSERT INTO deb_files (id, distribution_id, component_id, control_fields)
		VALUES ($1, $2, $3, '{"Package":"hello"}'::jsonb)`, df, dist, comp)
}

// canonical queries compare a repository's rows independent of ids; versions and
// files of the deb repository embed companion ids, which are replaced by names.
//
//nolint:gochecknoglobals // read-only table of canonical queries shared by the subtests
var canonicalQueries = map[string]string{
	"packages": `SELECT concat_ws('|', p.format, p.name, p.name_key, p.attrs::text) FROM packages p
		WHERE p.repository_id = r.id`,
	"versions": `SELECT concat_ws('|', p.name, v.version, regexp_replace(v.version_key, '[0-9a-f-]{36}', 'ID', 'g'), v.metadata::text,
		v.deleted_at IS NULL) FROM package_versions v JOIN packages p ON p.id = v.package_id WHERE v.repository_id = r.id`,
	"files": `SELECT concat_ws('|', f.path, f.filename, f.size_bytes, f.role, f.listed, f.arch, regexp_replace(f.part, '[0-9a-f-]{36}', 'ID', 'g'),
		f.attrs::text, f.checksums::text, f.unit_key, f.download_count, b.sha256, b.size_bytes, v.version)
		FROM package_files f JOIN package_versions v ON v.id = f.version_id LEFT JOIN blobs b ON b.id = f.blob_id
		WHERE f.repository_id = r.id AND f.deleted_at IS NULL`,
	"tags": `SELECT concat_ws('|', p.name, t.tag, t.version, t.unit_key) FROM package_tags t JOIN packages p ON p.id = t.package_id
		WHERE p.repository_id = r.id`,
	"maven_files": `SELECT concat_ws('|', f.path, (to_jsonb(x) - 'id')::text) FROM maven_files x JOIN package_files f ON f.id = x.id
		WHERE f.repository_id = r.id`,
	"deb_files": `SELECT concat_ws('|', f.path, d.name, c.name, x.control_fields::text) FROM deb_files x JOIN package_files f ON f.id = x.id
		JOIN debian_distributions d ON d.id = x.distribution_id JOIN debian_components c ON c.id = x.component_id
		WHERE f.repository_id = r.id`,
	"deb_graph": `SELECT concat_ws('|', 'dist', d.name, d.codename) FROM debian_distributions d WHERE d.repository_id = r.id
		UNION ALL SELECT concat_ws('|', 'comp', d.name, c.name) FROM debian_components c JOIN debian_distributions d ON d.id = c.distribution_id WHERE d.repository_id = r.id
		UNION ALL SELECT concat_ws('|', 'arch', d.name, a.name) FROM debian_architectures a JOIN debian_distributions d ON d.id = a.distribution_id WHERE d.repository_id = r.id`,
}

// canonicalRows returns the sorted canonical rows of one query for the
// repository called name.
func canonicalRows(t *testing.T, db *sql.DB, query, name string) []string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(),
		"SELECT q.row FROM (SELECT id FROM repositories WHERE name = $1) r CROSS JOIN LATERAL ("+
			query+") AS q(row) ORDER BY q.row", name)
	if err != nil {
		t.Fatalf("canonical rows of %s: %v", name, err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}

		out = append(out, s)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	slices.Sort(out)

	return out
}

// TestBackupRestore_PackageRowsRealDB backs up npm (with dist-tags), maven (with
// a side table) and deb (with a distribution graph) rows of one real database,
// restores them into another and compares the generic rows of every table.
func TestBackupRestore_PackageRowsRealDB(t *testing.T) {
	src := freshPostgres(t, 5)
	dst := freshPostgres(t, 5)

	seedBackupPackages(t, src)
	backupAndRestoreMetadataOnly(t, src, dst)

	for repo, tables := range map[string][]string{
		"bk-npm":   {"packages", "versions", "files", "tags"},
		"bk-maven": {"packages", "versions", "files", "maven_files"},
		"bk-deb":   {"packages", "versions", "files", "deb_files", "deb_graph"},
	} {
		for _, table := range tables {
			want := canonicalRows(t, src.GetDB(), canonicalQueries[table], repo)
			got := canonicalRows(t, dst.GetDB(), canonicalQueries[table], repo)

			if len(want) == 0 {
				t.Fatalf("%s/%s: nothing seeded", repo, table)
			}

			// The trashed npm version is not part of a backup.
			if table == "versions" && repo == "bk-npm" {
				want = slices.DeleteFunc(want, func(r string) bool { return strings.HasSuffix(r, "|f") })
			}

			if !slices.Equal(want, got) {
				t.Errorf("%s/%s differ after restore\nwant %q\n got %q", repo, table, want, got)
			}
		}
	}

	assertRestoredDebLinks(t, dst.GetDB())
}

// assertRestoredDebLinks checks the ids the deb rows embed point at the restored
// distribution and component, not at the ones of the source repository.
func assertRestoredDebLinks(t *testing.T, db *sql.DB) {
	t.Helper()

	var bad int

	err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM package_files f
		JOIN package_versions v ON v.id = f.version_id
		JOIN deb_files x ON x.id = f.id
		WHERE f.part <> x.distribution_id::text OR v.version_key <> v.version || '|amd64|' || x.component_id::text`).Scan(&bad)
	if err != nil {
		t.Fatal(err)
	}

	if bad != 0 {
		t.Fatalf("%d restored deb rows keep an id of the source repository in part/version_key", bad)
	}
}
