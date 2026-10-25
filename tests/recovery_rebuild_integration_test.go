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
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
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

	cols := slices.Clone(tbl.Exclude)
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

// Generation timestamps are time.Now() by design: the maven <lastUpdated> and
// the deb Release "Date:" line are the only served differences after a rebuild.
var (
	reLastUpdated = regexp.MustCompile(`<lastUpdated>\d+</lastUpdated>`)
	reReleaseDate = regexp.MustCompile(`(?m)^Date: .*$`)
)

func reNormalise(body string) string {
	return reReleaseDate.ReplaceAllString(reLastUpdated.ReplaceAllString(body, "<lastUpdated>X</lastUpdated>"), "Date: X")
}

// reMountServed serves uploads and reads from separate routers (their route
// patterns overlap), dispatching on the method.
func reMountServed(upload, serve func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore)) func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler {
	return func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
		t.Helper()

		up, rd := chi.NewRouter(), chi.NewRouter()
		up.Use(authenticateTestBearer)
		upload(t, up, pg, bs)
		serve(t, rd, pg, bs)

		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodGet || req.Method == http.MethodHead {
				rd.ServeHTTP(w, req)

				return
			}

			up.ServeHTTP(w, req)
		})
	}
}

// reRegenerate runs the metadata workers the way `openartifact rebuild` does as
// its post step, for every repository and npm package of the database.
func reRegenerate(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore, mavenRepo, debRepo *qrEnv) {
	t.Helper()

	ctx := context.Background()
	log := newTestLogger(t)

	mw := worker.NewMavenMetadataWorker(pg, bs, pg.GetDB(), log, nil).WithDrainTimeout(2 * time.Minute)
	dw := worker.NewDebMetadataWorker(pg, bs, gpgkeys.New(pg, log, nil), log).WithDrainTimeout(2 * time.Minute)
	nw := worker.NewNPMMetadataWorker(pg, log, pg.GetDB(), nil).WithDrainTimeout(2 * time.Minute)

	mw.NotifyStale(mavenRepo.repoID, "com.acme", "app")
	dw.NotifyStale(debRepo.repoID)

	rows, err := pg.GetDB().QueryContext(ctx, `SELECT id::text FROM npm_packages`)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, id := range ids {
		if err := pg.MarkPackumentStale(ctx, id); err != nil {
			t.Fatal(err)
		}

		nw.NotifyStale(id)
	}

	mw.Stop()
	dw.Stop()
	nw.Stop()
}

// TestRecoveryRebuildServedMetadata proves that what clients are served from
// generated metadata (npm packument, maven-metadata.xml, deb Packages/Release)
// is the same after a rebuild, once both sides have been regenerated.
func TestRecoveryRebuildServedMetadata(t *testing.T) {
	srcDB := freshPostgres(t, 8)
	dstDB := freshPostgres(t, 8)

	bs, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	mvn := newREEnv(t, srcDB, dstDB, bs, "maven", reMountServed(
		func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
			t.Helper()
			api.NewMavenUploadHandler(newTestLogger(t), pg, bs, nil).Register(r)
		},
		func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
			t.Helper()
			r.Route("/repos/maven/{name}", formatmaven.NewHTTPHandler(newTestLogger(t), pg, bs).RegisterRepoRoutes)
		}))
	npm := newREEnv(t, srcDB, dstDB, bs, "npm", reMountServed(
		func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
			t.Helper()
			api.NewNPMUploadHandler(newTestLogger(t), pg, bs, "http://localhost", nil).Register(r)
		},
		func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
			t.Helper()
			r.Route("/repos/{name}", formatnpm.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes)
		}))
	deb := newREEnv(t, srcDB, dstDB, bs, "deb", reMountServed(
		func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
			t.Helper()
			api.NewDebUploadHandler(newTestLogger(t), pg, bs, nil).Register(r)
		},
		func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
			t.Helper()
			r.Route("/repos/{name}", formatdeb.NewHTTPHandler(newTestLogger(t), pg, bs).RegisterRepoRoutes)
		}))

	for _, v := range []string{"1.0.0", "1.1.0"} {
		body, _ := qrNPMBody(t, "qrpkg", v)
		npm.src.wantStatus("npm "+v, npm.src.npmPublish("qrpkg", body), http.StatusOK, http.StatusCreated)
		mvn.src.wantStatus("maven "+v, mvn.src.mavenPut("com/acme/app/"+v+"/app-"+v+".jar", qpfNoise(len(v)+int(v[2]), 500)), http.StatusCreated)
	}

	for _, ver := range []string{"1.0", "2.0"} {
		rec, _ := deb.src.debUpload(qrDeb(t, "qrdeb", ver, 40))
		deb.src.wantStatus("deb "+ver, rec, http.StatusOK, http.StatusCreated)
	}

	j := recovery.NewJournal(srcDB.GetDB(), bs, newTestLogger(t), nil, recovery.JournalConfig{Batch: 200, Concurrency: 4})
	if _, failed, err := j.Drain(context.Background()); err != nil || failed != 0 {
		t.Fatalf("drain: failed=%d err=%v", failed, err)
	}

	rep, err := recovery.NewRebuilder(dstDB.GetDB(), bs, newTestLogger(t), recovery.RebuildOptions{Concurrency: 4}, recovery.Hooks{}).Run(context.Background())
	if err != nil || rep.ExitCode() != recovery.ExitOK {
		t.Fatalf("rebuild: %v", err)
	}

	// The post step of `openartifact rebuild`, on both sides so both are generated.
	reRegenerate(t, srcDB, bs, mvn.src, deb.src)
	reRegenerate(t, dstDB, bs, mvn.dst, deb.dst)

	var arch string
	if err := srcDB.GetDB().QueryRowContext(context.Background(), `SELECT arch FROM deb_packages LIMIT 1`).Scan(&arch); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		env  *reEnv
		path string
		want string // a substring proving the document is not empty
	}{
		{"npm packument", npm, "/repos/qr-npm/qrpkg", "1.1.0"},
		{"maven-metadata.xml", mvn, "/repos/maven/qr-maven/com/acme/app/maven-metadata.xml", "<version>1.1.0</version>"},
		{"deb Packages", deb, "/repos/qr-deb/dists/stable/main/binary-" + arch + "/Packages", "Package: qrdeb"},
		{"deb Release", deb, "/repos/qr-deb/dists/stable/Release", "Packages"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantCode, want := reGet(tc.env.src, tc.path)
			if wantCode != http.StatusOK || !strings.Contains(want, tc.want) {
				t.Fatalf("source GET %s = %d, body %.300s", tc.path, wantCode, want)
			}

			gotCode, got := reGet(tc.env.dst, tc.path)
			if gotCode != wantCode || reNormalise(got) != reNormalise(want) {
				t.Errorf("GET %s after rebuild: %d\n want %.800s\n  got %.800s", tc.path, gotCode, reNormalise(want), reNormalise(got))
			}
		})
	}
}
