//go:build integration

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/internal/storage"
)

// reEnv is one format's hosted repository, mounted over the shared blob store
// on both the source database and the one rebuilt from storage.
type reEnv struct {
	src, dst *qrEnv
}

func newREEnv(t *testing.T, srcDB, dstDB *database.PostgresDB, bs storage.BlobStore, format string,
	mount func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler,
) *reEnv {
	t.Helper()

	org := models.SystemProjectID
	repo := &models.Repository{Name: "qr-" + format, Format: format, RepoType: "hosted", ProjectID: &org}

	if err := srcDB.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", format, err)
	}

	return &reEnv{
		src: &qrEnv{t: t, pg: srcDB, bs: bs, repoID: repo.ID, repoName: repo.Name, router: mount(t, srcDB, bs)},
		dst: &qrEnv{t: t, pg: dstDB, bs: bs, repoID: repo.ID, repoName: repo.Name, router: mount(t, dstDB, bs)},
	}
}

// reRows dumps a journaled table (minus the columns the journal never carries)
// as ordered JSON, so two databases compare with one string equality.
func reRows(t *testing.T, pg *database.PostgresDB, tbl *recovery.Table) string {
	t.Helper()

	cols := []string{"search_tsv"}
	cols = append(cols, tbl.Exclude...)
	where := ""

	if tbl.Name == "projects" {
		where = " WHERE id <> '" + models.SystemProjectID + "'"
	}

	// Identifiers come from the compiled-in registry.
	q := `SELECT COALESCE(jsonb_agg(to_jsonb(t) - $1::text[] ORDER BY ` + tbl.KeyExpr("t") + `)::text, '[]') FROM ` + tbl.Name + ` t` + where

	var out string
	if err := pg.GetDB().QueryRowContext(context.Background(), q, "{"+strings.Join(cols, ",")+"}").Scan(&out); err != nil {
		t.Fatalf("dump %s: %v", tbl.Name, err)
	}

	return out
}

func reGet(e *qrEnv, path string) (int, string) {
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody))

	return rec.Code, rec.Body.String()
}

// reCompareTables requires every journaled table to hold the same rows in both
// databases and returns how many of them carried rows.
func reCompareTables(t *testing.T, srcDB, dstDB *database.PostgresDB) int {
	t.Helper()

	populated := 0

	for i := range recovery.Tables {
		tbl := &recovery.Tables[i]

		want := reRows(t, srcDB, tbl)
		if got := reRows(t, dstDB, tbl); got != want {
			t.Errorf("%s differs after rebuild:\n want %.600s\n  got %.600s", tbl.Name, want, got)
		}

		if want != "[]" {
			populated++
		}
	}

	return populated
}

// TestRecoveryRebuildAllFormats publishes through the real handlers, lets the
// journal mirror every row into the blob store, rebuilds an empty database from
// that store alone and requires identical rows and identical served bytes.
func TestRecoveryRebuildAllFormats(t *testing.T) {
	srcDB := freshPostgres(t, 8)
	dstDB := freshPostgres(t, 8)

	bs, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	mk := func(format string, mount func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler) *reEnv {
		return newREEnv(t, srcDB, dstDB, bs, format, mount)
	}

	ok := func(step string, e *qrEnv, rec *httptest.ResponseRecorder) {
		t.Helper()
		e.wantStatus(step, rec, http.StatusOK, http.StatusCreated, http.StatusAccepted)
	}

	// Formats with a dedicated upload helper.
	mvn := mk("maven", qrMountMaven)
	ok("maven", mvn.src, mvn.src.mavenPut("com/example/app/1.0.0/app-1.0.0.jar", qpfNoise(1, 900)))
	ok("maven pom", mvn.src, mvn.src.mavenPut("com/example/app/1.0.0/app-1.0.0.pom", []byte("<project><modelVersion>4.0.0</modelVersion></project>")))

	npm := mk("npm", qrMountNPM)
	body, _ := qrNPMBody(t, "qrpkg", "1.0.0")
	ok("npm", npm.src, npm.src.npmPublish("qrpkg", body))

	rpm := mk("rpm", qrMountRPM)
	rec, _ := rpm.src.rpmUpload(qrRPM("rpmone1", 100))
	ok("rpm", rpm.src, rec)

	deb := mk("deb", qrMountDeb)
	rec, _ = deb.src.debUpload(qrDeb(t, "qrdeb", "1.0", 50))
	ok("deb", deb.src, rec)

	helm := mk("helm", qrMountHelm)
	ok("helm", helm.src, helm.src.helmUpload(qrChart(t, "mychart", "0.1.0", 200)))

	var gets []struct {
		e    *reEnv
		path string
	}

	for _, f := range qpfFormats() {
		e := mk(f.format, f.mount)
		rec, _ := f.upload(t, e.src, 1, 300)
		ok(f.format, e.src, rec)
	}

	for _, f := range qrfFormats() {
		e := mk(f.format, f.mount)
		ok(f.format, e.src, f.upload(e.src, 1, 7, 400))

		switch f.format {
		case "raw":
			gets = append(gets, struct {
				e    *reEnv
				path string
			}{e, "/repos/qr-raw/dir/file1.bin"})
		case "ivy", "sbt":
			gets = append(gets, struct {
				e    *reEnv
				path string
			}{e, "/repos/qr-" + f.format + "/org.example/mod1/1.0/mod1-1.0.jar"})
		}
	}

	// Mirror everything the handlers wrote.
	j := recovery.NewJournal(srcDB.GetDB(), bs, newTestLogger(t), nil, recovery.JournalConfig{Batch: 200, Concurrency: 4})
	if _, failed, err := j.Drain(context.Background()); err != nil || failed != 0 {
		t.Fatalf("drain: failed=%d err=%v", failed, err)
	}

	st, err := j.Status(context.Background())
	if err != nil || st.Backlog != 0 || st.SidecarsTracked == 0 {
		t.Fatalf("status after drain: %+v err=%v", st, err)
	}

	rep, err := recovery.NewRebuilder(dstDB.GetDB(), bs, newTestLogger(t), recovery.RebuildOptions{Concurrency: 4}, recovery.Hooks{}).Run(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if code := rep.ExitCode(); code != recovery.ExitOK {
		b, _ := json.MarshalIndent(rep, "", "  ")
		t.Fatalf("rebuild exit %d: %s", code, b)
	}

	if populated := reCompareTables(t, srcDB, dstDB); populated < 20 {
		t.Errorf("only %d journaled tables carried rows; the fixture no longer covers the formats", populated)
	}

	for _, g := range gets {
		wantCode, want := reGet(g.e.src, g.path)
		if wantCode != http.StatusOK {
			t.Fatalf("source GET %s = %d", g.path, wantCode)
		}

		if code, got := reGet(g.e.dst, g.path); code != wantCode || got != want {
			t.Errorf("GET %s after rebuild: %d, bytes equal=%t", g.path, code, got == want)
		}
	}

	// A second rebuild finds everything in place and changes nothing.
	rep2, err := recovery.NewRebuilder(dstDB.GetDB(), bs, newTestLogger(t), recovery.RebuildOptions{}, recovery.Hooks{}).Run(context.Background())
	if err != nil || rep2.ExitCode() != recovery.ExitOK {
		t.Fatalf("second rebuild: %v exit=%d", err, rep2.ExitCode())
	}
}
